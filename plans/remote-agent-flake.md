# TestOutput_StopReleasesTheMessage flakes under CI load (#444)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Make `TestOutput_StopReleasesTheMessage` pass reliably on a loaded CI runner
without weakening what it checks: a stopped pipeline releases its held
message, and the next session delivers it.

## What the test was waiting on

The issue guessed at a JetStream redelivery or lease timer. It isn't one.
The 5 s `waitFor` never gets the message because the stop dead-lettered it.

`endSession` cancelled the session context first and stopped the subscriber
afterwards. In between, the output handler saw the cancel and returned
`errSessionEnded`, the subscriber NAK'd, and the dispatch loop, whose stop
channel was not closed yet, went round and fetched the same message again.
The handler, its scope already done, NAK'd it at once, and so on. Each round
is one delivery attempt; after `MaxDeliveryAttempts` (5) the subscriber moves
the message to the DLQ and acks it. The new session then has nothing to
deliver.

On an idle machine the window between the cancel and `Stop` is a mutex and a
map copy, so it almost never shows. Under load the stopping goroutine can be
descheduled for a few milliseconds, which is enough: with a 2 ms pause
injected there, 5 of 5 runs failed in 6.06 s, the same shape as the CI
failure (6.09 s). Unforced, 0 of 4000 runs failed locally (80 copies of
`-count=50 -cpu 1` in parallel).

The group path had it worse: member subscribers were stopped one after
another after the cancel, each spinning while it waited for the ones before
it, including the pipeline's own `Stop`, which can take up to the 2 s fetch
wait.

This is a production bug (a stopped or redeployed pipeline can dead-letter
the file it was holding for an offline agent), not a test-only problem. The
test was right to fail.

## Fix

1. `src/pkg/messaging/subscriber.go`: add `Subscriber.Halt()`, which closes
   the stop channel (once) and returns at once. `Stop()` calls it and then
   waits as before. Additive; existing callers see no change.
2. `src/cmd/remote-agent/service.go` `endSession`: halt every subscriber,
   then cancel, then `Stop` each. A handler that returns after the cancel
   finds the loop halted, so its NAK is the only attempt the stop costs,
   which is what the `errSessionEnded` comment already promised.
3. `src/cmd/remote-agent/groups.go`: the same order where a departed
   member's subscriber is stopped (halted under the lock that takes it out
   of `memberSubs`, so an `endSession` in between can't cancel it while it
   still fetches), and where a just-started one is thrown away. Neither
   reconcile path has its own test; the group test covers `endSession`.
   A member subscriber started after the session was already cancelled can
   still spin until it is thrown away: a small window that predates this
   change.
4. Test seam: `gateway.afterCancel`, an unexported func field, nil in
   production, called in `endSession` between the cancel and the stops.
   Tests set it to sleep 100 ms so the old race is certain, not rare.

## Tests

- `TestOutput_StopReleasesTheMessage`: holds the window open with the seam
  and asserts the DLQ is empty after the stop, then (as before) that the new
  session delivers the body.
- `TestGroup_StopReleasesTheMessage` (new): two offline members each hold
  the message; stop with the window held open; DLQ empty; both get it after
  a restart.
- `TestHaltBeforeCancelCostsOneAttempt` (new, `pkg/messaging`): a handler
  that holds until its owner ends; Halt, end, pause, Stop; exactly one
  attempt, nothing in the DLQ, the next subscriber gets the message.

Mutations:

- Drop the `Halt` calls in `endSession`: both gateway tests fail with the
  message in the DLQ.
- Drop `sub.Halt()` in the messaging test: 5 attempts, DLQ 1.
- Ack instead of NAK on stop (`return nil` for `errSessionEnded`): the
  output test fails, the new session never gets the message.

## Affected

`src/pkg/messaging/subscriber.go`, `src/pkg/messaging/messaging_test.go`,
`src/cmd/remote-agent/service.go`, `src/cmd/remote-agent/groups.go`,
`src/cmd/remote-agent/gateway_test.go`, `src/cmd/remote-agent/groups_test.go`,
`plans/backlog.md`.

Not touched: the agent API, the agent's allowed operations or permission
model, consumer configuration (AckWait, BackOff, MaxDeliver, MaxAckPending).

## Risks

- `pkg/messaging` is shared. `Halt` is new and `Stop` keeps its behaviour;
  `Stop` is now safe to call twice, which it wasn't.
- This changes when the gateway's subscribers stop fetching, i.e. message
  handling on a stop. It can only remove deliveries, never add them, but it
  is in the "could drop or replay messages" class, so the PR gets
  `agent:needs-human`.

## Decisions

- Fixed the production ordering instead of loosening the test. Raising the
  test's timeout would not help: the message is gone, not late.
- Left `TestOutput_HoldsWhileAgentOffline_NoRedeliveryCount` and
  `TestOutput_TakesOnlyItsPredecessorsMessage` alone. Their ~6 s is a
  deliberate 4 s wait (four AckWaits; the effective AckWait floor is 1 s) or
  a 4 s negative check, plus the 2 s fetch wait at cleanup. Different cause,
  not flaky. Noted in the backlog.
- Other `messaging.Subscribe` callers (`pkg/sdk`, `data-filter`,
  `data-converter`, `tenant-consumer`) were not audited for the same
  ordering. Noted in the backlog.

## Verification

- `go test -race -count=50 -run '^TestOutput_StopReleasesTheMessage$'
  ./cmd/remote-agent/` in 80 parallel copies at `-cpu 1`, before and after.
- `go test -race -count=10 -cpu 1,2 -run 'TestOutput_|TestGroup_StopReleases'`
  in 5 parallel copies.
- `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test -race
  ./cmd/remote-agent/... ./pkg/messaging/...`, `make lint-tenant`.
