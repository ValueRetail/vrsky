# Cancel-before-Stop in the other messaging.Subscribe callers (#459)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

#444 found the remote-agent gateway cancelling what its handlers wait on
before stopping the subscriber. The handler returned an error, the subscriber
NAK'd, and the dispatch loop, not yet halted, fetched the message again until
it was dead-lettered. #447 added `Subscriber.Halt` and fixed the gateway;
#467 pinned the order there.

Check every other `messaging.Subscribe` caller for the same shape: a handler
that returns an error once some context or resource is ended ahead of `Stop`.
Fix the ones that have it with Halt before cancel. Write down why the rest
are fine.

Acceptance criteria:

- Every non-test `messaging.Subscribe` caller has a verdict below, with the
  reason.
- Each buggy caller is fixed, with a harness test that stops while a message
  is in flight and fails without the fix (message dead-lettered or delivery
  count climbing).
- Backlog item 64 is struck with #459.

## The mechanism, and what makes a caller safe

`Subscriber.dispatch` calls the handler with `context.Background()`
(`src/pkg/messaging/subscriber.go`). So the subscriber never hands a
handler a context that shutdown cancels. A caller only has the bug if its
handler reaches past its own `ctx` argument for something the caller ends
before `Stop`: a captured outer context, a session, a closed connection.

## Callers

`grep -rn "messaging.Subscribe(" src --include='*.go'` outside tests:

| Caller | Verdict |
|---|---|
| `src/pkg/sdk/lifecycle.go` `subscribeDispatch` | fine |
| `src/cmd/data-filter/main.go` `Start` | fine |
| `src/cmd/data-converter/main.go` `Start` | fine |
| `src/cmd/tenant-consumer/bridge.go` `runBridge` | fine |
| `src/cmd/remote-agent/service.go`, `groups.go` | fixed in #447/#467, out of scope here |

`postgres-producer` and `postgres-consumer` (`src/pkg/io`) use core NATS
subscriptions, not `messaging.Subscribe`, so they are not in this class.

### `src/pkg/sdk/lifecycle.go`

`run` cancels `runCtx` and then calls `stop()` (= `Subscriber.Stop`) for
producers, filters and converters. That ordering would be the bug if a
handler saw `runCtx`, but none does:

- The start funcs for `RunProducer`, `RunFilter` and `RunConverter` take
  `runCtx` and never pass it on. `subscribeDispatch` doesn't take a context.
- The handler builds its context from the one the subscriber passes
  (`context.Background()`) plus trace and log values. `Deliver`, `Evaluate`,
  `Convert`, rehydrate and `republish` all get that context.
- The only other context a connector holds is the one `Configure` got: the
  caller's `ctx`, the parent of `runCtx`, which `run`'s `cancel()` does not
  touch. Every production `main` passes `context.Background()`. No producer
  in `src/cmd/*-producer` starts goroutines or stores that context for
  `Deliver`.
- The connector's own `Stop(ctx)` runs after `stop()` returns, so nothing a
  handler uses is released before the subscriber is stopped.

`RunConsumer` has no subscriber of its own; consumers that subscribe
(tenant-consumer, remote-agent) are covered separately.

Because this is the shared path, and the one most likely to be "improved"
later by threading `runCtx` into the handler so deliveries cancel on
shutdown, it gets a guard test (below) even though it has no bug today.

### `src/cmd/data-filter/main.go` and `src/cmd/data-converter/main.go`

Same structure in both. `main` does `cancel()` then `service.Stop()`, and
`Stop` closes `stopCh`, which makes a goroutine call `sub.Stop()`. The
cancelled context is passed to `Start`, but `Start` never uses it: the
handler closure's own `ctx` parameter shadows it, and `handleMessage` and
everything under it (`claimcheck.Rehydrate`, `getPipelineInfo`,
`processFilterEntry`/`processEntry`, the streaming paths, `pub.Publish`)
use the handler's context. The publisher, DB pool, spill store and NATS
connection stay open until `main` returns, after `Stop`. No handler error
can come from the cancel.

### `src/cmd/tenant-consumer/bridge.go`

`runBridge` waits on its bridge context, then calls `sub.Stop()`. The
bridge context is cancelled by a stop command or by `tenantConsumer.Stop`.
The handler again shadows `ctx` with its own and calls `handleSourceMessage`,
which publishes through `s.publish`, the SDK's publish closure. That closure
uses only the context it is given (`offloadIfLarge`, `pub.Publish`,
`res.DB.ExecContext`), none of it tied to the bridge context or `runCtx`. A
stop command therefore cannot make an in-flight bridge handler fail.

Something nearby but different: on process shutdown, `tenantConsumer.Stop`
cancels the bridges and waits a fixed 2 s rather than for the bridges'
`sub.Stop()` to return, and then the runner closes the NATS connection. A
bridge handler still publishing after those 2 s loses its ack and the
message comes back after AckWait on the next start (a duplicate bridged
envelope, not a dead letter). It is not cancel-before-Stop and no NAK loop
is possible (the fetch fails on the closed connection and the halted loop
exits), so it is out of scope. Not added to the backlog: it needs a
handler slower than 2 s at the exact moment of a redeploy, and the outcome
is at-least-once delivery, which the platform already assumes.

## Fix

None needed: no caller in scope has the bug. No production code changes.

## Test

One guard test in `src/pkg/sdk/runner_test.go`, written by a separate
test-author subagent, against the embedded NATS server
(`harness.StartEmbeddedJetStream`) and the real `sdk.RunProducer`:

- A producer whose `Deliver` blocks until the test releases it.
- Publish one envelope, wait for `Deliver` to start, cancel the runner's
  context (which cancels `runCtx` synchronously), then release `Deliver`.
  `Deliver` returns `ctx.Err()`, the way a connector that honours its
  context would.
- Assert: `Deliver`'s context was still live, the message was delivered
  once and acked (nothing pending or redelivered on the durable), and the
  DLQ is empty.

Test: `TestProducer_ShutdownDoesNotCancelInFlightDelivery`.

Mutations, both fail the test with `Deliver's context was cancelled by the
shutdown: context canceled` and `ack_pending=1`:

- Thread `runCtx` into `subscribeDispatch` and cancel the handler's context
  when it is cancelled (test author).
- In `RunProducer`, call `p.Deliver(ctx, env)` with the start func's
  `runCtx` instead of the handler's context (orchestrator, independently).

## Affected

`plans/issue-459-halt-before-cancel.md`, `plans/backlog.md`,
`src/pkg/sdk/runner_test.go`.

Not touched: `src/pkg/messaging`, `src/cmd/file-consumer`, webhook,
front-systems and remote-agent (other workers), consumer configuration.

## Risks

None in production: the PR adds a test and docs only. Delivery semantics are
unchanged, so no `agent:needs-human`.

## Decisions

- Did not add a defensive `Halt` to the SDK runner. It would mean a new
  return value on the internal `startFunc` and a change to when the runner
  stops fetching, for a bug that isn't there. The guard test catches the
  change that would introduce it.
- No guard tests for data-filter, data-converter and the tenant-consumer
  bridge. Their stop ordering lives in `main` or behind a database lookup,
  and testing it would need a refactor of code that is correct. The
  reasons above are short enough to recheck by reading.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant`. The new test with
`-count=20 -race`, plus the mutation run.

Results: build, vet, `golangci-lint` (0 issues), `go test -race ./...` and
`make lint-tenant` all pass. The new test at `-count=20 -race` passes 20 of
20, about 0.1 s a run.
