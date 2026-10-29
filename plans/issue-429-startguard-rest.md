# Webhook, Front Systems and remote agent on the start guard (#429)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

#397 and #487 put thirteen command-driven consumers on `sdk.StartGuard`.
Three were left (backlog item 53):

- webhook-consumer checks and inserts in two steps. Two starts both pass the
  check; a stop that lands while a start is loading finds nothing to stop,
  and the start then registers the webhook and writes `running`.
- front-systems-consumer has no guard and no record of what it started, so
  boot restore plus a start command (or two start commands) register its
  Front Systems webhooks twice.
- remote-agent checks and inserts in two steps and runs every command on its
  own goroutine. Two starts both subscribe the output durable; the second
  insert overwrites the first session, whose subscriber is never halted or
  stopped. A stop that lands while a start is loading is lost, and the start
  writes `running` afterwards.

## Acceptance criteria

Per consumer, each with a test that fails without that consumer's wiring:

1. Concurrent starts of one connection start it once (webhook: one map
   entry and one `running` write; Front Systems: webhooks registered exactly
   once; remote agent: one session, one output subscription).
2. A stop that arrives while a start is in progress ends stopped: nothing
   registered or subscribed, and the last status written (where the
   consumer writes status) is `stopped`.
3. A start refused (webhook, Front Systems) or queued (remote agent) while
   another is in flight is not lost when the first one fails.
4. Remote agent: no leaked subscription. After racing starts, a replace with
   a changed configuration, a stop mid-start or shutdown mid-start, no
   subscriber is left fetching for a session that is not in the map.
5. Remote agent: #467's halt-before-cancel order in `endSession` and the
   `reconcileMu` rules are unchanged; `TestOutput_|TestGroup_` stay green
   under `-count=20 -race`.
6. `src/pkg/sdk` unchanged.
7. Backlog item 53 struck with #429; the Sitoo note kept.

## Design

### webhook-consumer and front-systems-consumer

Same shape as kafka-consumer after #487:

- `starts sdk.StartGuard` under the consumer's existing lock (webhook's
  `sync.RWMutex`; a new `sync.Mutex` plus `active map[string]struct{}` for
  Front Systems).
- `ReserveOrRetry` before the slow part, `defer claim.Release()`,
  `claim.Activate(insert)` and, in webhook, `claim.RunningStatus().Write`.
- Stop handler: `StopLocked` in its critical section; webhook also takes
  `StopStatusLocked` when it removed a registration and writes `stopped`
  through it. `Stop` calls `StopAllLocked`.
- Front Systems registers before `Activate`. If registration fails it
  returns without activating, so the next start (or one refused meanwhile)
  tries again. Its stop handler now forgets the connection, so a later start
  registers again, as it did before this change. It still writes no status.

### remote-agent: per-connection command lanes

`StartGuard` does not fit: a start may replace a running session whose
configuration changed, so a refused start (or one dropped because the first
activated) could leave the old configuration running. Instead,
`src/cmd/remote-agent/lanes.go` adds `commandLanes`:

- Every start and stop takes a turn on its connection's lane when it
  arrives: in the NATS callback (before the goroutine is spawned) for
  commands, at the call for boot restore. A turn waits for the turns before
  it to finish, then runs. Commands for one connection run one at a time in
  the order they arrived.
- A start then sees what the previous command left: same configuration is a
  no-op, a changed one replaces, and a start that failed leaves the next one
  to try.
- Each lane counts the stops queued per tenant. A start gives up when a stop
  for its tenant was queued after it (checked before loading, and again in
  the critical section that would insert the session). If it built a
  session, it ends it with `endSession` (halt before cancel) instead of
  inserting it.
- `Stop` sets `closed`; a start finishing after that ends its session too.
- A stop command now writes `stopped` when it ended a session, inside its
  turn, so it lands after the `running` of the start before it.
  `stopConnection` itself (used by the replace and not-ours paths) still
  writes nothing.
- Lanes hold no lock while a command runs: a turn is a channel. The lane
  mutex is a leaf (taken after `s.mu` in the insert check, never before
  it, never around I/O), and no turn is waited on with `s.mu` or
  `reconcileMu` held, so `endSession` and `reconcileGroups` are untouched.
- The 30 s command timeout starts when the turn starts.

## Packages and files

- `src/cmd/webhook-consumer/service.go` + tests
- `src/cmd/front-systems-consumer/service.go` + tests
- `src/cmd/remote-agent/service.go`, new `lanes.go` + tests
- `plans/backlog.md`: strike 53, keep the Sitoo note

Not touched: `src/pkg/sdk`, `src/pkg/sdk/lifecycle.go`, data-filter,
data-converter, tenant-consumer, file-consumer, sitoo-consumer,
`.github/workflows`, `infrastructure/`.

## Decisions

- Remote agent gets queueing, not refusal. Serialising per connection gives
  each start the outcome of the one before it, which is what the
  fingerprint/replace model needs, and no start is lost. Commands already
  run on their own goroutines, so a waiting command blocks no subscription.
- The turn is taken on the NATS callback goroutine, so the order of one
  subscription's commands survives the hop to the worker goroutine. Start
  and stop are separate subscriptions; their relative order is whatever NATS
  delivered, as for every other consumer.
- A stop queued after a start makes it give up even if it has already done
  its lookups, like `StopLocked` does for the guard. Stops naming another
  tenant don't count, so a stop with the wrong tenant cannot cancel the real
  tenant's start (the same keying #487 chose).
- The remote agent's stop now writes `stopped`. Without it, a start that
  finished just before the stop's turn leaves `running` in the database
  after the API wrote `stopped`. This matches every other consumer's stop.
- Front Systems: a stop does not unregister webhooks at Front Systems and
  the webhook route keeps accepting events for stopped connections. Both are
  older than this change; noted in the backlog rather than changed here.
- Front Systems: a start for a connection that is already started now does
  nothing. Before, every start registered again, so a redeploy with changed
  `events` or `callback_url` while running picked the change up; now that
  takes a stop and a start, as on every other guarded consumer (review).
  Backlog item 97 covers the registration lifecycle as a whole.
- Remote agent: a start that fails a check after a stop was queued still
  writes `error` over the API's `stopped`. Older than this change and rare;
  left as is to keep the change small (review).
- Sitoo stays on `Reserve` (PR #472 is parked on that connector).

## Risks

- A remote-agent command now waits for the connection's previous command.
  Each is bounded by its own 30 s context (restore by the database calls'
  own behaviour, as before), so a hung start delays only that connection's
  next command.
- The agent's permission model is untouched: same lookups, same tenant
  checks, same allowed operations. Only the order and concurrency of starts
  and stops change.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant`; the new tests and
`TestOutput_|TestGroup_` in remote-agent with `-count=20 -race`. Mutation
run per consumer: revert that consumer's wiring and confirm its tests fail.
