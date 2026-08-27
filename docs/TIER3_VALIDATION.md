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
- **Prereq (#157):** the management-api runs the per-connection orchestrator only
  when `ORCHESTRATOR_MODE=k8s` (set in `management-api/deployment.yaml`). Import the
  generic worker images first: `infrastructure/scripts/k3d-load-images.sh workers`
  (builds `gcr.io/vrsky/vrsky-{consumer,filter,converter,producer}:latest`).
- Deploy a graph connection (≥1 node) and start it; drive sustained load (see `docs/LOAD.md` harness).
- **Pass:** starting the connection creates a `Deployment` + `HorizontalPodAutoscaler`
  per node (`kubectl get deploy,hpa -n vrsky-platform -l pipeline=<connectionID>`);
  replica count rises above `minReplicas` under load and falls after (no manual replica
  edits); stopping the connection removes the Deployments + HPAs.

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
- **#136 object-storage HA failover** — swapped to the 4-node distributed MinIO
  StatefulSet, wrote an object, **deleted a MinIO pod**, and read the object back
  through the surviving nodes (EC:2 tolerated the loss).
- **#137 PostgreSQL HA failover** — stood up the 3-instance CloudNativePG cluster,
  wrote a row through the `-rw` service, **deleted the primary** (`vrsky-pg-1`),
  and CNPG **promoted a standby** (`vrsky-pg-2`) — the row survived (zero data
  loss). This completes the **no-single-replica-SPOF** DoD item (MinIO + Postgres
  + management-api all proven HA).

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

### Blocked / deferred

- **Throughput re-measure on the scaled topology** → `docs/LOAD.md` (tracked on
  **#15**). Needs a load generator against the cluster ingress; the per-connection
  workers now run and autoscale (below), so this is unblocked.

## Validation run — k3d, 2026-07-20 (#157 orchestrator + #135 autoscaling)

Validated the per-connection orchestrator (#157) and worker autoscaling (#135)
end-to-end on the local k3d cluster. Imported the generic worker images
(`k3d-load-images.sh workers` → `vrsky-{consumer,producer,filter,converter}`),
enabled `ORCHESTRATOR_MODE=k8s`, and drove a `consumer → producer` graph
connection through its lifecycle:

- **Start → Deployment + HPA per node (#157).** Starting the connection created
  `vrsky-<conn>-consumer-0` and `vrsky-<conn>-producer-0` Deployments **and** an
  HPA each (min 1 / max 10 / 75% CPU); both worker pods reached `Running`.
- **Autoscale under load (#135) — PASS.** Flooding the consumer's `POST /webhook`
  input drove its CPU to ~620%; the **consumer HPA scaled 1 → 4 → 8 → 10**
  (maxReplicas), `10/10` available. The **producer HPA stayed at 1** (cpu ~2%) —
  only the node actually under load scaled, confirming per-node HPAs act
  independently. Scale-down follows the standard ~5-min HPA stabilization window.
- **Stop → teardown (#157).** Stopping the connection removed both Deployments and
  both HPAs (`deploy=0 hpa=0` within ~2s).

### Bugs fixed during this run (all on the #159 branch)

1. **Connection status never persisted.** `StartConnection`/`StopConnection` set
   `conn.Status` in memory then called the general `UpdateConnection`, whose SQL
   never writes the `status` column — so connections stayed `stopped` in the DB
   even while running. The stop path's `status == "stopped"` idempotency guard then
   skipped orchestrator teardown, **leaking worker Deployments + HPAs on every
   stop**. Fixed by routing both through `UpdateConnectionStatus`. (Latent before
   #157 — status was cosmetic; load-bearing once teardown depends on it.)
2. **Orchestrator worker images used the default pull policy.** With `:latest` that
   defaults to `Always`, so pods `ErrImagePull`'d against the unreachable
   `gcr.io/vrsky` even when the image was k3d-imported. Set `imagePullPolicy:
   IfNotPresent` on the generated Deployments.
3. **`vrsky-converter` image wouldn't build** (`cmd/converter/Dockerfile`): forced
   `CGO_ENABLED=1 GOARCH=amd64` (uncompilable on arm64; the converter needs CGO for
   wasmtime) into an alpine/musl runtime a glibc binary can't load. Now builds for
   the native arch on a glibc (`debian-slim`) runtime.

### Worker config note

> **Superseded — do not follow.** The generic workers this describes were retired
> in #201/#205 and deleted with `pkg/runtime` (ADR 0004). The `input_type` /
> `output_type` config shape below was in fact never what the UI wrote, which is
> why those workers could not serve a real node. Node config is read by the
> standing connector services, keyed on the node's `type`.

The generic `pkg/runtime` workers the orchestrator deploys take their source/sink
config from the node `config` JSON: a consumer needs `{"input_type":...,
"input_config":{...}}` (default `http` → `POST /webhook` on port 8000) and a
producer needs `{"output_type":"nats|http","output_config":{...}}` (the default
`file` output type is not supported by that runtime). An empty `{}` config makes a
worker crash-loop on startup — valid config is required to exercise a pipeline.

## Validation run — k3d, 2026-07-20 (#160 tenant provisioning → discovery)

Validated the tenant NATS provisioning → service-discovery chain (#21) end-to-end.
`POST /api/v1/tenants` (the path that enqueues a provisioning job — the *register*
path does not) drove the full flow:

- **Provisioner creates the instance.** The `K8sNATSProvisioner` created a
  `nats-<slug>-1` **Deployment + Service (4222/8222) + NetworkPolicy** (tenant-id
  isolation) in `vrsky-tenants`; the pod reached `1/1 Running` and the provisioning
  job completed (`status=completed`, `progress=100`, "NATS instance ready").
- **Discovery returns it (#21).** `GET /api/v1/tenants/{id}/nats-instances` returned
  the instance with `status: active` and its
  `nats://nats-<slug>-1.vrsky-tenants.svc.cluster.local:4222` URL.
- **DB.** A `nats_instances` row (instance 1, `active`) was written by the
  provisioner (not manually) — closing the gap left by the 2026-06-30 run, which
  had inserted that row by hand.

### Bug fixed during this run

- **Tenant NATS pod crash-looped → provisioning always failed.** The provisioner
  passed `--max_payload 8MB` and `--max_connections 1000` as nats-server **CLI
  flags**, but those are config-file-only options — nats-server prints usage and
  `exit(0)`, so the pod `CrashLoopBackOff`'d and `waitForDeployment` timed out
  (`provisioning_jobs.status = failed`). This broke tenant provisioning on *any*
  cluster. Fixed by dropping the invalid flags (defaults are fine); also
  right-sized the pod's oversized `1 CPU / 2Gi` requests to `250m / 512Mi` to match
  the platform components.

## Where these runs happen

The CI/sandbox environment is single-host (no Kubernetes), so a Tier-3 run must
happen on a real cluster — the validation runs recorded above were done on a
local **k3d** cluster (1 server + 2 agents). Everything the runbook needs — the
manifests, migrations, control loops, metrics — is in the repo.
`validate-tier3.sh` wraps the deploy + checks 1–3 into one command with pass/fail
output; check 4 (throughput) is run explicitly because it needs a load target.
