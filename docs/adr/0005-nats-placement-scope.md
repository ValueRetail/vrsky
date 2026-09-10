# ADR 0005 — Tenant NATS placement governs instance lifecycle, not data routing

- **Status:** Accepted (2026-09-10)
- **Date:** 2026-09-10
- **Deciders:** Ludvik
- **Relates to:** [#209](https://github.com/ValueRetail/vrsky/issues/209),
  [#19](https://github.com/ValueRetail/vrsky/issues/19) (tenant NATS placement),
  [ADR 0004](0004-standing-connector-services.md), `docs/scalability.md`

## Context

#19 placed each connection on a tenant NATS instance. #209 found that the
placement no longer reaches the data plane: `WithNATSURLResolver` /
`configForConn` resolve the instance URL and override `OrchestratorConfig.NATSURLs`,
but that value's only consumer was the `NATS_URLS` env stamped on the
per-connection worker Deployments, which ADR 0004 stopped creating. The standing
connector services dial the `NATS_URL` in their own pod env, which
`deploy-connectors-azure.sh` points at the platform NATS.

### What placement still does, and what it does not

Not all of #19 is stranded. Four consumers read the `nats_instances` rows, and
three of them work:

| Consumer | Reads | Status |
|---|---|---|
| `NATSAutoscaler` | placed-connection count per instance | **Works** — provisions and decommissions on `integrations >= maxIntegrations` |
| `NATSHealthMonitor` | active instances | **Works** — probes each instance, marks dead ones |
| `nats_instances_handler` | instances, per-connection placement | **Works** — the API and UI view |
| `configForConn` → `NATSURLs` | resolved instance URL | **Dead** — nothing reads the value |

The autoscaler's *other* trigger — scraped message rate — is dead for the same
reason, which is why #226 stopped publishing `vrsky_nats_instance_connections`
and `vrsky_nats_instance_msg_rate`. A flat zero beside fifty placed connections
reads as "this tenant is idle" rather than "no traffic was ever routed here".

### The fact that decides this

**A tenant NATS instance cannot carry VRSky's data plane as provisioned, and not
for want of wiring.**

| | Platform NATS | Tenant NATS instance |
|---|---|---|
| Workload | Clustered StatefulSet | Single-replica Deployment |
| JetStream | `--jetstream`, `store_dir: /data/jetstream` | **not enabled** |
| Storage | PersistentVolumeClaim | none (ephemeral) |
| Source | `infrastructure/kubernetes/platform-nats/` | `k8s_nats_provisioner.go`, `tenant-nats/provision-tenant-nats.sh` |

The provisioner passes `--port`, `--http_port` and `--server_name` and nothing
else. The data plane is JetStream throughout — the `VRSKY_DATA` stream, pull
durables per connector, the DLQ, and the at-least-once guarantee from #70. Core
NATS cannot serve any of it.

So #209 understates the gap. It is not that a dial plan was never wired; the
target was never built to receive the traffic. Every option that routes tenant
data to a tenant instance must first re-provision those instances with
JetStream, persistent storage, and a stream/durable topology per tenant — before
any of the connector-side work begins.

### Constraints

- **No customer has asked for a dedicated instance.** Prod runs a single
  platform NATS; the multi-instance path has never been exercised with traffic.
- **The cluster is cost-parked** between pilots, and no connector has yet run
  against a live vendor API. Isolation is not the binding constraint on adoption.
- **Standing services serve every tenant.** A service cannot be pinned to one
  tenant's NATS the way a per-connection pod could.

## Decision

**Narrow #19's scope on purpose: placement governs instance lifecycle and
accounting, not data-plane routing.** Tenant data flows over platform NATS.

Concretely:

1. Keep the autoscaler, the health monitor and the API/UI view — they work and
   are what placement is for under this scope.
2. Remove the dead resolver path (`WithNATSURLResolver`, the `NATSURLs`
   override in `configForConn`) or reduce it to accounting, so nothing looks
   like routing that is not.
3. **Guard the promise.** Refuse to provision a tenant instance while the data
   plane cannot use it, or surface it unmistakably in the API and UI. The named
   risk in #209 is placement *appearing* to succeed; a guard is what makes that
   impossible rather than merely documented.
4. Record the reopening condition: the first time a tenant is to be *sold* or
   promised NATS isolation, this ADR is superseded and one of the options below
   is implemented — starting with JetStream on tenant instances.

This is deliberately the option that builds nothing. The other three are real
work in service of a requirement nobody has stated, and all three carry the same
unbuilt prerequisite.

## Options considered

### A. Per-tenant instances of the connector services

| Dimension | Assessment |
|---|---|
| Complexity | High — a Deployment set per tenant, per connector kind |
| Cost | Reintroduces fan-out; per tenant rather than per connection, but ADR 0004 removed exactly this shape |
| Scalability | Reverses the ceiling ADR 0004 lifted (`docs/scalability.md` ceiling 2) |
| Prerequisite | JetStream + storage on tenant instances |

**Pros:** true isolation, both data and blast radius; each service dials one NATS, so the connector code is unchanged.
**Cons:** the pod-per-tenant cost curve across the 30 connector services `deploy-connectors-azure.sh` deploys, several at two replicas; contradicts the ADR it would amend.

### B. Connectors hold connections to several NATS instances

| Dimension | Assessment |
|---|---|
| Complexity | High — per-message instance selection inside every connector |
| Cost | Low infrastructure, high code and failure-mode surface |
| Scalability | Connection count per connector grows with tenant count |
| Prerequisite | JetStream + storage on tenant instances |

**Pros:** no extra pods; isolation without fan-out.
**Cons:** every connector gains multi-cluster durable management, per-instance reconnect and backpressure. The SDK currently hands each connector one `*nats.Conn`; this changes the SDK contract for all of them, and gets a per-message routing decision on the hot path.

### C. Superclusters / leafnodes

| Dimension | Assessment |
|---|---|
| Complexity | High, and concentrated in infrastructure rather than code |
| Cost | Gateway/leafnode topology to operate and debug |
| Scalability | Good — NATS is designed for this |
| Prerequisite | JetStream + storage + clustering on tenant instances |

**Pros:** connectors keep one logical connection; isolation becomes a NATS concern rather than an application one.
**Cons:** the largest prerequisite of the three — tenant instances become clustered, JetStream-enabled, persistent servers. Cross-account JetStream semantics are subtle, and nobody here has operated a supercluster.

### D. Narrow the scope (**recommended**)

| Dimension | Assessment |
|---|---|
| Complexity | Low — deletion and a guard |
| Cost | None |
| Scalability | Unchanged; platform NATS is not the current ceiling (`docs/scalability.md` puts single Postgres first) |
| Prerequisite | None |

**Pros:** honest today; costs nothing; keeps the three working consumers; leaves A–C fully open.
**Cons:** no data isolation. If a prospect demands it, this becomes a sales blocker, and the answer is then a project rather than a switch.

## Trade-off analysis

The choice is not really between four architectures. It is between building
isolation now on speculation, or recording plainly that we do not have it.

A, B and C all require the same first step nobody has taken — making tenant
instances capable of hosting JetStream. That step alone is most of the work, and
it is unfalsifiable until a real tenant with real traffic exists to size it. B
additionally changes the SDK contract every connector depends on, for a routing
decision on the hot path; C requires operating a topology no one on the project
has run.

D's cost is a future conversation. A, B and C's cost is present work whose
requirements are guesses. Given no customer has asked, no connector has yet
touched a live vendor API, and the cluster is parked between pilots, speculation
is the more expensive error.

The risk D must carry is the one #209 names: placement that *looks* like it
worked. That is why the guard in decision 3 is not optional. Documentation alone
would leave exactly the trap #209 was filed about.

## Consequences

**Easier**
- One data-plane topology to reason about, monitor and back up.
- The autoscaler and health monitor keep working, on the signal that is real.
- No dead code implying a capability that does not exist.

**Harder**
- Selling NATS isolation becomes a lead time, not a configuration change.
- Noisy-neighbour pressure is handled by platform NATS capacity and per-tenant
  quotas (#74), not by separation.

**To revisit**
- The first serious request for a dedicated instance supersedes this ADR.
- If `docs/scalability.md` ceiling 1 (single Postgres) is lifted and platform
  NATS becomes the next ceiling, re-evaluate — C is then the natural answer.

## Action items

1. [x] Accept or reject this ADR — **accepted 2026-09-10**.
2. [x] Remove the dead resolver path. `NATSURLResolver`, `WithNATSURLResolver`,
   `FactoryOption` and `configForConn` are gone; the adapter holds one config.
   `adapter_nats_url_test.go` keeps the history and points here.
3. [x] Guard provisioning. `ProvisionNATSInstance` refuses with
   `ErrTenantNATSNotRoutable` unless `VRSKY_ALLOW_TENANT_NATS=1`.
4. [x] Name the data plane in the API: `data_plane: "platform-nats"` on the
   nats-instances payload, beside the `urls` that read like routing.
5. [x] Re-scope #19 and close #209 against this ADR. #209 closed COMPLETED by
   #242 with the outcome recorded on it; the narrowed scope and the JetStream
   prerequisite are on #19, and the "discovery returns URLs nothing routes to"
   constraint is on #21.
6. [x] The JetStream prerequisite is recorded above and in the guard's own
   comment — the place someone lands when they try to provision one.
