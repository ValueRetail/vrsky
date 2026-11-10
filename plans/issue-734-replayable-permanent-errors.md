# #734: which producer errors are kept for replay

## Goal
A destination drops a message (acks it, keeps nothing) when it thinks a retry
cannot help: a `4xx`, a node with a setting missing, a payload that is not
JSON. For errors that come from the node's own config or credential that is
wrong: once the operator fixes the node, the same message would go through,
but it is gone. This change keeps those messages (retried, then the DLQ) and
leaves the drops that are about the payload itself.

Ludvik decided on 2026-11-03 (issue comment): a `401`/`403` from the target is
retried and lands in the DLQ; every other `4xx` stays dropped. The rest of the
table below is my proposal; the PR carries `agent:needs-human` for it.

## Scope
The six destinations the issue names: `src/cmd/{http,kafka,rabbitmq,sftp,cloud-storage,salesforce}-producer`.
Docs: `docs/operator/troubleshooting.md` (the table) and the connector pages
`docs/connectors/{http,kafka,rabbitmq,sftp,cloud-storage,salesforce}.md`.
Not touched: `pkg/sdk`, `pkg/messaging` (PR #810 reworks the retry code),
`pkg/managementapi`, `pkg/safeurl`, consumers, and the other six destinations
(see "Left out").

## Per case

| Destination | Case | Before | After | Why |
|---|---|---|---|---|
| HTTP | `401`, `403` (also the second `401` after an OAuth refresh) | dropped | retried, then DLQ | Ludvik's decision; a fixed credential lets a DLQ retry through |
| HTTP | any other answer below `500` except `429` (`400`, `404`, `422`, ...) | dropped | dropped | Ludvik's decision: other `4xx` stay dropped |
| HTTP | URL a request cannot be built from | dropped | retried, then DLQ | node config, same as a refused URL (#660) |
| HTTP | node with no `url` | acked as "not ours" (the node was skipped while loading config) | retried, then DLQ (refused as not an absolute URL) | node config |
| HTTP | `tls` block whose cert or key does not parse | acked as "not ours" (the config load failed as a whole) | that node retried, then DLQ; nothing is sent to it; siblings still get the message | node config; sending without the certificate is not an option |
| Kafka | no `brokers` or `topic` | dropped | retried, then DLQ | node config |
| RabbitMQ | no `url`, or neither `exchange` nor queue | dropped | retried, then DLQ | node config |
| SFTP | no `host`, `username` or `remote_dir`; no password or key | dropped | retried, then DLQ | node config / credential |
| SFTP | `filename_template` that cannot be parsed or rendered | dropped | retried, then DLQ | the template is node config; fixing it makes the message work (a field the payload lacks included) |
| Cloud storage | no `bucket` | dropped | retried, then DLQ | node config |
| Cloud storage | key template fails and the generated fallback key fails too | dropped | dropped | the fallback only fails when the payload's own `uuid` field cleans away to nothing (`..`), so it is about the payload; logged at Error |
| Salesforce | no `object` | acked as "not ours" (the node was skipped while loading config; the `object` check in `writeRecords` never ran) | retried, then DLQ | node config |
| Salesforce | `upsert` with no `external_id_field` | retried, then DLQ (already, the doc said dropped) | unchanged | doc fixed |
| Salesforce | payload not JSON | `sdk.Permanent` | unchanged | about the payload; the SDK logs `permanent error; dropping message` |

## Decisions made without asking
- "Dead-letter now" does not exist. The SDK has nil (ack), `Permanent` (ack +
  log) and everything else (NAK, backoff, DLQ after `MaxDeliveryAttempts` =
  5). A first-attempt DLQ needs a new error kind in `pkg/sdk` and a branch in
  `pkg/messaging`'s subscriber, both reworked by #810 while this was written
  (it merged during this work; the branch is rebased on it). So config errors
  use the ordinary retry: five attempts over about 2.5 minutes, then the DLQ.
  That is bounded (no loop), costs nothing outside the pod (each attempt fails
  before any connection is opened, except `401`/`403`, which is one request
  per attempt) and is the pattern the URL checks already use (#661, #677,
  #678, #686). A `sdk.DeadLetter(err)` kind is backlog item 201.
- `404` on a configured path is a config error too, but Ludvik ruled that
  every `4xx` other than `401`/`403` stays dropped. Left as he decided; noted
  in the PR as the one case worth a second look.
- No drop counter. The payload drops that stay (`sdk.Permanent`) are logged by
  the SDK at Warn. A `vrsky_messages_dropped_total` counter belongs in the SDK
  runner next to `IsPermanent`, which is `pkg/sdk`: backlog item 202. The HTTP
  `4xx` drop stays a per-node nil (not `Permanent`), because in a fan-out a
  `4xx` node has "taken" the message and must not be resent; it keeps its
  Error log line and feed event.
- The error texts say what to fix and that the message can be replayed from
  the DLQ, like Salesforce's `external_id_field` error.
- A misconfigured node in a connection with several nodes of the same
  destination type now fails the whole delivery, so for Kafka, RabbitMQ, SFTP
  and cloud storage the healthy sibling node gets the message again on each
  attempt (they have no skip set like HTTP's `sent.go` or Salesforce's
  `written.go`). That is how a down target already behaves there. HTTP and
  Salesforce skip the nodes that took it. Noted in the troubleshooting page.
  The reviewer suggested the other way: check every eligible node's settings
  before writing to any, so a broken node fails the message with no side
  effects and the replay after the fix delivers once to each node. The cost
  is that the healthy node gets nothing until the fix. I kept the current
  behaviour (the healthy target keeps receiving, with repeats) and put the
  choice to Ludvik in the PR; it is a small change either way.
- The config cache: every one of these destinations except cloud storage
  keeps a connection's config for 5 minutes without eviction, so a DLQ retry
  right after the fix can meet the old node. The docs say to wait 5 minutes.
  Evicting on the connection start command (as cloud storage does since #141)
  is backlog item 203.

## Left out
Sitoo, Front Systems, Visma, Brightpearl, Business Central and SAP mark a
`401`/`403` `sdk.Permanent` too. Ludvik's rule applies to them, but the brief
for this PR is the six destinations the issue names. Backlog item 204.
Refusing to start a connection whose Kafka, RabbitMQ, SFTP or cloud-storage
node lacks a required field (`pkg/managementapi`) is backlog item 205.

## Acceptance criteria
- Each case marked "retried, then DLQ" ends in the DLQ on the embedded NATS
  harness (five attempts with a fast backoff), with an error naming the
  missing setting or the status.
- The cases that stay dropped are acked with no DLQ copy (HTTP `400`, `404`;
  Salesforce non-JSON).
- HTTP fan-out: a `401` node is retried and not skipped, a healthy sibling is
  sent the message once.
- Tests fail with the old code (mutation: restore `return nil`).
- `docs/operator/troubleshooting.md` table and the connector pages match.
- Backlog item 177 struck with #734.

## Verification
From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run`,
`go test -race -count=1` on the six packages, `make lint-tenant`,
`make lint-connector`.
