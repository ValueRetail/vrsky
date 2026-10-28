# An ending poller can drop a newer poller's map entry (#408)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A poller that ends on its own (one-shot done, or a setup error) removes its
connection from the consumer's active map only while the entry there is
still its own. After a stop and a new start, the old poller ending leaves
the new poller's entry alone, so a later start is still refused.

## Acceptance criteria (from the issue)

1. Each of the four consumers deletes only its own entry, compared by
   pointer under the lock.
2. Deterministic tests per consumer: old poller ends after stop plus new
   start; the new entry survives and a later start is refused. Mutation:
   put back the unconditional delete and the tests fail.
3. `go test -race` passes for the four packages and `./...`;
   `make lint-tenant` passes.
4. Backlog item 52 struck with `(#408)`.

## Where the unconditional delete is today

| Consumer | Ending path | Delete |
|---|---|---|
| api | `pollConnection` one-shot (`OneTimeOnly`) | `src/cmd/api-consumer/api_poller.go:42` |
| sftp | `runPoller` one-shot (`poll_interval_seconds <= 0`) | `src/cmd/sftp-consumer/service.go:266` |
| cloud-storage | `finishConn`, called from `runPoller` (open store failed, one-shot done) and `runEventLoop` (open store or event source failed) | `src/cmd/cloud-storage-consumer/service.go:472` |
| tenant | `runBridge` error paths: no data connection, source not shared, JetStream context, subscribe failed | `src/cmd/tenant-consumer/bridge.go:42, 62, 87, 109` |

Each does `s.mu.Lock(); delete(map, connID); s.mu.Unlock()`. The sequence
that breaks it: start A, stop (cancels A, deletes A's entry), start B
(inserts B's entry), then A finishes and deletes B's entry. The next start
finds nothing active and starts a third poller beside B.

## Approach

The maps hold `context.CancelFunc` values, and Go funcs can't be compared.
In each of the four packages:

- A small entry type, `activeConn{cancel context.CancelFunc}`; the map
  becomes `map[string]*activeConn`. The start creates the entry in
  `claim.Activate` and hands the same pointer to the poller goroutine.
- A helper `dropActive(connID, own *activeConn)` deletes under `s.mu` only
  when `map[connID] == own`. Every ending path above calls it instead of
  `delete`.
- Stop, stop command and `Stop()` read `.cancel` from the entry. Nothing
  else changes: same lock, same status writes, same logs.

The existing `start_race_test.go` fixtures build the map with the old type;
they change to `make(map[string]*activeConn)`, nothing else.

## Tests

Written by a separate test author from the acceptance criteria. Per
consumer, deterministic (no sleeps as synchronisation): an old poller's
ending path runs after a stop and a new start; then the new entry is still
in the map (same pointer) and a further `startConnection` is refused (no
second DB lookup, no second poller). Mutation run: replace the compare in
`dropActive` with an unconditional delete in each package and check the
new tests fail.

## Decisions

- Pointer to a per-start entry rather than a token counter: the issue
  allows either, and a pointer needs no extra state.
- One helper per package, not an SDK helper: the four maps differ in name
  and the SDK's `StartGuard` deliberately leaves the active map to the
  consumer.
- Out of scope, filed as backlog item 58: the same old poller still writes
  status `stopped` or `error` for the connection after the new start
  wrote `running`, so the UI can show a running connection as stopped.

## Risks

- A missed call site would keep the bug in one path. `grep delete(` on the
  four maps after the change confirms only `handleStopCommand` and
  `dropActive` delete.

## Verify

From `src/`: `go test -race -count=10` on the four packages,
`go test -race ./...`, `go vet ./...`, `golangci-lint run ./...`,
`make lint-tenant`, plus the mutation run above.
