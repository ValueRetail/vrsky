# #721: producer retry signals (backlog 180, 182)

Checked in the code on 2026-11-09 (`main` at `eafadd3c`).

## Goal

- 180: `sitoo-producer` retries and dead-letters a message whose `base_url`
  it refuses, like the other destinations, instead of dropping it.
- 182: the delay in `sdk.RateLimited(err, d)` decides when the message comes
  back, instead of being ignored.

## What the code does today

- `sitoo-producer` `Deliver` returns `sdk.Permanent` from
  `safeurl.CheckBaseURL`, so the SDK acks and drops the message.
- `sdk.RateLimited` stores the delay; only tests call `sdk.RetryAfter`. The
  SDK returns the error to `messaging.Subscriber.dispatch`, which calls
  `m.Nak()`.
- Measured against the embedded server (nats-server 2.15.1): a plain `Nak()`
  is redelivered at once, not after the backoff. With a backoff of
  1 s, 2 s, 3 s, 4 s and a handler that always fails, the gaps between
  deliveries were 151 ms each (one poll), and the message was in the DLQ
  0.6 s after the first attempt. `processNak` in nats-server puts a NAK
  without a delay on the redelivery queue; `BackOff` only applies when the
  ack wait runs out. So the "1 s, 5 s, 30 s, 2 min" in the docs and in the
  issue does not happen for NAK'd messages. That is wider than this issue
  (every retriable error of every worker) and is reported as its own
  finding, not changed here.
- `NakWithDelay(d)` works, with one twist measured on the same server: it
  sets the pending timestamp to `now - AckWait + d`, and the redelivery
  check compares against `BackOff[n-1]` for delivery `n`. So the wait is
  `d + (BackOff[n-1] - BackOff[0])`. With the same 1 s to 4 s backoff and
  `NakWithDelay(1s)` the gaps were 1.06 s, 2.11 s, 3.02 s, 4.07 s.

## Change

- `sitoo-producer`: `sdk.Retriable` for a refused `base_url`. Flip
  `TestDeliver_RefusesBadBaseURL` to assert not permanent (the issue asks
  for this change of behaviour).
- `pkg/sdk/errors.go`: the unexported `rateLimitedError` gets a method
  `NakDelay() time.Duration`. No exported identifier or signature changes.
- `pkg/messaging/subscriber.go`: `dispatch` looks for an error in the chain
  with `NakDelay()` (an unexported interface, found with `errors.As`). If
  there is one, and the message is not on its last attempt, it calls
  `NakWithDelay(d)` with `d` clamped to at least 1 s and at most the
  subscriber's largest backoff step (2 min by default). Everything else is
  as before: plain errors `Nak()`, the last attempt goes to the DLQ, a panic
  and a failed DLQ publish `Nak()`.
- No change to the stream or consumer config (BackOff, MaxDeliver, AckWait,
  retention), the subject layout or any exported SDK API.

## Acceptance

- A refused Sitoo `base_url` is not `sdk.IsPermanent` (test fails on the
  old code).
- Against the embedded server through `pkg/sdk/harness`: a producer that
  returns `sdk.RateLimited(err, d)` is redelivered after about `d`, not at
  once, is delivered exactly 5 times and then lands in the DLQ.
- A delay above the largest backoff step is cut to that step (embedded
  server), and the clamp has a table test (floor, ceiling, in range, zero).
- Mutation: with `NakWithDelay` replaced by `Nak`, and with the Sitoo fix
  reverted, the new tests fail.

## Files

- `src/cmd/sitoo-producer/service.go`, `base_url_test.go`
- `src/pkg/sdk/errors.go`, `producer.go` and `lifecycle.go` (comments),
  new `src/pkg/sdk/ratelimit_test.go`
- `src/pkg/messaging/subscriber.go`, new `src/pkg/messaging/nakdelay_test.go`
- Docs: `docs/operator/troubleshooting.md`, `docs/NATS_ARCHITECTURE.md`,
  `docs/sdk/interfaces.md`, connector pages for Sitoo, Front Systems,
  Visma, Brightpearl, Business Central, SAP S/4HANA
- `plans/backlog.md`: strike 180 and 182

## Risks

- MaxDeliver counts a `NakWithDelay` redelivery like any other, so the DLQ
  after five attempts holds. Tested.
- Ack wait and heartbeats: the heartbeat stops before the NAK as before;
  the redelivered message gets the same ack wait as today.
- A delayed message stays pending on the consumer and counts toward
  `MaxAckPending` (32). The producer durables are shared by every
  connection of a type, so a tenant whose target keeps answering `429`
  can hold up to 32 slots for up to the delay, slowing the other tenants
  of that worker. Today the same messages churn to the DLQ in under a
  second. The 2 min cap bounds it. Flagged for the human reviewer.
- The wait is `d + (BackOff[n-1] - BackOff[0])` on this server; with the
  default schedule a 5 s hint waits 5 s, 9 s, 34 s, 124 s (about 172 s to
  the DLQ). If a later nats-server measures `d` from the NAK instead, the
  wait becomes `d`, never less. Either way it is at least the delay asked
  for. Production runs `nats:2.12-alpine`; the processNak code is the same
  shape there (not verified against a 2.12 binary).
- Visma, Brightpearl, Business Central and SAP pass a fixed 5 s and do not
  read `Retry-After`. Unchanged here; noted as a follow-up.

## Verification

- In `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race -count=1 ./pkg/sdk/... ./pkg/messaging/... ./cmd/sitoo-producer/...`
- From the root: `make lint-tenant`, `make lint-connector`
- Mutation runs on both fixes.
