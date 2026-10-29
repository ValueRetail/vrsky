# #494: a subscriber's Stop waits out its fetch

## Open questions

For a human: is the residual window under Risks acceptable? In
production's three-node cluster, a message the consumer leader hands to
the pull request while `Halt`'s UNSUB is still on its way over the route
has one delivery spent and comes back after AckWait; on its fifth attempt
it is dropped without the DLQ. The exact alternative is short pull
requests (an `expires` of 100–200 ms) with `Halt` waiting out the current
one: no drain, no reclaim, no window, at the cost of a few pull requests
per second per idle subscriber (handled by the consumer leader, not
replicated) and a stop of up to one poll. The PR carries
`agent:needs-human` for this choice.

## Goal

`Subscriber.loop` in `src/pkg/messaging` long-polls with
`Fetch(batch, MaxWait(2s))`. `Halt` only stops the *next* fetch, so `Stop`
on an idle subscriber waits up to 2 s for the current one to time out.
Every pipeline stop, redeploy and remote-agent test cleanup pays that.
Make `Halt` (and so `Stop`) end the fetch at once, without changing what
happens to messages.

## What happens to messages today

- A fetch that returns messages, even after `Halt`, has them dispatched
  (handler runs, then ack, NAK or DLQ as usual). Then the loop sees the
  halt and exits.
- The fetch always runs until its pull request is finished on the server
  (batch filled, or the server's 408 at ~1.8 s, or the client deadline at
  2 s). So when the loop exits, no pull request is left on the server and
  nothing can arrive at the subscription afterwards. `Stop` then drains.

## Approach

1. The fetch takes a context: `context.WithTimeout(haltCtx, 2s)` passed
   as `nats.Context`. The 2 s deadline keeps the server-side request
   exactly as today (nats.go sets `expires` to 90 % of the deadline, as it
   does for `MaxWait`). `haltCtx` is cancelled by `Halt`.
2. A fetch that returns with the subscriber halted is not an error: no
   Warn, no 500 ms back-off sleep. Messages it did return are dispatched
   as today.
3. Reclaim. A cut-short fetch leaves its pull request open on the server
   for up to ~1.8 s. Anything the server hands to it would land in the
   subscription's buffer with nobody reading it, count a delivery, and
   come back only after AckWait (or never, if that was the last attempt).
   So when the loop sees the halt after a fetch, it first drains the
   subscription (UNSUB: the server stops delivering to the request), waits
   until nats.go reports the drain complete (its own Flush round trip and
   an empty buffer), and collects every message that arrived before the
   UNSUB took effect. These, plus whatever the fetch returned, are
   dispatched in order, as the uncut fetch would have dispatched them.
   It narrows the window to about a millisecond but can't close it (see
   Risks). The wait is bounded by the same 2 s as the fetch, so a broken
   connection can't make Stop slower than today.
4. `Stop` is unchanged: `Halt`, wait for the loop, `Drain` (a no-op if the
   loop already drained).

Exported API unchanged. No new NAKs, terms or DLQ paths.

## Acceptance criteria

- `Stop` and `Halt`+`Stop` on an idle subscriber return in well under
  200 ms (about 2 s before).
- A message being handled when `Halt` comes is acked or NAK'd exactly as
  before: one delivery, `NumDelivered` unchanged by the stop, nothing in
  the DLQ, and the next subscriber on the durable gets it.
- A message published 20 ms after `Halt` (with `Stop` later) is left for
  the next subscriber with no delivery spent. Before, the halted
  subscriber still handled it if its fetch was open.
- A message published within a millisecond or so of `Halt` is never lost
  and never dead-lettered by the stop: the halting subscriber handles it,
  or the next one gets it with at most one delivery spent (rare; bounded
  in the test).

These two replace the first version of this criterion, "never stranded",
which the test author's race test showed the design can't meet; see
Risks.
- No goroutine left behind after `Stop`.
- A halt-cut fetch logs nothing at Warn/Error.
- Existing `pkg/messaging` and remote-agent tests pass, also under
  `-count=20 -race`.

## Files

- `src/pkg/messaging/subscriber.go`
- `src/pkg/messaging/stop_test.go` (new, written by a separate test author)
- `plans/backlog.md` (strike item 81)

## Risks

- The residual window. nats-server (v2.15.1, `consumer.deliverMsg`)
  checks the request's interest, counts the delivery and tracks it as
  pending, then hands the message to an internal queue that publishes it
  later. If our UNSUB takes effect in between, the publish finds no
  interest. What follows depends on where the consumer's leader is:
  - On the server the client is connected to (the tests, the local
    stack, and a third of the time in production): the stream's send
    loop calls `didNotDeliver`, which restores the delivery count (a first
    delivery still reads 2 afterwards), drops the dead request and
    redelivers at once. Nothing is lost and no attempt is spent from
    attempt 2 on. The race test saw this a few times per thousand messages
    published within ~1 ms of a halt.
  - On another node: the leader sees the route's interest, publishes over
    the route, and the client's server drops the message silently. It
    comes back after AckWait (1 s by default) with one delivery spent; on
    its fifth attempt it is not redelivered and never reaches the DLQ. The
    window is the UNSUB's trip over the route plus the leader's queue
    latency, which the client can't measure.
  Today a graceful stop can't do this (the fetch outlives its request); a
  crash can. No client-side fix exists in nats.go 1.53 / nats-server 2.15:
  a pull request can't be withdrawn without dropping interest, and keeping
  interest means waiting for it to expire. Flagged for a human, with the
  polling alternative in the open question.
- Messages the cut fetch returned wait in `reclaim` without heartbeats.
  Normally that's one round trip (about 100 ms if something was buffered,
  `checkDrained`'s poll); `reclaim` gives up after half the AckWait so a
  slow connection can't let JetStream hand them to another subscriber
  first.
- Halted subscribers now stop receiving. Before, a message arriving while
  a halted subscriber's fetch was still open was handled by it, to a
  handler whose owner was usually shutting down (the #444 setting, where
  that costs an attempt). Now it stays in the stream for the next
  subscriber, untouched.
- Dispatching reclaimed messages after the UNSUB: acks, `InProgress` and
  NAKs go out on the connection, not the subscription, so they still work
  (checked in nats.go v1.53.1: `ackReply` uses `sub.conn`, which drain
  never clears).

## Verify

- New tests fail on main (Stop ~2 s) and pass with the fix; a mutation
  run on the halt handling and the reclaim.
- Timings of `go test -race ./pkg/messaging/...` and
  `./cmd/remote-agent/...` before and after.
- From `src/`: build, vet, golangci-lint, `go test -race ./...`,
  `make lint-tenant`; `-count=20 -race` on `pkg/messaging` and on
  `TestOutput_|TestGroup_|TestCommandLanes_` in remote-agent.
