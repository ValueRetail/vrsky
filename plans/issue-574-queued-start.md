# A queued Front Systems start runs when the row has moved on (#574)

Open questions: none. Calls made without asking are under "Decisions".

## Problem

#518 made a Front Systems start that is queued behind another start (the
StartGuard's `ReserveOrRetry`) skip itself when the start ahead of it failed
for good (401/403, bad `base_url`). The check was a per-connection count of
permanent failures: if it moved while the start waited, the start was dropped.

That is right when the queued start is the same request. It is wrong when a
stop command is lost:

1. The pipeline is stopped and started again with fixed keys. The stop is core
   NATS and never reaches this consumer, so the old bad-keys start keeps the
   claim.
2. The new start, with the fixed keys, queues behind it.
3. The old start fails with 401 and bumps the count. The queued start skips
   itself.
4. The old start's "error" write is guarded by the `updated_at` it read (the
   management API bumped it on the restart), so it correctly writes nothing.

The row says `running`, nothing is trying to start the connection, and events
get 410.

## Acceptance criteria

1. A queued start is dropped only when the start it waited for wrote `error`
   to the row, or when the row's `updated_at` is still the one that failed
   start read. Otherwise it runs.
2. A test reproduces the sequence (stop lost, start with new keys queued, old
   start 401) and shows the new start runs and becomes active.
3. The existing "start queued behind a 401 is dropped" test still passes.
4. Only `src/cmd/front-systems-consumer` changes.

## Design

`permanentFailures` was `map[connectionID]uint64`. It is now a map to a
`permanentFailure{count, version, wroteError}`:

- `count` as before: the queued start still compares it with the value it saw
  when it was asked for.
- `version` is the `updated_at` the failed start read (`startResult.updatedAt`).
- `wroteError` is whether `markError` changed the row. `markError` now returns
  that (`RowsAffected() == 1`).

`afterAttempt` records the failure under `mu` before `markError`, then sets
`wroteError` after it returns. The failed start's claim is still held until
`afterAttempt` is done, so a queued start runs only after both are recorded.

The queued start (the `ReserveOrRetry` retry in `startConnection`) now runs
`sameRequestFailed` once the count has moved:

- `wroteError`: drop. No row read.
- no `version` (the failed start never read the row): run.
- otherwise read the row's `updated_at` (`rowRunning`, 10 s timeout, under the
  lifecycle context so `Stop` ends it) and drop only if it equals `version`.
- the read fails: run.

The read happens in `StartClaim.Release`, after the guard's lock is released
and with no consumer lock held, on the goroutine that released the claim. No
new goroutines, no new locks, no lock-order change.

## Decisions

- "Run" is the default whenever the answer is unknown (no version, row
  unreadable). The cost of being wrong is one repeated refused call, and the
  start then marks the row itself; the cost of the other mistake is the bug in
  the issue. A database that cannot be read also makes the start's own
  lookup fail transient, which the retry loop handles.
- A row that is gone or not `running` is not special-cased: its `updated_at`
  differs from, or equals, the version like any other, and a start that runs
  then finds no row (`startNotOurs`) or does what any start command does.
  The retry loop's "no longer running" check is for background retries, not for
  a start someone asked for.
- When the old start's "error" write fails (database blip) and the row is
  unchanged, the queued start is dropped, as before #574: it is the same
  request.
- The failed start's version is `res.updatedAt` (what the start read), not the
  pending start's `updatedAt` (which a retry loop may have refreshed from the
  status check).

## Touches

`src/cmd/front-systems-consumer`: `retry.go` (`permanentFailure`,
`sameRequestFailed`, `markError` returns a bool, `afterAttempt`), `service.go`
(`permanentFailures` type, the queued start's check), new
`retry_queued_test.go`. No migration, API, SDK or NATS change.

## Tests

`retry_queued_test.go`:

- `TestFrontSystemsRetry_QueuedStartAfterPermanentFailure` (sqlmock, table):
  1. old start's "error" write refused (0 rows), row has a newer version: the
     queued start runs, becomes active, event 202. This is the issue's
     sequence.
  2. old start wrote `error`: dropped, no row read (none scripted).
  3. "error" write failed, row still at the old version: dropped.
  4. "error" write failed, row unreadable: runs.
- `TestFrontSystemsRetryPG_QueuedStartRunsAfterRestartedRow` (Postgres): the
  issue's sequence with the real row. The old start is held at its first
  create, the row is stopped and started (updated_at bumped), the start
  command queues, the 401 lands; the new start activates and the row stays
  `running` with no `last_error`.
- `TestFrontSystemsRetryPG_QueuedStartDroppedWhenErrorWritten`: no restart;
  the "error" write lands and the queued start does not call Front Systems.
- The existing `TestFrontSystemsRetry_QueuedStartDroppedAfterPermanentFailure`
  is unchanged and passes.

Before the fix the first case of the table and the Postgres restart test fail
(the new start never runs: `active = false`, one create).

## Mutations

Each mutation of the new condition must fail a test. Run with
`-run QueuedStart` in the package (sqlmock table plus the two Postgres tests).

| Mutation | Failing test |
|---|---|
| `wroteError` ignored, always read the row | `PG_QueuedStartDroppedWhenErrorWritten`, the existing dropped test, table case "old start wrote error" |
| `wroteError` branch returns false | `PG_QueuedStartDroppedWhenErrorWritten`, the existing dropped test |
| version comparison inverted | table case "error write refused, row restarted", `PG_QueuedStartRunsAfterRestartedRow` |
| version branch always drops (the pre-fix behaviour) | table case "error write refused, row restarted", `PG_QueuedStartRunsAfterRestartedRow` |
| version branch never drops | table case "error write failed, row unchanged" |
| unreadable row drops | table case "row unreadable" |
| `markError` reports success when the guard refused | table case "error write refused, row restarted", `PG_QueuedStartRunsAfterRestartedRow` |
| `wroteError` never recorded | `PG_QueuedStartDroppedWhenErrorWritten`, the existing dropped test |
| failed start's version not recorded | three table cases |

## Verification

From `src/`: gofmt, `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race -count=3 ./cmd/front-systems-consumer/` with
`VRSKY_TEST_POSTGRES_URL` set, and `make lint-tenant` from the repo root.
