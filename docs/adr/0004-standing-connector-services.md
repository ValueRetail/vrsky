# ADR 0004 — Standing connector services replace per-connection workers

- **Status:** Accepted (2026-08-27), amended 2026-08-27 (decision 4)
- **Date:** 2026-08-27
- **Relates to:** #201 (transform fork), #205 (edge fork), #135 (worker HPAs),
  #19 (tenant NATS placement), `docs/scalability.md` ceiling 2

## Context

VRSky carried two runtimes for the same pipeline nodes, and only one of them
worked.

**The runtime that works.** Every node type is served by a *standing service*:
one Deployment per connector kind (`file-consumer`, `http-producer`,
`cloud-storage-producer`, `sitoo-consumer`, …) plus the shared `data-filter` and
`data-converter`. Each subscribes to a JetStream pull durable on `vrsky.data.*`,
claims a message by looking up the connection's nodes in the `connections` table
and matching the node's config `type`, and is activated per connection by the
`vrsky.commands.{tenant}.connection.start` command the management API publishes.
This is what moved 1 GiB through prod on 2026-08-26.

**The runtime that never worked.** On connection start, the orchestrator also
created a Deployment + HPA per node, running generic worker images
(`vrsky-consumer`, `vrsky-producer`, and before #201 `vrsky-filter` /
`vrsky-converter`). These were wired to `{tenant}.pipeline-{conn}.{node}.output`
subjects that nothing publishes to or subscribes from — SDK connectors speak
`vrsky.data.*` and ignore `INPUT_NATS_SUBJECT` / `OUTPUT_NATS_SUBJECT` entirely.

The edge workers were worse than inert. Both read an `input_type` /
`output_type` key that the UI never writes (it writes `type`), so:

- `cmd/consumer` always fell through to its `"http"` default and sat on a bare
  HTTP listener, publishing to a dead subject;
- `cmd/producer` fell through to `"file"`, which `pkg/io.NewOutput` does not
  implement — `os.Exit(1)`, i.e. a crash loop for the lifetime of the connection.

#201 stopped spawning the transform half. This ADR covers the edges, and with
them the last of the fork.

## Decision

**1. The orchestrator deploys no per-connection workers at all.**
`CreateAllDeploymentSpecs` skips every node type. Starting a connection now
means: validate the DAG, mark it running, publish the start command. Connection
count costs rows, not pods.

`StartConnection` additionally *prunes* per-connection Deployments and HPAs that
its spec set no longer contains, so connections created before this change shed
their leftover pods on the next start rather than needing an operator sweep.

**2. Every UI-selectable node type gets a standing service in prod.**
`deploy-connectors-azure.sh` grew from the 10 retail connectors to the full set
that the source and destination dropdowns in `PropertyEditor.tsx` offer. A node
type with no service in that table is a type the platform silently cannot run —
that is the failure #205 was, so the script's service table and the UI dropdowns
must be kept in step.

That invariant is now enforced rather than merely stated. Two tests in
`pkg/managementapi/nodeconfig_test.go` pin the chain UI → validator → deployment:
`TestNodeConfigRulesCoverUI` fails when the UI offers a type the validator does
not know, and `TestNodeConfigRulesAreDeployed` fails when a type the validator
accepts has no service in the deploy script (or a service is deployed that no
type maps to). Deliberate gaps live in `deployExceptions` with their reason; the map is
currently empty, which is the state to keep it in — every type the UI offers has
a service that runs it.

**3. Replicas are 1 unless the connector is a pure pull-durable subscriber.**
Pull durables distribute work across replicas with no coordination, so producers
with no local state run 2 + a PDB. Singletons remain where a second replica
would be wrong rather than merely unnecessary: consumers drive their own
ingestion (directory watches, API polls, CDC cursors) and would double-ingest;
producers with an HTTP surface hold the UI's SSE event buffer per pod and would
show an operator half the events.

**4. The Deployment-building machinery is deleted.**
It was initially retained, on the argument that dedicated pods for a noisy or
isolation-sensitive connection is a plausible future feature and this code would
be its starting point. That was reversed immediately: unreachable code with
passing tests is precisely how the fork in this ADR survived unnoticed for
months, and a reviewer cannot tell which half of `pkg/orchestrator` is live. The
future feature, if it arrives, will want the *standing* services' claim model
anyway — not the topic-wired one this code encodes.

Deleted: `CreateDeploymentSpec` / `CreateAllDeploymentSpecs` and every builder
below them, `deployComponent` / `applyHPA`, the whole `nats.go` topic scheme
(`GetOutputTopic` and friends — the dead subjects themselves), `GetContainerImage`,
`DeploymentSpec` / `NodeScaling` / `TopicPair`, the pod resource and env-name
constants, and the `ImageRegistry` / `ImageVersion` / `PayloadStore*` / autoscaling
fields on `OrchestratorConfig` along with the management-API env that fed them
(`WORKER_IMAGE_*`, `PAYLOAD_STORE_*` — the standing services carry their own).
`GetPipelineStatus` went too, from the adapter and from
`managementapi.PipelineOrchestrator`: it derived per-node status from the worker
Deployments, had no production caller, and would now always answer `{}`.

What survives is the part with a live purpose: graph validation, the label and
name helpers, connection teardown, and the orphan GC.

One piece of behaviour had to be rescued rather than deleted. `CreateDeploymentSpec`
called `IsValidNodeType`, and once the builder stopped deploying, that check was
the *only* thing rejecting an unrecognised node type — a type with no standing
service would otherwise start "successfully" and silently do nothing. It now lives
in `BuildExecutionGraph`, which is a better home: validation happens as the graph
is built, before the handler marks the connection running.

## Consequences

**Generic destinations work.** Before this, a prod pipeline ending in an HTTP,
database, file, cloud-storage, SFTP, Kafka, RabbitMQ or Salesforce node had
nothing running that could deliver it. This is what makes the ADR 0003 input
formats useful end to end: "CSV file in → converter → HTTP endpoint out" is now
a deliverable pipeline rather than a half of one.

**Scalability ceiling 2 is gone, not deferred.** `docs/scalability.md` named
pod-per-connection orchestration as the second ceiling and proposed a "shared
multi-tenant worker-pool mode" as a large piece of future work. The platform was
already that; the pods were the vestige. Deleting them removes the ceiling.

**Ceiling 1 got shallower.** DB pool count now scales with the number of
standing services (a few dozen, fixed) instead of with connection count, so
`max_connections` exhaustion is no longer a fan-out risk — just ordinary
single-instance load.

**Connector memory limits were wrong and are fixed here.** Connectors were
capped at 128Mi while the claim-check rehydrate cap
(`PAYLOAD_REHYDRATE_MAX_BYTES`) defaults to 128 MiB. A producer that does not
implement the ADR 0001 streaming path would be OOM-killed before the cap could
reject anything. Limits are now 512Mi, matching what ADR 0001 assumed.

**Tenant NATS placement (#19) is now visibly inert.** It resolved a per-tenant
NATS URL and stamped it on the worker pods — the only consumer it ever had.
Standing services dial the `NATS_URL` in their own env, so a connection placed
on a tenant instance is not actually served from it. The resolution logic and
its tests are retained; wiring placement through to the standing services is
separate work.

**Retired:** every per-connection worker binary and the code that existed only to
serve them.

- Edges: `cmd/consumer`, `cmd/producer`, their images and build entries, the
  `workers` image group, and the demo scripts that drove them
  (`test-pipeline.sh`, `test-pipeline-interactive.sh`, `scripts/e2e-test.sh`).
- Transforms: `cmd/filter` and `cmd/converter`, which #201 left in place because
  a compose service still built them, along with `pkg/filter` (8,956 lines) and
  `pkg/converter` (11,937 lines) — the libraries behind them, imported by nothing
  else — and `pkg/runtime`, the package that read the worker env contract
  (`INPUT_NATS_SUBJECT` and friends) this ADR deleted the producer of.
- Tests: the orchestrator's `integration`-tagged tests and
  `test/integration/checkpoint_e2e_test.go`. All asserted per-connection pods and
  message flow over the dead subjects, needed a cluster no CI job provisions, and
  had never run.

**The legacy converter was richer than the live one, on paper.** `pkg/converter`
carried a function registry (string/numeric/date/ID helpers), HTTP and Postgres
lookups, WASM plugin functions, JSON-schema validation and a rule engine;
`pkg/filter` carried gating, routing, rate limiting and rejection queues. The
live `data-converter` implements field mappings with `{field}` string
interpolation, and `data-filter` implements rule-based row filtering. Deleting
the legacy packages removes **no working functionality** — none of it ever ran in
prod, and the config shapes it read were not the ones the UI writes — but it does
discard a reference implementation. If any of those capabilities is wanted, it
should be specified against the live transforms and their config, not restored;
git history has the old code.

## Alternatives considered

**Fix the generic workers instead.** Teach `cmd/consumer`/`cmd/producer` to read
`type`, publish on `vrsky.data.*`, and speak JetStream. This rebuilds, per node
type, what the SDK connectors already do — including claim-check, streaming
(ADR 0001), and connection-scoped dispatch (#203) — and leaves two
implementations of every connector to keep in sync. The fork is the bug; a
second correct implementation is still a fork.

**Keep spawning workers for isolation.** Real per-connection isolation has value
for a noisy or regulated tenant. But it should be an opt-in on top of a runtime
that works, not the default path for every connection — and it was never
delivering that isolation, because the pods did no work.

**Keep the machinery in place for that future feature** (the original decision 4,
reversed the same day — see above). Retaining dead code to seed a hypothetical
feature is what let this fork persist; git history preserves it just as well, and
without the ambiguity about what runs.
