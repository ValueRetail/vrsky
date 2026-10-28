# Start guard: status order after activation, and a refused start not lost (#423, #424)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Two gaps left in `sdk.StartGuard` (#375, #397, merged in #403), now shared by
every command-driven consumer:

- #423 (backlog 36): a start writes status `running` after `Activate` has
  put the connection in the active map, outside the owner's lock. A stop in
  between cancels the poller and writes `stopped`; the start's `running`
  write then lands last, and the database says `running` with nothing
  running. After a restart, `RestoreRunning` even starts the stopped
  pipeline again.
- #424 (backlog 37): a start refused because another start for the same
  connection is in flight returns at once. If that other start fails (a
  transient database error during boot restore, say), the refused start is
  gone and the connection stays down until someone starts it again.

## Acceptance criteria

1. A stop that lands after `Activate` and before the start's `running`
   write ends with status `stopped` in the database, whichever of the two
   writes reaches the database first. Same for every consumer that writes
   status (api, cloud-storage, db, file, kafka, rabbitmq, salesforce, sftp,
   tenant).
2. A stop followed quickly by a new start ends `running`: an older status
   write never overwrites a newer one for the same connection.
3. A start refused because another is in flight runs again if that other
   start gives up without activating (lookup error, not ours, bad config).
4. It does not run again if the other start activated (no double start), or
   if a stop arrived after it was refused (the stop is newer and wins), or
   if the consumer is shutting down (`StopAllLocked`).
5. Consumer shutdown (`Stop`) still leaves the database saying `running`,
   so the next boot restores the pipeline.
6. Connections in different tenants (and different connection IDs) are
   independent: a stop, a retry or a status write for one never affects
   another.
7. The exported SDK API stays backward compatible: `Reserve`, `Activate`,
   `Release`, `StopLocked`, `StopAllLocked` keep their signatures and
   documented behaviour; the new pieces are additions. The existing
   `startguard_test.go` and per-consumer `start_race_test.go` pass unchanged.
8. Tests that fail without each fix (mutation run), `go test -race` green,
   guard tests stable under `-count=20 -race`.
9. Backlog items 36 and 37 struck with the issue numbers, and item 91
   (left by #456) struck once the poller-ending writes are ordered too.

## Design (additions to `src/pkg/sdk/startguard.go`)

### #424: `ReserveOrRetry`

```go
func (g *StartGuard) ReserveOrRetry(mu sync.Locker, tenantID, connectionID string,
    active func() bool, retry func()) (*StartClaim, bool)
```

Same as `Reserve`, and `Reserve` becomes `ReserveOrRetry(..., nil)`. When the
refusal is because another start holds the claim (not because the
connection is active), `retry` is kept on that claim, the latest refused
start winning. The claim's `Release` runs it, after dropping the claim and
with the lock free, but only if the claim was still registered, which means
the start gave up on its own. `Activate` clears it (the connection is up, a
retry would be refused anyway), and so do `StopLocked` and `StopAllLocked`
(the stop is newer than the refused start). The consumer passes
`func() { s.startConnection(ctx, connectionID, tenantID) }`.

The retry runs on the giving-up start's goroutine (boot restore or the
start-command subscription), inside its deferred `Release`. No new
goroutine, so nothing outlives `Stop`.

### #423: ordered status writes

```go
type StatusWrite struct{ /* guard, connection ID, sequence number */ }
func (c *StartClaim) RunningStatus() StatusWrite
func (g *StartGuard) StopStatusLocked(tenantID, connectionID string) StatusWrite
func (w StatusWrite) Write(write func()) bool
```

Each connection has a sequence number, guarded by the owner's lock.
`Activate` takes the next one for the start's `running` write;
`StopStatusLocked`, called in the stop handler's critical section when the
stop cancelled an active connection, takes the next one for its `stopped`
write. `Write` runs the write under a lock of that connection's own (never
the owner's lock, so no database I/O under the active map's lock, and a slow
write holds up no other connection) and only if no write with a higher number has run for the
connection. Status writes then land in the order of the state changes that
caused them: a stop after activation always ends `stopped`, a new start
after a stop always ends `running`.

A second hook, `BeforeStatusWrite`, runs at the start of each `Write`, so
tests can hold a poller's late write while a newer start writes `running`.

A new test hook, `StartGuard.AfterActivate` (like `AfterReserve`, nil in
production), runs after `Activate` has released the lock, so tests can land
a stop between `Activate` and the `running` write.

`StopAllLocked` takes no number: on shutdown the start's `running` write
still goes through, so the database keeps `running` for the next boot.

## Packages and files

- `src/pkg/sdk/startguard.go` (+ tests in `src/pkg/sdk/startguard_test.go`
  or a new `_test.go` beside it)
- `src/pkg/sdk/restore.go` comment, if it describes the refusal
- `src/cmd/{api,cloud-storage,db,file,kafka,rabbitmq,salesforce,sftp,tenant}-consumer/service.go`:
  `ReserveOrRetry`, `RunningStatus().Write`, `StopStatusLocked` + `Write`
- `src/cmd/{api,sftp,cloud-storage,tenant}-consumer`: `dropActive` returns
  the ending write's ticket (after #456)
- `src/cmd/{brightpearl,business-central,sap-s4hana,visma}-consumer/service.go`:
  `ReserveOrRetry` only (they write no status)
- tests per consumer where a DB status matters (file consumer against
  `src/pkg/testdb`)
- `plans/backlog.md`: strike 36 and 37, note item 53 and sitoo

Not touched: `src/cmd/sitoo-consumer` (another worker owns the Sitoo
connector right now), `src/cmd/remote-agent`, `.github/workflows`,
`infrastructure/`.

## Decisions

- Retry instead of waiting. A refused start could instead block until the
  in-flight one finishes, which needs no consumer change, but the existing
  tests pin that a refused start returns at once, and a blocked start
  command holds up the subscription's other commands.
- Latest refused start wins, not a queue: they carry the same connection,
  and a queue of identical starts against a down database only repeats the
  same failure.
- One status lock per connection, not one for the guard. The first cut
  had one guard-wide mutex; review pointed out that a status UPDATE stuck
  on a row lock would then hold up every other connection's start and stop
  on that consumer. Lanes are created on demand and dropped when unused.
- The stop handler takes its ticket only when it cancelled an active
  connection. Every consumer sees every stop command, so taking one on
  every stop would grow the counter map with other consumers' connections
  (review).
- Each of the twelve changed consumers gets its own behavioural test of the
  wiring, not only the file consumer (review).
- Poller-ending writes go through the ordering too. #456 (PR #479, merged
  first) made api and sftp one-shots, cloud-storage `finishConn` and tenant
  `failBridge` write only when `dropActive` reports the entry was still
  theirs, and left a window (backlog item 91): a new start could write
  `running` between the drop and the old poller's write. `dropActive` now
  also returns a `StopStatusLocked` ticket taken in the same critical
  section, and the ending write goes through it, so that window is closed
  and item 91 is struck. No DB write runs under `s.mu`, as #456 required.
- The status order, lanes and pending retries are keyed by tenant and
  connection, not connection alone (coordinator, on review of #487).
  `ReserveOrRetry` and `StopStatusLocked` take the tenant the command
  names, the one its UPDATE filters on. A stop or start carrying the wrong
  tenant then lands in its own order: it can't skip or reorder the real
  tenant's writes, and its retry can't displace the real tenant's. Both
  functions are new in this PR, so this changes no released API. The stop
  handlers still cancel by connection ID whatever tenant the command names;
  that is older than this change and goes to backlog item 92 for a human,
  since it is a tenant-isolation change to every consumer.
- Sitoo is left on `Reserve` for now (it writes no status, so #423 does not
  apply); switching it is a one-line follow-up, noted in the backlog.

## Risks

- `Release` now may run a whole start. A consumer that calls `Release`
  somewhere other than a `defer` right after `Reserve` would run it early;
  all fourteen defer it.
- A `Write` blocked on a hung database holds that connection's lane, so a
  later start or stop of the same connection waits for it. Other
  connections are not affected.
- `seq` and `written` are keyed by connection ID only, like the active map.
  A stop carrying a wrong tenant ID would still advance the order while its
  UPDATE matches no row. That needs a forged command on internal NATS, and
  the stop handler already cancels by connection ID alone.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant`; guard tests with
`-count=20 -race`. Mutation runs: drop the retry in `Release`, drop the
sequence check in `Write`, and confirm tests fail.
