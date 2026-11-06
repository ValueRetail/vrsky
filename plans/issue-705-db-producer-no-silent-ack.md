# db-producer: retry or dead-letter what it cannot write (#705)

Also covers backlog item 183 (the database page and the stale DLQ parts of
`docs/NATS_ARCHITECTURE.md`).

No open questions.

## Goal

`db-producer` acks messages it did not write. `getTargetConnections`
`continue`s past a target it cannot open or ping; when no target is left,
`Deliver` logs at debug and acks. `insertRow` errors are logged and skipped
and the message is acked. A node with no `host` is skipped the same way. All
of these lose data without a DLQ entry.

## Acceptance criteria

- A target that cannot be opened or pinged makes `Deliver` return a
  retryable error (NAK, then the DLQ after five attempts).
- With several targets, a redelivery does not write rows again to a target
  that already took the message.
- A row the database refuses fails the delivery with the database's error
  (retryable, so it ends in the DLQ with that error in
  `X-Vrsky-Last-Error`), and none of the message's rows stay in that
  target. A connection-level error (database gone, connection reset) is
  retryable too.
- A node with no `host` (or a node config that does not parse, or a table
  name that is not `table` or `schema.table`) is logged at Error and
  dead-lettered, not skipped or dropped.
- A non-JSON payload is still `sdk.Permanent` (dropped).
- `ServesConnection` and the #704 config-read behaviour are unchanged.
- Tests against a real Postgres (`pkg/testdb`) that fail on main, and a
  mutation run on the decisions below.
- Docs: `docs/connectors/database.md` (source and destination config as the
  code reads it), `docs/sdk/migration.md`, the DLQ parts of
  `docs/NATS_ARCHITECTURE.md`, the database row in
  `docs/operator/troubleshooting.md`; backlog items 179 and 183 struck.

## Decisions

(a) Open or ping failure. `getTargetConnections` no longer opens anything;
it reads and caches the node configs. Each target opens its pool on first
use (open plus ping, under a per-target mutex), and a failure there is an
error for that target in this delivery. Nothing about the failure is
cached, so the next delivery tries again. Before, a target that failed its
ping was left out of the cached list for the 5-minute TTL as well.

(b) Several targets. In-process skip, like the remote agent (#587): a
`writtenSet` remembers (connection, envelope id, node id, host, port,
database, table) for each target that committed a message whose delivery
then failed on another target. The redelivery skips those targets. The
entry is removed when the message finally succeeds; otherwise it expires
after `messaging.DLQRetention` (so a DLQ retry from the same process also
skips them) and the set is capped at 10 000 entries. Forgetting early costs
a second write, never a missed one.

Idempotent writes were the other option. They need a key the destination
table does not have: in `insert` mode the table is the customer's, and
adding a column or a unique index to it is not ours to do. The rows of one
target are now written in one transaction, so a target has either all of a
message's rows or none, and the skip set only has to work per target.

The residual is at-least-once per target: a redelivery handled by another
process (restart, rolling deploy) does not see the set and writes again to
the targets that had committed, and a commit whose acknowledgement is lost
is written again. Documented on the database page.

The envelope id is the message's identity here (the SDK hands `Deliver` the
envelope, not the stream sequence). Consumers and the SDK's republish give
every message a fresh UUID, so two different messages do not share one.

(c) A refused row. The rows of one target go in one transaction. Any row
error rolls the transaction back and fails the delivery with
`row N of M refused by <table>: <database error>`. That is retryable: five
attempts, then the DLQ, where the operator sees the error and can retry once
the table or the data is fixed. Not `sdk.Permanent`, because the DLQ keeps
the message and a drop would not, and a constraint can change (a dropped
check, a widened column). A connection-level error takes the same path; the
SDK cannot tell them apart and does not need to.

(d) A node with no `host` is a config error: the target carries it, the
delivery logs it at Error with the node id and fails retryable, so it
reaches the DLQ. A node config that does not parse and a bad table name go
the same way. The bad table name used to be `sdk.Permanent`; with several
targets that dropped the message for the healthy targets too, and a DLQ
entry can be retried once the node is fixed.

Errors on several targets are joined, each prefixed with its node id.

## Review round 1

- The connection string quoted nothing (`password=%s`), so a password with
  a space broke lib/pq's parser, whose error quotes the part after the
  space; since this change that error reaches the DLQ header, the feed and
  the log. `pkg/pgdsn` builds the string with every value quoted; used by
  db-producer (delivery and Test Connection) and db-consumer (source,
  test-connection, sample-data, schema).
- Connecting gives up after 10 s (`connect_timeout`), so a target that
  drops packets doesn't hold the worker for the OS timeout on every attempt.
- `TargetConnection.close` marks the target closed; `pool` then returns a
  retryable error instead of opening a pool on a list that was evicted or
  replaced, which nothing would close.
- `user` is part of the skip key, next to host, port, database and table.

## Affected files

- `src/cmd/db-producer/main.go`: lazy per-target pools, config errors on the
  target, one parse of the payload, per-target transaction, error
  collection, the skip set.
- `src/cmd/db-producer/written.go` (new): the skip set.
- `src/pkg/pgdsn` (new) and `src/cmd/db-consumer/{service,server}.go`: the
  quoted connection string (review round 1).
- `src/cmd/db-producer/*_test.go`: real-Postgres tests, mock tests updated
  for the transaction.
- Docs listed above, `plans/backlog.md`.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1 ./cmd/db-producer/...
./pkg/sdk/...` with `VRSKY_TEST_POSTGRES_URL` on a throwaway container;
from the root `make lint-connector` and `make lint-tenant`. The new tests
run on main first to show they fail there, then mutations of (a)–(d).
