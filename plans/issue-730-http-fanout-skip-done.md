# HTTP fan-out: don't resend to nodes that already took the message (#730)

Backlog item 159. No open questions.

## Goal

`http-producer`'s `Deliver` sends one message to every matching HTTP node of
a connection and acks only when none of them failed retryably. When one node
fails (certain for a refused private URL since #660) and a sibling succeeds,
the message is NAK'd, and each of the five attempts sends it to the healthy
sibling again. The DLQ copy then holds only the first failing node's error,
and a retry from the DLQ sends it to the healthy sibling once more.

## Acceptance criteria

- Two nodes, one refused: the healthy node receives the message exactly once
  across all five attempts, and the message lands in the DLQ.
- The DLQ copy's `X-Vrsky-Last-Error` names every failing node (by node id),
  not only the first.
- Both nodes healthy: one request each, no DLQ.
- A retry from the DLQ (same envelope) handled by the same process does not
  send to the healthy node again.
- The skip memory is bounded (entry cap) and entries expire (TTL at least the
  redelivery window plus the DLQ retention, so a DLQ retry is covered).
- The skip is per process, documented, with #727 linked as the cross-process
  version for the remote agent.
- Tests on the embedded NATS harness that fail on main; mutation run on the
  skip key.
- Docs: `docs/connectors/http.md`, the HTTP row in
  `docs/operator/troubleshooting.md`; backlog item 159 struck with `(#730)`.

## Decisions

(a) In-process skip set, the remote agent's design (#587,
`src/cmd/remote-agent/written.go`) and the one #705 uses for db-producer.
A local copy in `src/cmd/http-producer/sent.go`, not a shared package: #705
has its own copy in flight and I must not touch `src/cmd/db-producer`.
Consolidating the three copies is a follow-up (noted in the backlog).

(b) Key: connection id, envelope id, node id, method and URL. The SDK hands
`Deliver` the envelope, not the stream sequence, so the envelope id is the
message's identity (consumers and the SDK republish give every message a
fresh UUID; a DLQ retry republishes the same envelope, which is what we want
skipped). Method and URL are part of the key so a node edited to point
somewhere else between attempts is a new target and gets the message. The
URL is kept in memory only, never logged.

(c) What counts as "took the message": any node whose send returned nil,
that is a `2xx`, or a `4xx` / unbuildable request (already acked by design,
retrying can't help). Those are not sent again on redelivery.

(d) Lifecycle: entries are added only when a delivery fails on some node and
at least one other node took it; the delivery that completes the message
(no failures left) removes them. TTL 7 days (`messaging.DLQRetention`; an
SDK connector can't import `pkg/messaging`, `make lint-connector`), cap
10 000 entries. Forgetting early costs a duplicate, never a missed send.

(e) Errors of all failing nodes are joined, each prefixed with `node <id>:`.
That joined text is what the SDK writes into `X-Vrsky-Last-Error`. The
messages already use the redacted URL.

(f) `DeliverStream` is unchanged: it declines fan-out (more than one eligible
node), so it never has a sibling to skip.

(g) Test hook: the dispatch backoff can only be shortened from inside
`pkg/sdk` today (`export_test.go`), so a harness test of five attempts would
take about 2.5 minutes. Add `sdk.WithDispatchBackoff` (a `RunOption`, like
`WithDB`) and `harness.Options.Backoff`. Additive and backwards compatible:
production passes no option and keeps `messaging.DefaultBackoff`.

## Residual

At-least-once per node across processes: a redelivery handled by another
pod (restart, rolling deploy, a second replica) does not see the set and
sends to every node again. A lost `2xx` (timeout after the partner accepted)
is sent again too. The cross-process version is #727 (remote agent; the same
design would cover this producer).

## Affected files

- `src/cmd/http-producer/main.go`: node id on `HTTPConfig`, skip in `Deliver`,
  joined errors.
- `src/cmd/http-producer/sent.go` (new): the skip set.
- `src/cmd/http-producer/fanout_test.go`, `sent_test.go` (new).
- `src/pkg/sdk/lifecycle.go`, `src/pkg/sdk/base.go`,
  `src/pkg/sdk/harness/harness.go`: the backoff option.
- `docs/connectors/http.md`, `docs/operator/troubleshooting.md`,
  `plans/backlog.md`.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1 ./cmd/http-producer/...
./pkg/sdk/...`; from the root `make lint-connector`, `make lint-tenant`. The
new tests run against main's `Deliver` first to show they fail, then a
mutation run on the skip key and the forget.
