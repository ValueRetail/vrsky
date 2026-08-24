# VRSky Scalability — Assessment & Roadmap

> Grounded in the current architecture. **Caveat:** prod has 0 real connections and
> the cluster is cost-parked, so this is an *architectural* assessment, not an
> empirical benchmark. A k6 load-smoke harness exists (`.github/workflows/
> load-smoke.yml`, p99 + error-rate regression guards) but hasn't been run at
> target scale — doing so is how we'd find the real ceiling.

## Verdict

The building blocks are the right ones for horizontal scale, and multi-tenancy is
genuinely built in — not bolted on. The limits are **(1) a single PostgreSQL** and
**(2) the pod-per-connection worker model**; neither is a rewrite.

## What scales well (by design)

- **Per-connection worker autoscaling** — the orchestrator gives each connection a
  Deployment + **HPA** (`MinReplicas`/`MaxReplicas`/`TargetCPUPercent`), and NATS
  **JetStream durable consumers load-balance across replicas**, so a connection's
  throughput scales out.
- **Multi-tenancy** — **per-tenant NATS instances** (`nats_instances`, migration
  000018), tenant-scoped queries throughout (`lint:tenant-ok`), enforced **quotas**
  (default 50 msg/s, 10 integrations, 1 GiB/tenant) + daily usage metering.
- **Stateless control plane** — UI / filter / management-api at 2 replicas + PDB;
  horizontally scalable, HA today.
- **Independent connectors** — adding an integration type scales linearly.

## Ceilings (priority order)

### 1. Single in-cluster PostgreSQL  ← highest priority
The management DB is one StatefulSet instance that every connection, tenant, and
usage-metering write hits. With pod-per-connection fan-out, each worker holds a DB
pool — connection count multiplies fast and can exhaust `max_connections`.

**Fix (staged):**
- **(a) Bound every worker's connection pool** in code — **✅ done.** Pools already
  existed but were monolith-sized (25 open / 5 idle per worker, no idle reaping),
  which is exactly what multiplies across pod-per-connection fan-out. Now each
  worker defaults to a lean **6 open / 2 idle** with a **90 s `ConnMaxIdleTime`**
  so idle pollers stop pinning connections; the control plane keeps 25/5. All
  values are env-tunable (`DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS`,
  `DB_CONN_MAX_LIFETIME_SECONDS`, `DB_CONN_MAX_IDLE_SECONDS`) for per-deployment
  sizing. Caps the connection explosion; PgBouncer (b) is the next multiplier.
- **(b) PgBouncer** (transaction pooling) in front of Postgres — one more multiplier
  of headroom.
- **(c) Managed Postgres** (Azure DB for PostgreSQL Flexible Server) + read replicas
  for the read-heavy paths (usage/metrics, connection listing).

### 2. Pod-per-connection orchestration
Each connection = 1–2 pods (src/dst) + an HPA. Elegant isolation, but a practical
ceiling around a few thousand connections/cluster (etcd/scheduler/object pressure,
cost), and wasteful for many small or idle connections.

**Fix:** a **shared multi-tenant worker-pool mode** for light connections (one
worker process multiplexing many connections off the shared NATS subjects), keeping
dedicated pods only for heavy/isolated ones. The connector SDK already loads
per-connection config by ID, so a pool worker is a routing layer on top — not a
connector rewrite. This is the biggest *architectural* lever for density + cost.

### 3. In-cluster stateful services
Postgres, MinIO, NATS all run in-cluster. For scale:
- Storage → **Azure Blob** (the cloud-storage connector already speaks S3/Azure/GCS).
- Postgres → managed (see #1c).
- NATS is 3-pod HA, but per-tenant instances add pod count — consider **shared NATS
  instances for small tenants**, dedicated for large (density vs isolation).

### 4. Node capacity + Azure quota
Currently 2× E4bds_v5 (~8% used), previously EBDSv5-quota-constrained. Real scale
needs the **cluster autoscaler** + quota headroom.

## Immediate roadmap

| Step | Ceiling | Effort | Needs cluster? |
|---|---|---|---|
| ~~Bound worker DB pools~~ ✅ | 1a | small (code) | no — **done** |
| PgBouncer | 1b | small (infra) | yes ← **next** |
| Shared worker-pool mode | 2 | large (code) | for load test |
| Managed Postgres / Blob | 1c / 3 | medium (infra + migration) | yes |
| Run k6 at target scale | all | small | yes |

## Empirical next step

Run the k6 load-smoke at realistic rates + connection counts to turn this
architectural read into measured limits — that's the only way to know the real
ceiling and validate each fix.
