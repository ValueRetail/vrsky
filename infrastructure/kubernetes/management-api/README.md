# management-api — Kubernetes manifests

The VRSky control plane: REST/UI backend, auth, tenant + connection management,
and service-token issuance to workers.

## Files

1. **deployment.yaml** — Deployment (`replicas: 2`, rolling update, probes)
2. **service.yaml** — ClusterIP (API on 8080→3000, metrics on 9090)
3. **pdb.yaml** — PodDisruptionBudget (`minAvailable: 1`)

## High Availability (#138)

The management-api runs **N≥2 replicas** behind the Service / Traefik gateway,
with no single point of failure. It is designed to be **horizontally scalable**:
bump `replicas` in `deployment.yaml` (or attach an HPA) and add capacity.

### Why it's safe to run multiple replicas

| Concern | Status under N replicas |
| --- | --- |
| **Auth / sessions** | Stateless. Sessions are rows in Postgres (`CreateSession`); the cookie carries a token validated against the DB on every request. Any replica can serve any request. |
| **OAuth token refresh** | **Gated cluster-wide.** Each grant is refreshed only by the replica that wins that grant's **Postgres advisory lock** (`oauth_refresher.go` → `withAdvisoryLock`). N replicas no longer race on refresh-token rotation. |
| **Usage rollup** (hourly) | **Gated cluster-wide** by a fixed advisory lock — one replica does the rollup per tick; the rest skip. (Upserts are idempotent anyway; this removes wasted work + write contention.) |
| **Tenant provisioning** | Per-replica but request-scoped: the job runs only on the replica that handled the create request — it is never double-run. |
| **Metrics cache** | Per-replica read cache. Independent caches mean slightly more Prometheus load, no correctness issue. |
| **Rate limiters** (`QuotaTracker`, `ConnectionRateLimiter`) | **Per-replica by design** (see below). |

The advisory-lock primitive lives in `src/pkg/managementapi/dblock.go`. It uses
transaction-scoped locks (`pg_try_advisory_xact_lock`) so a lock can never leak
on crash/cancel, and it needs no extra infrastructure — Postgres is already the
shared, HA (#137) coordination point, avoiding a Kubernetes Lease/RBAC just to
gate two timers.

### Known trade-offs

- **Rate limiters are per-replica.** `QuotaTracker` (max_msg_per_sec) guards only
  the synthetic **test-message generator**, and `ConnectionRateLimiter` guards
  the data-sharing read endpoint. With N replicas a tenant's effective limit can
  reach N× the configured value. This is an accepted over-permit on
  control-plane convenience traffic — the real data path is enforced by
  workers + per-tenant NATS quotas, not here. If either limiter ever needs to be
  exact cluster-wide, back it with a shared counter (NATS-KV keyed by
  tenant/connection id).
- **Server-Sent Events (provisioning progress, live tenant updates) are
  per-replica.** A browser connected to replica B won't receive an event
  broadcast on replica A. Mitigate by enabling **sticky sessions** on the SSE
  routes at Traefik (cookie-based affinity) — Service-level `sessionAffinity:
  ClientIP` does *not* help, because behind the gateway all clients share the
  gateway's source IP. The robust fix (fan SSE out over NATS so every replica's
  hub sees every event) is a follow-up; the UI already reconnects/refetches, so
  status still converges. Core CRUD/auth/token APIs are unaffected.

### Zero-downtime rollouts

`maxUnavailable: 0` + `maxSurge: 1` brings a new replica to Ready before
removing an old one; the old replica drains in-flight requests on SIGTERM
(readiness flips first — the `/readyz` gate + graceful drain from #85). A
rolling restart therefore drops zero requests.

## Deploy

```bash
kubectl apply -f service.yaml
kubectl apply -f deployment.yaml
kubectl apply -f pdb.yaml

kubectl rollout status deployment/vrsky-management-api -n vrsky-platform
kubectl get pods -n vrsky-platform -l app=vrsky-management-api   # expect 2 Running
```

### Verify HA behavior

```bash
# OAuth refresh runs once cluster-wide: only one replica logs a refresh per
# grant; others log "grant locked by another replica; skipping" at debug.
kubectl logs -n vrsky-platform -l app=vrsky-management-api --prefix | grep "oauth refresh"

# Rolling restart drops zero requests (run a load generator against the API
# while this proceeds):
kubectl rollout restart deployment/vrsky-management-api -n vrsky-platform
```

The advisory-lock mutual exclusion is covered by a Go integration test:

```bash
# against any reachable Postgres
MGMT_TEST_DB_URL="postgres://user:pass@host:5432/db?sslmode=disable" \
  go test ./pkg/managementapi/ -run TestWithAdvisoryLock_MutualExclusion -v
```
