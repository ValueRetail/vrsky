# VRSky Scalability — Assessment & Roadmap

> Grounded in the current architecture. **Caveat:** prod has 0 real connections and
> the cluster is cost-parked, so this is an *architectural* assessment, not an
> empirical benchmark. A k6 load-smoke harness exists (`.github/workflows/
> load-smoke.yml`, p99 + error-rate regression guards) but hasn't been run at
> target scale — doing so is how we'd find the real ceiling.

## Verdict

The building blocks are the right ones for horizontal scale, and multi-tenancy is
genuinely built in — not bolted on. The main limit is **a single PostgreSQL**;
that is not a rewrite. The pod-per-connection worker model was the other named
ceiling and is **gone** — see below.

## What scales well (by design)

- **Shared connector services on JetStream pull durables** — one standing service
  per node type (`file-consumer`, `http-producer`, `data-filter`, …) multiplexes
  every connection of that type, routing by node config. Because they are **pull**
  durables, replicas of a service share the work without coordination, so the
  destination side scales by replica count rather than by connection count.
- **Multi-tenancy** — **per-tenant NATS instances** (`nats_instances`, migration
  000018), tenant-scoped queries throughout (`lint:tenant-ok`), enforced **quotas**
  (default 50 msg/s, 10 integrations, 1 GiB/tenant) + daily usage metering.
- **Stateless control plane** — UI / filter / management-api at 2 replicas + PDB;
  horizontally scalable, HA today.
- **Independent connectors** — adding an integration type scales linearly.

## Ceilings (priority order)

### 1. Single in-cluster PostgreSQL  ← highest priority
The management DB is one StatefulSet instance that every connection, tenant, and
usage-metering write hits.

Retiring the per-connection workers (#201/#205) took most of the pressure off:
pool count now scales with the number of **standing services** (a few dozen pods,
fixed) instead of with connection count, so `max_connections` exhaustion is no
longer a fan-out risk. What is left is ordinary single-instance load — every
connector polls this DB for per-connection config, and every usage write lands
here — which is why it is still the top ceiling, just a much less steep one.

**Fix (staged):**
- **(a) Bound every worker's connection pool** in code — **✅ done.** Pools already
  existed but were monolith-sized (25 open / 5 idle per worker, no idle reaping),
  which is exactly what multiplied across the old pod-per-connection fan-out. Now each
  worker defaults to a lean **6 open / 2 idle** with a **90 s `ConnMaxIdleTime`**
  so idle pollers stop pinning connections; the control plane keeps 25/5. All
  values are env-tunable (`DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS`,
  `DB_CONN_MAX_LIFETIME_SECONDS`, `DB_CONN_MAX_IDLE_SECONDS`) for per-deployment
  sizing. Caps the connection explosion; PgBouncer (b) is the next multiplier.
- **(b) PgBouncer** (transaction pooling) in front of Postgres — one more multiplier
  of headroom.
- **(c) Managed Postgres** (Azure DB for PostgreSQL Flexible Server) + read replicas
  for the read-heavy paths (usage/metrics, connection listing).

### 2. ~~Pod-per-connection orchestration~~ — resolved (#201, #205)
This described each connection costing 1–2 pods + an HPA, with a practical ceiling
of a few thousand connections per cluster. The recommended fix was a "shared
multi-tenant worker-pool mode".

**It turned out the platform already worked that way, and the pods were the
vestige.** The standing connector and transform services multiplex every
connection off the shared `vrsky.data.*` subjects; the per-connection Deployments
the orchestrator spawned alongside them were wired to
`{tenant}.pipeline-{conn}.{node}.output` topics nothing publishes to, so they did
no work at all. #201 stopped spawning them for transform nodes and #205 for edge
nodes, which removes the ceiling outright: **connection count now costs rows in
`connections`, not pods.**

What remains is the *replica* story for the standing services, which is smaller
and better understood: pull-durable producers scale freely (2 replicas today),
while pollers (consumers) and connectors serving per-pod SSE streams are pinned to
one replica until they get leader election and a shared event bus respectively.

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
| ~~Shared worker-pool mode~~ ✅ | 2 | — | no — **done** (#201/#205) |
| PgBouncer | 1b | small (infra) | yes ← **next** |
| Leader election for polling consumers | 2 | medium (code) | for load test |
| Managed Postgres / Blob | 1c / 3 | medium (infra + migration) | yes |
| Run k6 at target scale | all | small | yes |

## Empirical next step

Run the k6 load-smoke at realistic rates + connection counts to turn this
architectural read into measured limits — that's the only way to know the real
ceiling and validate each fix.
