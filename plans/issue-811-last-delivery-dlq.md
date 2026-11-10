# #811: the last delivery's message reaches the DLQ, or says why it did not

Checked in the code on 2026-11-10 (`main` at `c4064f8d`, after #810).
Backlog item 199.

## What happens today (verified)

In the nats-server 2.15.1 source (module cache; production runs 2.12, same
code paths per the #794 diff):

- `getNextMsg`: a message on the redelivery queue gets
  `dc = incDeliveryCount(seq)`. If `dc > maxdc` it sends the
  `MAX_DELIVERIES` advisory, deletes the message from `pending`, writes that
  through `updateDelivered`, moves the ack floor, and skips it.
- `checkPending`: when a pending message's ack wait runs out,
  `hasMaxDeliveries(seq)` does the same once `deliveryCount >= maxdc`.

So on the fifth (`MaxDeliver`) delivery a plain NAK, a delayed NAK and no
answer at all end the same way: the durable forgets the message. Only an
ack or a term is any different, and both mean "done".

`Subscriber.dispatch` on that delivery:

- handler error: copies the message to `VRSKY_DLQ`, then acks. If the DLQ
  publish fails it NAKs (`will NAK and retry`), which is the dead end above.
- handler panic: the deferred `recover` calls `retry` whatever the attempt,
  so it never dead-letters. `retry` also adds the message to the delayed
  set, where it holds one of the `MaxDelayed` places until its due time
  (up to 2 min), though it never comes back (issue comment from the review
  of #810).

The message is still in `VRSKY_DATA` (Limits retention, 72 h, or until the
size caps shed it), but nothing reads it there by sequence.

Reproduced on the embedded server before the fix
(`TestProducer_LastDeliveryReachesTheDLQ`, `TestLastDeliveryHoldsNoDelayedSlot`):
see Verification.

## Change

`src/pkg/messaging/subscriber.go`

1. The handler runs inside its own `recover` (`runHandler`). A panic becomes
   the error `handler panicked: <value>` and from there takes exactly the
   path of a returned error: retried on the backoff before the last
   delivery, dead-lettered on it. `X-Vrsky-Last-Error` says it panicked.
   The `panic` status of `vrsky_message_processing_seconds` stays.
2. Because `retry` is now only reached before the last delivery, no message
   on its last delivery enters the delayed set.
3. The DLQ copy is retried in place (`deadLetter`): up to
   `DLQPublishAttempts` (3) publishes, 500 ms and then 2 s apart, each
   bounded by `DLQPublishTimeout` (2 s). The heartbeat keeps running, so the
   message stays in flight on the server meanwhile. A retry cannot make two
   copies: the DLQ publish carries `Msg-Id: <stream>-<seq>` and `VRSKY_DLQ`
   has the 2-minute duplicate window, so a publish that was stored but whose
   answer was lost is collapsed. Worst case the loop is held about 8.5 s;
   when the DLQ answers with an error at once, 2.5 s.
4. If every attempt fails, the subscriber neither acks, NAKs nor terms the
   message. It logs `Could not copy the message to the DLQ; it is left
   unacked and stays only in VRSKY_DATA` at error level with tenant,
   connection, durable, stream, stream sequence, attempt count, the last
   handler error and the DLQ error, and counts it in
   `vrsky_dlq_publish_failed_total{pipeline_id, worker}` (the labels of
   `vrsky_dlq_messages_total`).

`src/pkg/messaging/publisher.go`: `PublishToDLQ` now passes its context to
`PublishMsg`, so the per-attempt timeout applies (it ignored `ctx` before).

## Why this, and not the other options

- Leave it unacked rather than NAK or term: none of the three keeps the
  message in the durable on the last delivery (above). An ack or a term
  would claim it was handled. A NAK reads as "retry", which is the dead end
  this issue is about. Left unacked, it shows in the durable's unacked
  count until its ack wait runs out, and then the server's own
  `MAX_DELIVERIES` advisory carries the stream sequence, the same trace a
  worker that crashed on its last attempt leaves. It holds one
  `MaxAckPending` slot for one ack wait (2 min in production), at a time
  when the DLQ, and so JetStream, is failing anyway.
- Retry in place with a short bound rather than longer: the dispatch loop
  is serial, so while it retries nothing else on that replica is handled,
  and the rest of the fetched batch waits without heartbeats. A few seconds
  covers a reconnect or a leader move; a longer outage is not helped by
  holding the loop.
- The halt does not cut the retries short. A stopping pod would otherwise
  give up on a message it could still save; the bound keeps Stop to a few
  seconds more. Known gap: the SDK's Stop budget is 5 s (#808), shorter
  than the 8.5 s worst case, so a pod stopped during a long DLQ outage can
  exit mid-retry. The message then ends as it does today.
- The `recover` now covers only the handler. Before, it also covered the
  ack, the DLQ copy and the NAK; none of those panics, and a panic there
  should not be turned into a retry.

## What would make it fully recoverable (for Ludvik; not done here)

Both need a change agents may not make on their own (consumer config, or a
new stream/subject or advisory consumer):

- `MaxDeliver` one higher than the attempts the subscriber counts
  (`MaxDeliveryAttempts + 1` on the consumer, DLQ on `NumDelivered >= 5` as
  now). A message whose DLQ copy failed is then NAK'd with a delay and comes
  back once more, and that sixth delivery only tries the DLQ copy. Changes
  every durable's config (reconciled in place by `ensureConsumer`), and a
  sixth delivery whose copy also fails is lost the same way, just later.
- A small listener on `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.VRSKY_DATA.*`
  that reads the message from `VRSKY_DATA` by its stream sequence and copies
  it to the DLQ. It would also cover a worker that crashes or is OOM-killed
  on the last attempt, which loses the message today too. New consumer of
  system advisories, so foundational. Backlog item 201.

## Acceptance criteria

1. Harness test (`src/pkg/sdk`, embedded server), table-driven: the fifth
   delivery fails with the DLQ refusing publishes (its `MaxMsgSize` set to
   1, which `EnsureStreams` does not reconcile), and with the handler
   panicking. Fails on `c4064f8d`; after the fix, one DLQ copy when the DLQ
   comes back within the bound or the handler panicked, with the right
   `X-Vrsky-Last-Error`; no sixth delivery; the payload still in
   `VRSKY_DATA`.
2. With the DLQ refusing throughout: exactly `DLQPublishAttempts` publishes,
   no DLQ copy, `vrsky_dlq_publish_failed_total` up by one, no sixth
   delivery.
3. A panic or error on the last delivery leaves nothing in the delayed set
   (`TestLastDeliveryHoldsNoDelayedSlot`, package test).
4. `docs/NATS_ARCHITECTURE.md`, `docs/operator/troubleshooting.md` and
   `AGENTS.md` say what happens, and how to find the message by its stream
   sequence.

## Packages touched

`src/pkg/messaging` (subscriber, publisher, metrics, tests), `src/pkg/sdk`
(one test file; the comment of `TestProducer_PanicRetriesFollowTheBackoff`).
Docs, `plans/backlog.md`. No stream or consumer config changes.

## Risks

- A DLQ outage holds a replica's dispatch loop up to about 8.5 s per message
  on its last attempt. Messages queued behind it in the same batch may hit
  their ack wait and be redelivered (spending an attempt), as behind any
  slow handler.
- No alert on `vrsky_dlq_publish_failed_total` yet; the rules live in
  `infrastructure/prometheus-rules.yml`, outside this change. Backlog
  item 202.

## Verification

Before the fix (`c4064f8d`, the new tests with the two constants stubbed):
`TestProducer_LastDeliveryReachesTheDLQ` failed in five of six cases. In
each the durable reported 0 unacked and 0 waiting after the fifth attempt,
and the DLQ held no copy: the message was only in `VRSKY_DATA`. The DLQ
was published to once (and not at all on a panic).
`TestLastDeliveryHoldsNoDelayedSlot/handler_panics`: no DLQ copy, and 1
message still counted in the delayed set.

After: both pass; `go test -race -count=3` on `pkg/messaging` and
`pkg/sdk` passes.

Mutations, each caught:

- a panic on the last delivery takes the retry path again: three harness
  cases and the delayed-set test fail;
- one DLQ try instead of three: the recovery case and both give-up cases
  fail;
- a NAK after giving up: both give-up cases fail (the durable counts 0
  unacked right away instead of 1);
- no `vrsky_dlq_publish_failed_total` increment: both give-up cases fail.
