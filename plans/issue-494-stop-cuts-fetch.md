# #494: a subscriber's Stop waits out its fetch

## Decision

Ludvik, 2026-10-29, on PR #521: don't accept the residual window of the
first version (a halt's UNSUB racing a delivery, which on a three-node
cluster costs the message an attempt, and on its last attempt loses it
without the DLQ). Rework to short pull requests: each fetch asks the server
to hold its pull request only briefly, and `Halt`/`Stop` wait out the
current one instead of cancelling it. No context cancellation of a fetch in
flight, no drain to withdraw a request, no reclaim. The exported API stays
the same.

## Goal

`Subscriber.loop` in `src/pkg/messaging` long-polled with
`Fetch(batch, MaxWait(2s))`, so `Stop` on an idle subscriber waited up to
2 s for the fetch to time out. Every pipeline stop, redeploy and
remote-agent test cleanup paid that. Make a stop take at most one short
poll, without any stop ever stranding a message.

## Design

1. `pollWait = 150ms`. Each fetch is one pull request that the server holds
   for 150 ms (its `expires`). The loop checks the halt between fetches and
   sends nothing once halted. `Halt` doesn't touch the fetch in progress.
   A consumer with an AckWait under 600 ms polls for a quarter of its
   AckWait instead (at least 10 ms). See item 4 for why.
2. A fetch ends on the server's word, not on a client timer. It uses
   `FetchBatch` and collects until the batch is complete or the server's
   408 says the request expired. (`Fetch` ignores a 408 when it has nothing
   and waits for its context, so with `Fetch` every idle round would end on
   the client's clock.)
3. The client listens longer than the server holds the request. nats.go
   sets a pull request's `expires` to 90% of the time left until the
   context's deadline, and listens until the context is done.
   `pollContext` reports a deadline that gives the server 150 ms and stays
   open for `fetchWait` (2 s). With an honest deadline the client would
   stop listening 15 ms after the server started the request's clock. A
   request that took longer than that to reach the server (a pod held back
   by its CPU limit, a GC pause, a slow route) would outlive its fetch, and
   a halt would leave it open. With `pollContext` that margin is 1.85 s, and
   the 2 s backstop only matters when no answer comes at all.
4. Everything a fetch returned is dispatched, halted or not: handled, then
   acked, NAK'd or dead-lettered as usual. The server counted those as
   delivered to this subscriber, and only it can settle them. Dispatch
   starts after the fetch is over, as before, so a NAK never lands while
   our request is still open. That is what #444 needs. The price is that
   the first message of a batch waits up to a whole poll for its handler,
   without heartbeats. If that wait passed the AckWait, the server would
   redeliver the message into the same fetch and it would be handled twice.
   Hence the cap. Nothing in production sets an AckWait below the default
   second; some tests do. Main had the same exposure with its 1.8 s
   requests.
5. The 500 ms back-off after a failed fetch ends on a halt. It holds no
   pull request.
6. `Stop` is unchanged: halt, wait for the loop, drain.

## Why there is no window

Messages for a pull request are queued on the server ahead of its 408, and
both travel the same path to the client, so the 408 comes after the last
message the request delivered. The loop exits only after a fetch has ended:
batch complete, 408, or a status error that ends the request (409 consumer
deleted or leadership change, 503 no responders). At that point the server
holds no pull request of ours. A halted loop sends no new one. When `Stop`
drains, there's nothing for the UNSUB to race: it can't cost a delivery or
leave one in a buffer nobody reads.

What remains is a request that reached the server more than 1.85 s late,
or a connection lost mid-fetch. Main had the same with a 200 ms margin, and
there it's a crash, not a stop.

## Cost

An idle subscriber sends a pull request every poll, so about 6–7 a second
(150 ms held plus a round trip), and the server answers each with a 408.
Main sent one every 2 s. The consumer leader handles them in memory. The
main stream and its consumers are R1 today (`EnsureStreams` sets no
replicas), so nothing is replicated. On an R3 consumer, nats-server 2.15
proposes each pull request to the consumer's raft group when it arrives and
again when it ends, so this would become two raft proposals per poll per
subscriber. Worth remembering if the stream ever goes R3.

Latency gets better. With a batch above 1, a message reaching an idle
subscriber used to wait up to 1.8 s for the request to expire before it was
dispatched. Now the wait is at most 150 ms. A backlog smaller than the batch
now waits for the 408 too, up to 150 ms, where main's `Fetch` returned it
at once with a no-wait request. A full batch returns at once.

`Stop` on an idle subscriber now takes up to one poll, about 150 ms, down
from up to 2 s. A halted subscriber still handles what its last poll
brought: at most 150 ms of new messages, down from 2 s.

## Acceptance criteria

- `Stop` and `Halt`+`Stop` on an idle subscriber return within one poll and
  a margin (test bound 400 ms; about 2 s before).
- A message being handled when `Halt` comes is acked or NAK'd exactly as
  before: one delivery, nothing in the DLQ, and after a NAK the next
  subscriber gets it with `NumDelivered=2`.
- A message published around a halt is never stranded. Either the halting
  subscriber's last poll brought it, and it was handled once, on its first
  delivery, and acked. Or it is left untouched (no delivery counted) and the
  next subscriber gets it with `NumDelivered=1`. No exceptions, not even
  rare ones.
- The same holds when the subscriber's pull requests reach the server late.
- A message published after the halted subscriber's last poll ended is left
  untouched: a halted loop sends no more pull requests.
- A halt ends the back-off after a failed fetch.
- With an AckWait shorter than a poll, a message is still handled once.
- No goroutine left behind after `Stop`. A stop during a poll logs nothing
  at Warn or above.
- Existing `pkg/messaging` and remote-agent tests pass, also under
  `-count=20 -race`.

## Files

- `src/pkg/messaging/subscriber.go`
- `src/pkg/messaging/stop_test.go`
- `plans/backlog.md` (strike item 81)

## Tests

In `stop_test.go`, all against the embedded JetStream server:

- `TestStopIdleSubscriberIsPrompt`: idle stop ≤ 400 ms at several points
  in a poll and several polls in. A stop that hangs fails after 5 s.
- `TestHaltDuringHandlerKeepsDeliverySemantics`: ack and NAK cases.
- `TestHaltRaceWithPublish`: publish before, after and concurrently with
  `Halt`, 20 ms after it, and after the last poll ended, with batch 1 and
  32. No strands at all.
- `TestHaltOnSlowLinkStrandsNothing`: the subscriber's connection goes
  through a proxy that delays everything it sends by 60 ms. A message is
  published every 20 ms from the halt to past the last poll. No strands.
- `TestShortAckWaitPollsShorter`: with an 80 ms AckWait and a batch of
  32, a message is handled once, on its first delivery.
- `TestStopLeavesNoGoroutines`, `TestHaltLogsNothing`,
  `TestHaltEndsBackoff` (the fetch fails because the durable was deleted).

## Risks

- `pollContext` depends on how nats.go 1.53 turns a context into
  `expires`. If an upgrade changes that, an idle fetch would end on the 2 s
  backstop, or the server would hold requests for 2 s. Either way the idle
  stop test fails (stops take up to 2 s), and the slow-link test catches a
  client that stops listening before the server's request expires.
- A fetch failing with a status error after some messages now logs a Warn
  and backs off after dispatching them. Main's `Fetch` returned the
  messages and dropped the error.
- Six or seven pull requests a second per idle subscriber, see Cost.
