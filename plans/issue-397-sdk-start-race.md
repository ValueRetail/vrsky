# One start guard in the SDK for every command-driven consumer (#397)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

#383 fixed a start race in the file consumer (#375). Thirteen other
consumers still have it. Move the reservation into `src/pkg/sdk` once and
have every consumer use it, so no consumer runs two pollers for one
connection, whatever order the boot restore and start commands arrive in.

## Acceptance criteria (from the issue)

1. At most one poller per connection in every listed consumer, in any order
   of restore and start commands.
2. A stop that lands mid-start wins: no poller is left running and the start
   does not write status `running`. A failed start leaves no reservation
   behind. Both as in #383.
3. The reservation lives once in `src/pkg/sdk`, added to the SDK without
   changing anything that exists.
4. One shared table-driven race test in the SDK: concurrent restore plus
   start, a stop mid-start, a failed start, three starts (the ownership case
   from #383's second review). One smoke test per consumer showing a second
   start while one is in flight does not start a second poller.
5. The comment on `RestoreRunning` in `src/pkg/sdk/restore.go` matches.
6. `go test -race ./...` passes; the shared reservation is mutation-tested.
7. Backlog item 35 struck with "(#397)".

## The race, per consumer

Line numbers are `src/cmd/<name>/service.go` on main at 10b31903. Each
`Run` subscribes to `vrsky.commands.*.connection.start`, then calls
`sdk.RestoreRunning`, which calls `startConnection` on the `Run` goroutine
while NATS can call the same function from the subscription goroutine.
Each `startConnection` checks the active map under `RLock`, unlocks, does
slow work (database lookup, secret resolution, in db-consumer a source
connection and ping), then inserts under `Lock`. Two starts that both pass
the check both insert; the second overwrites the first's cancel func, so a
later stop cancels only one poller and the other runs until the process
exits, publishing every record a second time.

| consumer | map | check | insert | stop handler | `Stop` | restore |
|---|---|---|---|---|---|---|
| db-consumer | `activeConnections` | 179 | 241 | 258 | 103 | 95 |
| api-consumer | `activePipelines` | 159 | 185 | 196 | 111 | 103 |
| salesforce-consumer | `active` | 186 | 209 | 217 | 150 | 143 |
| cloud-storage-consumer | `active` | 189 | 219 | 234 | 153 | 146 |
| sftp-consumer | `active` | 182 | 209 | 217 | 146 | 139 |
| sitoo-consumer | `active` | 218 | 243 | 250 | 182 | 175 |
| brightpearl-consumer | `active` | 154 | 177 | 184 | 119 | 113 |
| visma-consumer | `active` | 152 | 174 | 181 | 117 | 111 |
| business-central-consumer | `active` (`*poller`) | 234 | 261 | 289 | 196 | 190 |
| kafka-consumer | `active` | 172 | 192 | 200 | 136 | 129 |
| rabbitmq-consumer | `active` | 162 | 182 | 190 | 126 | 119 |
| sap-s4hana-consumer | `active` | 169 | 191 | 198 | 134 | 128 |
| tenant-consumer | `activeBridges` | 126 | 154 | 163 | 85 | 77 |

The same stop handlers also lose a stop that lands in the window: the map
has nothing yet, the stop is a no-op, and the start then finishes and runs
a poller for a pipeline the user stopped.

## Shared design: `sdk.StartGuard`

New file `src/pkg/sdk/startguard.go`. Only additions to the SDK.

```go
type StartGuard struct {
    // AfterReserve, when set, runs after a start has taken its claim, with
    // the owner's lock released. Tests use it to hold a start inside its
    // slow part. Nil in production.
    AfterReserve func(connectionID string)
    // unexported: starting map[string]*StartClaim
}

func (g *StartGuard) Reserve(mu sync.Locker, connectionID string, active func() bool) (*StartClaim, bool)
func (c *StartClaim) Activate(insert func()) bool
func (c *StartClaim) Release()
func (g *StartGuard) StopLocked(connectionID string) bool
func (g *StartGuard) StopAllLocked()
```

- The zero value is ready, so a field `starts sdk.StartGuard` needs no
  constructor and the existing struct-literal test setups keep working.
- The guard has no lock of its own. It runs under the owner's lock, the one
  that already guards the owner's active map, passed to `Reserve` as a
  `sync.Locker` (`&s.mu`; for a `sync.RWMutex` that is the write lock). One
  lock means "active" and "starting" are checked and changed in the same
  critical section, with no lock-order question between two mutexes.
- The owner's map stays the source of truth for "active". Several consumers
  remove themselves from it when a poller ends (api and sftp one-shot,
  cloud-storage `finishConn`, tenant bridge errors), so a guard that kept its
  own "active" set would refuse every later start of those connections.
- `Reserve`: under `mu`, refuse if `active()` or another claim exists;
  otherwise record a new claim. Then, unlocked, run `AfterReserve`.
- `Activate(insert)`: under `mu`, if a stop marked the claim, return false
  and change nothing; otherwise run `insert` (the owner's map write) and
  drop the claim if it is still this one's. `insert` runs with `mu` held and
  must not lock it.
- `Release`: under `mu`, drop the claim only if it is still this one's.
  Deferred right after `Reserve`; a no-op after `Activate` or after a stop
  took the claim. The ownership check is what keeps an abandoned start from
  freeing a newer start's claim (the three-start case).
- `StopLocked(id)`: caller holds `mu` (the stop handler already does, to
  remove the map entry). Marks the claim stopped and removes it, so a start
  after the stop is not refused while the abandoned one is still working.
  Returns whether a start was in flight.
- `StopAllLocked()`: the same for every claim, for the consumer's `Stop`.

### How each consumer adopts it

Every consumer gets the same four edits and nothing else in the start path
changes:

1. A field `starts sdk.StartGuard` next to the map and `mu`.
2. The `RLock` check becomes
   `claim, ok := s.starts.Reserve(&s.mu, id, func() bool { _, ok := s.active[id]; return ok })`,
   with the same "already running" log line (plus "or starting") and
   `defer claim.Release()`.
3. The `Lock`/insert/`Unlock` becomes `claim.Activate(func() { s.active[id] = cancel })`.
   On false: `cancel()`, log "start abandoned: a stop arrived while it was in
   progress", return before the status write and before the goroutine.
   db-consumer also closes the source database it opened.
4. The stop handler calls `s.starts.StopLocked(id)` inside its existing
   critical section; `Stop` calls `s.starts.StopAllLocked()` inside its own.

### What stays per consumer

- Logs, status writes and their order, events (db), webhook-only early
  returns (sitoo, brightpearl), config validation, the pollers themselves.
- A stop that only finds a claim writes no status, as in #383: the
  management API sets `stopped` before it publishes the command, and the
  start may yet turn out not to be this consumer's connection.
- The self-removal paths named above are unchanged (see Risks).

### The file consumer

It moves to the same guard: `reserveStart`, `activateStart`,
`releaseStart` and `startReservation` go, and `beforeStartWork` stays as
the file consumer's own seam (its #375 tests use it). Its seven
`TestStartRace_*` tests are the check that nothing changed.

## Out of scope (backlog)

- Self-removal deletes whatever is in the map under that id, without an
  ownership check (api `pollConnection` one-shot, sftp one-shot,
  cloud-storage `finishConn`, tenant `runBridge` errors). After a stop and a
  new start, the old poller ending can drop the new poller's entry; a third
  start then runs a second poller. Older than this change. New item 52.
- Items 36 (stop just after activation leaves `running`) and 37 (a refused
  start is dropped if the in-flight one fails) now apply to every consumer
  that uses the guard, not only the file consumer. New item 53.
- webhook-consumer (check-then-insert, but no poller: a double start only
  overwrites the map entry) and front-systems-consumer (no state) are not
  in the issue's list. A stop mid-start is still lost there. Part of 44.

## Risks

- Deadlock: `insert` runs with the owner's lock held. Each consumer's insert
  is a single map assignment. The `-race` tests would hang on a mistake.
- `StopLocked` called without the lock: a data race the consumer tests catch
  under `-race`.
- A consumer whose `mu` is passed by value would lock a copy. `go vet`
  (copylocks) flags that; every call passes `&s.mu`.
- Behaviour change, deliberate: a second start while one is in flight is
  refused where it used to run in parallel (the bug), and a stop mid-start
  now cancels the start.

## Tests

From a separate test author, given this plan's acceptance criteria and the
API above, not the implementation:

- `src/pkg/sdk/startguard_test.go`: table-driven, deterministic (gates, no
  sleeps): concurrent restore and start through `RestoreRunning` plus a
  start command, a stop mid-start, a failed start leaving no claim, three
  starts where an abandoned start must not free a newer start's claim.
- One test per consumer: hold a start in `AfterReserve`, send a second
  start, release, assert one poller (each package's existing fakes and
  style).

## Verification

From `src/`:
- `go test -race -count=10 ./pkg/sdk/... ./cmd/...`
- `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `make lint-tenant`
- Mutations, each restored afterwards: drop the ownership check in
  `Release`; drop the deferred `Release` in one consumer; put the unlocked
  check-then-insert back in one consumer. The tests must fail each time.

## Decisions

- Guard under the owner's lock, not its own: avoids two locks and keeps the
  owner's map authoritative.
- Exported `AfterReserve` test hook on the SDK type, nil in production,
  rather than a hook per consumer.
- File consumer migrated too, so the reservation lives once.
- Backlog items 52 and 53 follow main's 51 (renumbered at merge); other open PRs may also take
  numbers from 43.
