# Tier-3 scalability validation (closing epic #140)

The production-scalability epic (#140) is **code-complete and merged** — autoscaling
(#135), object-storage HA (#136), PostgreSQL HA (#137), management-api HA (#138),
AckWait/redelivery (#139), and tenant NATS service discovery + autoscaling
(#21, #19). None of it can be exercised in the single-host docker-compose stack
(it's inert behind the `NATS_URL` fallback and needs real K8s control loops), so
**closing #140 requires one validation pass on a real Kubernetes cluster.**

This runbook is that pass: it maps every #140 Definition-of-Done item to a
concrete check with explicit pass/fail criteria. `infrastructure/scripts/validate-tier3.sh`
automates the happy path; the manual steps here are the source of truth.

## Prerequisites

- A Kubernetes cluster. Local is fine: `k3d cluster create --config infrastructure/kubernetes/k3d-config.yaml`.
- `kubectl` pointed at it; `helm` for the operators below.
- **metrics-server** (for the worker HPA, #135): `kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml`.
- For **PostgreSQL HA** (#137): the CloudNativePG operator — `kubectl apply --server-side -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.24/releases/cnpg-1.24.0.yaml`.
- The management-api image applies DB migrations on start (golang-migrate, see its `entrypoint.sh`), so a fresh cluster DB reaches migration **000018** automatically — no manual schema step.

## Deploy

```bash
# Core platform (namespaces, platform NATS, postgres, minio, management-api, workers, monitoring)
infrastructure/kubernetes/deploy-vrsky-platform.sh

# Swap in the HA variants the deploy script doesn't apply by default:
kubectl apply -f infrastructure/kubernetes/postgresql/cnpg-cluster.yaml   # #137 (replaces single statefulset)
kubectl apply -f infrastructure/kubernetes/postgresql/cnpg-pooler.yaml
kubectl apply -f infrastructure/kubernetes/minio/statefulset-distributed.yaml  # #136 (replaces deployment.yaml)
kubectl apply -f infrastructure/kubernetes/management-api/pdb.yaml              # #138
# (management-api deployment.yaml already sets replicas: 2)
```

## Validation checks → #140 Definition of Done

### 1. A connection autoscales its workers under load — #135
- Deploy a connection; drive sustained load (see `docs/LOAD.md` harness).
- **Pass:** `kubectl get hpa -n vrsky` shows the connection's worker HPA, and replica count rises above `minReplicas` under load and falls after. No manual replica edits.

### 2. No single-replica SPOF — #136 / #137 / #138
- **Postgres (#137):** `kubectl get cluster -n vrsky-database vrsky-pg` shows 3 instances, 1 primary. Delete the primary pod → a standby is promoted, the app reconnects via the pooler, no data loss.
- **Object storage (#136):** `kubectl get pods -n vrsky-storage` shows the 4-node distributed MinIO StatefulSet. Delete one pod → reads/writes continue (EC:2 tolerates it).
- **management-api (#138):** `kubectl get pods -n vrsky-platform -l app=vrsky-management-api` shows 2 Running. `kubectl rollout restart deploy/vrsky-management-api -n vrsky-platform` while hitting the API → zero failed requests; OAuth refresh logs appear on only one replica (advisory lock).
- **Pass:** each component survives a single-pod loss with no request/data loss.

### 3. Onboarding the Nth tenant needs no manual NATS wiring — #21 / #19
- Provision a tenant: `infrastructure/kubernetes/tenant-nats/provision-tenant-nats.sh <tenant> 1`.
- **Discovery (#21):** `curl …/api/v1/tenants/{id}/nats-instances` lists the instance + a `nats://…:4222` URL; the tenant's workers connect via it (worker logs: "NATS discovery resolved tenant instances").
- **Health (#21):** delete the tenant's NATS pod → within ~30s the instance flips `unhealthy` and drops out of the discovery response; restore → back to `active`.
- **Autoscaling (#19):** drive the tenant past a trigger (≥50 connections, or sustained >100k msg/s). **Pass:** the autoscaler provisions instance #2 (`kubectl get pods -n vrsky-tenants`), `vrsky_nats_instance_capacity_pct` crosses 80 (the `NATSInstanceApproachingCapacity` alert fires), and new connections place onto the new instance (`SELECT nats_instance_id, count(*) FROM connections GROUP BY 1`). Drain a tenant's connections off an extra instance → it's decommissioned.

### 4. Re-measure end-to-end throughput on the scaled cluster — record in `docs/LOAD.md`
- Run the flagship scenario against the clustered deployment: `tests/load/run.sh --rate 20000 --duration 60s webhook-to-http` (point it at the cluster ingress).
- **Pass:** record sustained msg/s, p99, and error rate under the scaled topology in `docs/LOAD.md` (a new "Scaled cluster (#90/#140)" section), replacing the single-host caveat for the headline ceiling.

## Closing out #140

When all four checks pass:
1. Tick the four Definition-of-Done boxes in #140.
2. Close **#19** and **#21** with a comment linking the validation run (paste the `kubectl`/curl evidence).
3. Close **#140**.
4. If the throughput numbers supersede the deferral note, update `docs/LOAD.md` and reference #90.

## Validation run — k3d, 2026-06-30

Validated the control plane live on a local k3d cluster (1 server + 2 agents):

- **#138** — management-api ran **2/2 replicas** `1/1` behind the Service; rolling
  restart kept serving.
- **DB / migrations** — `golang-migrate` applied **000001 → 000018 cleanly** on a
  fresh DB (`schema_migrations: 18 | f`).
- **#21 health monitor** — `nats health monitor started` in the logs.
- **#19 autoscaler** — `nats autoscaler started ... k8s=true` (live
  K8sNATSProvisioner, not the inert compose path).
- **#21 discovery API (end-to-end)** — registered a tenant, inserted a
  `nats_instances` row, and `GET /api/v1/tenants/{id}/nats-instances` returned the
  instance + `nats://…:4222` URL (Bearer auth → tenant-scoping → URL formatting).

### Fresh-deploy bugs fixed during this run (all on this branch)

These broke *any* clean deploy — several would hit production identically:

1. `k3d-config.yaml` — rejected `switchContext`; host ports collided with the compose stack.
2. Platform NATS StatefulSet — clustered JetStream bootstrap **deadlock** (no `podManagementPolicy: Parallel`; liveness used full `/healthz`).
3. Platform NATS Service — governing service was `ClusterIP`, not headless → peer DNS never resolved.
4. Missing `secret.yaml` templates (Postgres, MinIO, management-api) — added committed `secret.example.yaml` + deploy fallback.
5. Resource **requests** oversized (NATS/Postgres/MinIO 1–2 CPU / 2–4 Gi each) → `Insufficient memory` on modest nodes.
6. Image delivery — manifests referenced unreachable registries; added `k3d-load-images.sh`.
7. management-api **RBAC** — `default` SA couldn't provision tenant NATS / deploy workers.
8. management-api missing `DB_HOST`/`DATABASE_URL` env (entrypoint defaulted to the compose host).
9. **`nats_instances` created by no migration** — only existed in legacy `init-schema.sql`; a migrate-only (production) DB lacked it and `000018`'s FK failed → folded the table into `000018` with a status CHECK covering the values the code writes.
10. Loading legacy `init-schema.sql` *and* `golang-migrate` → dirty schema; stopped loading init-schema (migrate is the source of truth). Plus management-api missing `ENCRYPTION_KEY`, and the legacy filter's `NATS_URL` env name/value.

### Still to validate (need more setup / a fuller cluster)

- #135 worker HPA scaling + #19 scale-up under sustained load (orchestrator + load gen).
- #137/#136 HA failover (apply the CNPG / distributed-MinIO manifests, then pod-kill).
- Throughput re-measure on the scaled topology → `docs/LOAD.md`.

## What this can't cover here

This environment is single-host (no K8s), so the run itself must happen on your
cluster. Everything up to that point — the manifests, migrations, control loops,
metrics, and this runbook — is in place. `validate-tier3.sh` wraps the deploy +
checks 1–3 into one command with pass/fail output; check 4 (throughput) is run
explicitly because it needs a load target.
