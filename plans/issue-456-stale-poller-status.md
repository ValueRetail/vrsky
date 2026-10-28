# An old poller still writes its final status after a newer start (#456)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A poller that ends on its own (one-shot done, or a setup error) writes its
final status (`stopped` or `error`) only while its map entry is still its
own. After a stop and a new start, the old poller ending late leaves both
the newer poller's entry and the stored status `running` alone. A poller
that ends with no newer start writes its status as before.

## Acceptance criteria

1. In api, sftp, cloud-storage and tenant, the ending paths write the
   final status only when `dropActive` reports that the entry was this
   poller's own.
2. Per consumer: old poller ends after stop plus a new start, the stored
   status stays `running` (no status write from the old poller); a poller
   ending with no newer start still writes its own status.
3. Mutation run: put back the unconditional status write and the tests
   fail.
4. From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
   `go test -race ./...`, `make lint-tenant` pass; new tests pass with
   `-count=10 -race`.
5. Backlog item 61 struck with `(#456)`.

## Where the status is written today

| Consumer | Ending path | File |
|---|---|---|
| api | `pollConnection`, one-shot (`OneTimeOnly`) | `src/cmd/api-consumer/api_poller.go` |
| sftp | `runPoller`, one-shot (`poll_interval_seconds <= 0`) | `src/cmd/sftp-consumer/service.go` |
| cloud-storage | `finishConn`, called from `runPoller` and `runEventLoop` | `src/cmd/cloud-storage-consumer/service.go` |
| tenant | `runBridge`, four setup error paths | `src/cmd/tenant-consumer/bridge.go` |

Each writes the status first and then calls `dropActive`, which already
skips the delete when the entry is no longer its own (#408).

## Approach

Same change in all four packages:

- `dropActive(connID, own) bool` reports whether it removed the entry
  (the entry was still `own`).
- The ending paths call `dropActive` first and write the status only when
  it returned true. Otherwise they log at info level that a newer start
  owns the connection and skip the write.

No change to `src/pkg/sdk`, the stop command or the start path.

## Tests

Written by a separate test author from the criteria above, in
`stale_status_test.go` per package, on the sqlmock fixtures the #408
tests use. sqlmock does not report unexpected calls, so the stale-write
tests check every earlier expectation was met, then register a catch-all
Exec and fail if anything reaches it. The catch-all matches any SQL with
any argument count: sqlmock ignores an Exec that doesn't match the next
expectation's SQL and argument count, so a narrower one could miss the
ending write and pass vacuously. The check also fails if an earlier
expectation was left unmet, since then nothing could reach the catch-all. The tenant JetStream-context
path is not covered: `nc.JetStream()` on the harness connection never
fails.

The #408 tests expected the old poller's status write (they documented
the behaviour this issue changes); that one expectation goes, nothing
else in them changes.

## Decisions

- Drop first, then write. Holding the consumer's lock across the status
  write would close the last gap fully, but it puts a DB call without a
  timeout under the lock every start and stop takes, so one slow write
  would stall every connection on that consumer. Rejected.
- Residual window, accepted: between the drop and the write, a new start
  could reserve, look the connection up, activate and write `running`
  before the old poller's single `UPDATE` lands. Normally that needs a
  start command within a few milliseconds of an old poller ending, but
  with the 6-connection DB pool saturated the new start's lookup and
  write can both get a connection first. The stop command's `stopped`
  write races a new start the same way. Before this change the window was
  the whole time between a restart and the old poller ending. The full
  fix (conditional UPDATE on `started_at`, or the StatusWrite ordering
  from #423/#424) is backlog item 79.
- During shutdown `Stop()` empties the active map, so a one-shot or setup
  failure that ends then no longer writes `stopped`/`error`. The DB keeps
  `running` and RestoreRunning re-runs it on the next boot, so sftp and
  cloud-storage with `after_action=none` can re-publish a one-shot that a
  deploy cut short.
- Before this change the write came before the drop. Swapping them means
  a stop arriving in between (entry still A's, stop removes it and writes
  `stopped`) leaves A's own `stopped` write out. Same final status.

## Risks

- A missed call site keeps the bug on one path. After the change,
  `grep updateConnectionStatus` in the four packages shows only the start
  (`running`), the stop command and the guarded ending paths.

## Verify

From `src/`: the build, vet, lint, race tests and `make lint-tenant`
above; `go test -race -count=10` on the four packages; the mutation run.
