# Two starts for one file connection can race (#375)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

The file consumer never runs two watchers for one connection, whatever the
order in which the boot restore and start commands reach `startConnection`.

## Acceptance criteria (from the issue)

1. At most one watcher per connection, whatever the interleaving of restore
   and start commands. Check and insert happen under one lock, or a
   per-connection reservation is taken before the slow part.
2. A start that fails part-way (database error, path refusal) leaves no
   reservation behind, so a later start can succeed.
3. A test runs a restore and a start for the same connection concurrently,
   many times under `-race`, and asserts one watcher and one publish per
   file.
4. `plans/backlog.md` item 20 struck with this issue's number.

Added here (see "Stop during a start"):

5. A stop command that arrives while a start is past the check but not yet
   active wins: no watcher is left running, and the start does not write
   status `running`.

## The interleaving

Line numbers are `src/cmd/file-consumer/service.go` on main at 1d2f4752.

`Run` subscribes to `vrsky.commands.*.connection.start` (226) and then calls
`sdk.RestoreRunning` (242) on its own goroutine. NATS runs the start
handler on the subscription's goroutine, so `startConnection` can run twice
at once for the same connection:

| restore goroutine | start-command goroutine |
|---|---|
| 296–302: `RLock`, not in `activeConnections`, `RUnlock` | |
| | 296–302: `RLock`, not in `activeConnections`, `RUnlock` |
| 304: `getConnection` (DB) | 304: `getConnection` (DB) |
| 345–369: resolve, `MkdirAll`, baseline | 345–369: same |
| 372–374: `Lock`, `activeConnections[id] = ac1` | |
| 381: `go watchDirectory(ctx1, ac1)` | |
| | 372–374: `Lock`, `activeConnections[id] = ac2` (overwrites ac1) |
| | 381: `go watchDirectory(ctx2, ac2)` |

Two watchers poll the same directory with separate `knownFiles`, so every
new file is published twice. ac1 is no longer in the map, so a stop command
cancels only ctx2 and the first watcher runs until the process exits. The
window is the database round trip plus the filesystem work, milliseconds
wide, and a deploy is exactly when the UI and restore both start things.

## Approach

A per-connection reservation, held from the check until the connection is
in `activeConnections` or the start gives up. The lock is not held across
the database or filesystem calls.

- `fileConsumer.starting map[string]*startReservation`, guarded by `s.mu`,
  allocated lazily.
- `reserveStart(id)`: under `s.mu`, refuse if the id is in
  `activeConnections` or `starting`; otherwise put a new reservation in
  `starting`. Replaces the `RLock` check at 296–302.
- `activateStart(id, res, ac)`: under `s.mu`, if a stop marked the
  reservation, return false; otherwise insert `ac` and drop the reservation
  in the same critical section. Replaces 372–374. "Active" and "starting"
  never both hold, and there is no moment where neither does.
- `releaseStart(id, res)`, deferred right after the reservation is taken:
  under `s.mu`, delete `starting[id]` only if it is still `res`. Every early
  return (DB error, not a file consumer, path refusal, `MkdirAll` failure,
  a stop) runs it; after a successful activation it is a no-op.
- A nil-by-default hook, `beforeStartWork func(connectionID string)`, runs
  after the reservation and before the database lookup. Tests use it to hold
  one start inside the window while another arrives, instead of sleeping.

The second start logs "already active or starting" and returns, like the
"already active" case today. `startAndWaitActive` keeps working: repeated
commands still return before touching the database.

### Stop during a start

Today a stop that lands in the window finds nothing in `activeConnections`,
logs "not active" and is lost; the start then finishes and the watcher runs
for a pipeline the user stopped. With the reservation, `handleStopCommand`
also looks in `starting`: it marks the reservation stopped and removes it.
The start sees the mark in `activateStart`, cancels its context, does not
start the watcher and does not write status `running`.

When only a reservation was found, stop does not write status `stopped`.
The management API sets `stopped` itself before it publishes the command
(`StopConnection` in `pkg/managementapi/handler.go`), and the reservation
may belong to a connection that turns out not to be a file connection, whose
status this service has no business writing. When a watcher was active,
stop behaves as before.

Removing the reservation (not only marking it) means a start that arrives
after the stop is not refused while the abandoned one is still working; it
takes its own reservation and the abandoned one never activates because it
checks its own mark.

The service's `Stop` marks and clears all reservations the same way, so a
start in flight at shutdown does not start a watcher nobody will cancel.

### The #366 baseline ordering

Unchanged. The baseline is still taken (`listFiles`) before the connection
is put in `activeConnections` and before status `running` is written; the
only change is that the insert now goes through `activateStart`. Tenant path
resolution, `MkdirAll` permissions and the #378 watcher (`pollOnce`,
stability rule, clock, `pending`/`knownFiles`) are not touched.

## Files

- `src/cmd/file-consumer/service.go`
- a new test file from the test author (`src/cmd/file-consumer/start_race_test.go`)
- `plans/backlog.md`

## Risks

- Lock held across slow calls: none. `s.mu` is held only for map work.
- Deadlocks: `reserveStart`, `activateStart`, `releaseStart`, the stop
  handler and `Stop` each take `s.mu` once and call nothing that takes it
  again. The deferred release takes `s.mu` after `activateStart` has
  returned. `Configure` replaces `activeConnections` under `s.mu` and does
  not touch `starting`.
- A start that panics part-way: the deferred release still runs.
- Not fixed here, pre-existing: a stop that lands after `activateStart` but
  before the `running` write leaves the database saying `running` with no
  watcher. Backlog item 33.
- The same check-then-insert shape is in the other command-driven consumers
  (db, api, salesforce, cloud-storage, sftp, ...). Out of scope; backlog
  item 32.
- New with this change (the test author spotted it): a start refused because
  another is in flight is dropped, not queued. If the first then fails, the
  connection stays down until the next start. Before, the second start ran
  anyway, at the price of the double watcher. Backlog item 34.

## Decisions

- Reservation rather than one lock across check and insert: one lock would
  hold `s.mu` across a database query and filesystem calls, and the upload
  and events handlers read the map under the same lock.
- Stop during a start wins and does not write status (above).
- A test seam (`beforeStartWork`) in production code, nil outside tests.
- A refused second start returns at once rather than waiting for or retrying
  after the first (backlog item 34).
- Backlog numbers start at 32 because #379 is taking 31; they may need
  renumbering at merge time.

## Tests

Written by a separate test author from the acceptance criteria, in
`src/cmd/file-consumer/start_race_test.go`. The database is a small fake
`database/sql` driver that answers every lookup and status write and counts
them; sqlmock answers each expectation once, so a second start would fail on
the mock and hide the bug. Watchers are counted by behaviour (one publish
per file over ten further polls) and by the "Starting directory watch" log
record, not by map entries, since the old code overwrote the entry.

- `TestStartRace_RestoreAndStartCommandConcurrent`: 60 parallel iterations,
  restore and a start command released together.
- `TestStartRace_SecondStartWhileFirstInProgress`: one start held in
  `beforeStartWork` while the other arrives, three pairings.
- `TestStartRace_StartWhenAlreadyActiveIsNoop`
- `TestStartRace_FailedStartLeavesNothingBehind`: DB error, `../other-tenant`
  and an absolute path outside the base, via restore and via command; then a
  start succeeds and publishes once.
- `TestStartRace_StopDuringStartWins`
- `TestStartRace_StartAfterStopWhileAbandonedStartInFlight`

## Verification

From `src/`:
- `go test -race -count=20 ./cmd/file-consumer/...`
- `go test -race ./...` once, `go vet ./...`, `golangci-lint run ./...`,
  `make lint-tenant`
- Mutations: put back the unlocked check-then-insert and the concurrency
  test must fail; drop the deferred release and the failed-start test must
  fail. Both restored afterwards.

## Review

A fresh reviewer approved with no must-fix findings. Nits taken: the PR says
plainly that a refused start is now dropped (item 34), and item 32 notes
that the "harmless" comment on `RestoreRunning` only holds for the file
consumer until the other consumers get the same fix. Not taken: a test for
the ownership check in `releaseStart` (a mutant that deletes without
checking survives); it needs a three-start interleaving and the case it
guards is a stop-then-start during a start that then fails.
