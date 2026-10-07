# Stable connections: no silent new id, and connectors that resume on boot

## What actually happened (2026-10-07, from the audit log)

The builder already keeps the connection id on redeploy: stop → `PUT` the
same id → start. It did so three times today for the webhook pipeline
(`ac3d291b`, at 09:22, 10:12, 10:21). At 12:00:48 the connection was
**deleted** (`DELETE /api/v1/connections/ac3d291b…` → 204), and two seconds
later the deploy found stop and update answering 404 and fell back to
*creating* a new connection — `6785db7a`, a new webhook URL — without a
word. The BC pipeline's trail of ids over the week (`4f23df67` → `df0f112d`
→ `2c0ec8ff` → `69d1bd1e`) is the same fallback.

So two things, not one:

1. **The builder must never change the id quietly.** When the connection it
   deployed is gone, say so and ask; a webhook URL is a contract with the
   sender.
2. **The reason anyone redeploys at all** is that the standing consumers do
   not resume running pipelines when they restart — every connector rollout
   (or crash, or node move) stops every pipeline on it until someone
   redeploys from the builder, and a webhook source answers 404 meanwhile.
   The remote-agent gateway already restores from the database on boot;
   the other sixteen consumers should too.

## Open questions

1. **When the deployed connection is gone**, recommended: **stop and ask**
   — "This pipeline's connection was deleted on the server. Deploy as a new
   pipeline? Its webhook URL will change." Cancel leaves everything as it
   is. Alternative: create and warn afterwards; that is what happens today,
   minus the silence, and a till would still be posting to a dead URL.
2. **Which consumers resume on boot.** Recommended: **all sixteen** that
   handle start commands, in one PR — the change is the same fifteen lines
   in each, and a repo-level test keeps the next connector honest. The
   three that matter in prod today (webhook, Business Central, file) get
   real harness tests; the rest are covered by that guard plus the shared
   helper's own test. Alternative: those three only, now.
3. **A running pipeline the connector cannot bring back** (its config no
   longer resolves, a secret gone): today a failed start command is logged
   and nothing else. Recommended: unchanged — log, leave `running`, the
   operator sees it in the connector log and the pipeline panel. Flipping
   the row to `error` from inside a connector is a bigger change.

## Goal

A connection id — and with it a webhook URL — changes only when a human
deletes the connection, and then the builder says so before making another.
A connector restart brings every running pipeline back by itself; after a
rollout nobody redeploys anything, and a webhook source is reachable again
the moment its consumer is up.

## What exists (checked 2026-10-07)

- `PipelineBuilder.deployPipeline`: `prevConnectionId` from the canvas →
  `POST …/stop` (error ignored) → `PUT …` → on *any* error, `POST
  /connections` and relink. The catch is what turns a deleted connection,
  or a `PUT` refused because the stop failed, into a new id.
- The canvas ↔ connection link lives in `localStorage` per browser; opening
  `/connections/{id}/edit` elsewhere imports the graph and links it, so the
  id survives there too. The builder has no tests.
- `UpdateConnection` refuses a running connection (400); `DeleteConnection`
  auto-stops first. Nothing server-side was wrong.
- Start/stop are NATS commands (`vrsky.commands.<tenant>.connection.start`)
  published by the API and not persisted. Sixteen consumers subscribe and
  keep an in-memory map of active connections; every one guards against a
  double start (`already registered`). All have `db`. Only `remote-agent`
  has `restoreRunning` (`SELECT id, tenant_id FROM connections WHERE status
  = 'running' AND nodes::text LIKE '%"remote_agent"%'` → its own start
  path).
- Webhook consumer after today's restart: 404 for the running pipeline
  until the user redeployed — which, because of the delete, became a new id.

## Approach

### 1. Builder — the fallback becomes a question

Extract the deploy sequence from `PipelineBuilder.tsx` into
`services/deployPipeline.ts` (pure: takes the API client and the previous
id, returns `{ connectionId, replaced: boolean }` or throws), so it can be
tested. Behaviour:

- Previous id and `stop` + `PUT` succeed → same id (today).
- `stop` or `PUT` answers **404** → the connection is gone: return a
  `ConnectionGone` result. The page shows a confirm dialog (the existing
  `showConfirmDialog`): *Deploy as a new pipeline?* with the webhook-URL
  warning when the source is a webhook. Confirm → create + relink + start;
  cancel → nothing deployed, canvas link cleared so the next Deploy is an
  honest first deploy.
- `PUT` fails for any other reason (400 "cannot update a running
  connection" because the stop failed, 403, 5xx) → an error toast with the
  server's message; **no create**.

### 2. SDK — `sdk.RestoreRunning`

```go
// RestoreRunning calls start for every connection the database says is
// running, so a connector brings its pipelines back after a restart without
// a redeploy. typeHint narrows the scan to rows whose graph mentions the
// node type; start itself still decides (it is the same function the start
// command runs, with its own "not mine" and "already active" checks).
func RestoreRunning(ctx context.Context, db *sql.DB, logger *slog.Logger, typeHint string,
    start func(ctx context.Context, connectionID, tenantID string)) (n int)
```

Query: `SELECT id::text, tenant_id FROM connections WHERE status = 'running'
AND nodes::text LIKE '%"' || $1 || '"%'` (`lint:tenant-ok`, fleet-wide
boot scan; each row's tenant scopes the start). Logs one line with the
count. Idempotent by construction: start is the same path the command uses.

### 3. Every consumer — fifteen lines each

In each of the sixteen: split `handleStartCommand(msg)` into the parse and
`startConnection(ctx, connID, tenantID)`; in `Run`, after the command
subscriptions are in place (so nothing published during the scan is
missed), call `sdk.RestoreRunning(ctx, s.db, s.logger, "<type>",
s.startConnection)`. `remote-agent` switches its own `restoreRunning` to
the helper so there is one implementation. Consumers: api, brightpearl,
business-central, cloud-storage, db, file, front-systems, kafka, rabbitmq,
salesforce, sap-s4hana, sftp, sitoo, tenant, visma, webhook.

Two replicas of a consumer both restore, exactly as both receive a start
command today — no change in semantics.

### 4. Docs and memory

- `docs/connectors/http.md`: the webhook URL is stable across Stop/Start,
  Deploy and connector restarts; only deleting the connection changes it.
- `docs/adr/0004-standing-connector-services.md`: a note that standing
  consumers restore running pipelines on boot (closing the gap the ADR
  left). `docs/operator/troubleshooting.md`: "after a connector restart"
  entry rewritten — nothing to redeploy. The deploy memory note that says
  "redeploy after every connector restart" goes.

## Files

| File | Change |
|---|---|
| `ui/src/services/deployPipeline.ts` (+test) | the deploy sequence, `ConnectionGone` |
| `ui/src/pages/PipelineBuilder.tsx` | calls it; confirm dialog; clears the link on cancel |
| `src/pkg/sdk/restore.go` (+test, real Postgres via `pkg/testdb`) | `RestoreRunning` |
| `src/cmd/<16 consumers>/service.go` | `startConnection` split + the call in `Run` |
| `src/cmd/{webhook,business-central,file}-consumer/*_test.go` | boot-restore harness tests |
| `src/pkg/managementapi/nodeconfig_test.go` | `TestConsumersRestoreRunningOnBoot` (source guard) |
| `docs/connectors/http.md`, `docs/adr/0004-…`, `docs/operator/troubleshooting.md` | as above |

Not changed: the API, the command subjects, the orchestrator, producers
(they consume the shared stream and need no start).

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `deployPipeline.test.ts`: same id on success; `ConnectionGone` on 404 from stop or PUT; other PUT failures throw and never create; first deploy creates | the builder's contract | fall back to create on any error |
| `TestRestoreRunning` (Postgres) | only `running` rows; only those whose graph has the type; start called once per row with its own tenant; count returned | drop the status or the type filter |
| `TestWebhookConsumer_RestoresOnBoot` | a running webhook row in the database at boot → `/webhook/{id}` answers 202 with **no** start command ever published; a stopped row does not | remove the `RestoreRunning` call |
| `TestBCConsumer_RestoresOnBoot`, `TestFileConsumer_RestoresOnBoot` | same for a poller and a watcher | — |
| `TestConsumersRestoreRunningOnBoot` | every `cmd/*` that subscribes to `connection.start` calls `sdk.RestoreRunning` | delete the call in any one |
| Existing harness tests | a start command still works, and a start for an already-restored connection is a no-op | — |

Then `gofmt`, `go vet`, `golangci-lint`, `lint-tenant`, `go test -race ./...`
with the database; UI `tsc`, coverage gate.

## Verify in prod (after deploy, on your word)

1. `kubectl rollout restart deploy/vrsky-webhook-consumer` → within seconds
   `POST /webhook/6785db7a…` unsigned answers **401** (registered), with no
   redeploy; the log says `Restored running pipelines count=1`.
2. Same for `vrsky-business-central-consumer` → the BC pipeline's next poll
   appears in its log without a redeploy.
3. In the builder: Delete a throwaway pipeline from the list, then Deploy
   its canvas → the dialog appears; Cancel → nothing created; Confirm → a
   new id, stated.

## Rollout

`build-push-acr.sh core` + `connectors`, `deploy-core-azure.sh ui`, then
`deploy-connectors-azure.sh` (restarts the fleet). **This is the last time a
fleet restart needs a redeploy afterwards**: the old images stop the
pipelines; the new ones, coming up, restore them. Order: deploy connectors,
then — once only — redeploy the two running pipelines from the builder
(or Stop/Start them) so the new images pick them up; after that, restarts
are self-healing. Alternatively restart the three in-use consumers first and
verify step 1 before rolling the rest.

## Risks

- **A restore storm on a fleet rollout**: sixteen consumers each scan
  `connections` once at boot and read the rows that mention their type.
  Two pipelines today; negligible at any plausible size.
- **A pipeline marked `running` that cannot start** (question 3) stays
  `running` in the UI while its connector logs the failure — as a failed
  start command does today.
- **The webhook 404 window** shrinks from "until someone redeploys" to the
  consumer's boot time (seconds); the till's outbox retries cover it.
- **Delete still changes the URL** — by design; the dialog makes it a
  choice rather than an accident.

## Non-goals

A webhook URL independent of the connection id (a slug); persisting start
commands; marking rows `error` from connectors; producers (no start
needed); the alert responder's "redeploy" action.
