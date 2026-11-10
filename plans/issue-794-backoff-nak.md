# #794: retries follow the backoff (and `sdk.RateLimited` its delay)

Checked in the code on 2026-11-10 (`main` at `244b9881`). Backlog item 182
moved here from #721; the delayed-NAK code from branch `wip/794-delayed-nak`
(`4bb1adf4`) is ported, not cherry-picked (the Sitoo half and its docs are on
main already).

## Open question for Ludvik

Slot pressure is a human decision in the issue. This PR implements the
orchestrator's recommended answer (a cap per connection, plus a cap per
subscriber, both local to the worker; below). It changes no stream or
consumer configuration. If you would rather raise `MaxAckPending` or build a
delay stream, the comparison is below; the cap is a small, separate piece of
`subscriber.go` and comes out cleanly.

## Goal

A failed delivery comes back on the `DefaultBackoff` schedule (1 s, 5 s,
30 s, 2 min) and goes to the DLQ on the fifth failure, about 156 s after the
first attempt. Today `Subscriber.dispatch` NAKs with a plain `m.Nak()` and
nats-server redelivers at once, so all five attempts take under a second.

## What nats-server does with a NAK (2.15.1 in `go.mod`, 2.12.0 in production)

`processNak` and the part of `checkPending` that matters are the same in
both versions (diffed in the module cache; the only change in
`checkPending` is where `ttl` is read).

- A plain NAK goes straight on the redelivery queue. `BackOff` only applies
  when an ack wait runs out.
- `NakWithDelay(d)` sets the pending entry's timestamp to
  `now - AckWait + d` and writes it through `updateDelivered`, so it survives
  a leader change or restart. `checkPending` redelivers once
  `Backoff[i]` has passed since that timestamp, where `i` is the redelivery
  count so far (`NumDelivered - 1`, capped at the last step). So the wait is
  `d + Backoff[i] - AckWait`.
- `AckWait` is `Backoff[0]` (the subscriber keeps them equal, #99/#139).

To wait `w` after delivery `n`, the subscriber NAKs with
`d = w - Backoff[n-1] + AckWait`. For the plain schedule (`w = Backoff[n-1]`)
that is `d = AckWait`: the same as the attempt's ack wait starting over now.

A delayed NAK counts toward `MaxDeliver`, and redeliveries are served before
new messages. Once `len(pending) >= MaxAckPending`, `getNextMsg` serves
redeliveries only. That is the stall below.

## Change

`src/pkg/messaging/subscriber.go`

- `retryWait(backoff, ackWait, delivered, err)` returns the wait for this
  attempt and the delay to NAK with. The wait is `Backoff[n-1]`; an error
  that asks for a delay (`RetryDelay() time.Duration` on it or anything in its
  tree, as `sdk.RateLimited` has) gets the larger of the two. The asked-for
  delay is clamped to [1 s, largest step] first (a schedule shorter than 1 s,
  only in tests, wins over the floor). With `errors.Join` the largest delay in
  the tree wins.
- `dispatch` NAKs a failed attempt (handler error or panic) through
  `retry`, which uses `NakWithDelay`. The fifth failure still goes to the DLQ
  as before. A DLQ publish failure keeps its plain NAK (see findings).
- No metadata (should not happen on a JetStream message): plain NAK, logged.

## Slot pressure: the answer implemented

A message waiting out a delay stays pending on the server and keeps one of
the durable's `MaxAckPending` slots (32) for the whole schedule, about 156 s
with the defaults, up to `4 × 2 min = 8 min` when every attempt asks for the
2 min cap. Producer durables are shared by every connection of a type, and
most producers run two replicas on one durable. Without a bound, 32 waiting
messages from one tenant's dead target stop the durable for everyone: the
review of `4bb1adf4` measured a 10.1 s stall for tenant B behind 32
`RateLimited(1s)` messages from tenant A.

Each `Subscriber` keeps a small table of the messages it NAK'd with a delay
and that have not come back yet (stream sequence → connection, due time).
Before a delayed NAK it checks two caps:

| Cap | Option | Default | With `MaxAckPending` 32 |
| --- | --- | --- | --- |
| waiting messages per subscriber | `MaxDelayed` | `max(1, MaxAckPending/4)` | 8 |
| waiting messages per connection, per subscriber | `MaxDelayedPerConnection` | `max(1, MaxDelayed/2)` | 4 |

Over either cap, the message gets a plain NAK: today's behaviour, back at
once, five attempts in about a second, then the DLQ. The worker logs
`Handler error, retrying at once: too many messages waiting out a retry
delay` and counts it in `vrsky_message_retry_undelayed_total{durable}`.

An entry leaves the table when its message is delivered to this subscriber
again, or when its due time has passed (the message went to another replica).

What this bounds:

- Per durable, waiting messages hold at most `replicas × MaxDelayed` slots:
  16 of 32 with two replicas, 8 with one. The other slots keep serving new
  messages, so in steady state a message on a healthy connection waits for
  the deliveries queued ahead of it, not for a retry timer. The caps are per
  process: during a rolling update the old and the new pods count
  separately and together can still fill all 32 slots for a while. The
  durable-wide answer is #813.
- One connection holds at most `replicas × MaxDelayedPerConnection` (8, or
  4) of those, so one dead target does not use up the delayed budget of
  other connections that have a short blip.
- The tests (4 s steps): one connection with 40 failing messages, and ten
  connections with 4 failing messages each, do not delay a message on a
  healthy connection by more than 2 s, half a step (measured 0.8 to 0.9 s).
  Without the caps the healthy message waited 8.7 s with 2 s steps and
  16.5 s with 4 s steps, the failing messages' whole schedule.

What it costs:

- A burst of failures on one connection beyond 4 (per replica) messages gets
  the old fast path and is dead-lettered in about a second. For a dead target
  that is where those messages end up anyway, and they can be retried from the
  DLQ. For a blip of a few seconds on a busy connection, only the first few
  messages ride it out.
- The table is per process. After a restart it is empty while the server may
  still hold delayed messages from before, so for up to one schedule (about
  2.5 min) a replica can admit up to `MaxDelayed` more.
- The caps are per replica, not per durable. Two replicas double them (the
  defaults allow for that).

### The alternatives (not implemented)

- **Raise `MaxAckPending`.** A consumer config change, which is a hard limit
  for agents. It moves the threshold, not the failure mode: any fixed number
  of slots is filled by enough waiting messages, and a higher number means
  more prefetched messages sitting unheartbeated in a serial worker. It would
  pair well with the caps (more slots, same share for waiting messages).
- **Term and republish to a delay stream.** Nothing waits on the data
  stream, so no slot is held and every failed message gets the full schedule,
  however many fail. But it is a new stream, a new consumer that republishes
  on time, an attempt counter carried in a header instead of `NumDelivered`,
  the dedup window to handle on republish (a new `Msg-Id` per attempt), and
  ordering. That is stream and subject layout work, foundational and a hard
  limit. It is the right answer if fast-DLQ overflow turns out to bite.

## Acceptance criteria

1. Harness test (`src/pkg/sdk`, embedded server): with an injected short
   schedule, the gaps between attempts follow `Backoff` within a tolerance,
   there are exactly five attempts, and the DLQ copy comes after the fifth.
   Fails on `main` (gaps of one poll).
2. Harness tests: one connection failing every delivery, and ten failing
   connections each within the per-connection cap, do not hold a healthy
   connection's message more than 2 s (half of the 4 s step). The first
   fails without the per-connection cap, the second without the
   per-subscriber cap. A panicking handler is retried on the schedule too.
3. `sdk.RateLimited`: the wait is the larger of the step and the clamped
   delay; `Permanent(RateLimited(e, d))` and `RateLimited(Permanent(e), d)`
   are dropped, not retried; `errors.Join` takes the largest delay.
4. Unit tests (table-driven) for `retryWait` and the delayed table.
5. Docs and comments say what happens: `AGENTS.md`,
   `docs/NATS_ARCHITECTURE.md`, `docs/operator/troubleshooting.md`,
   `docs/sdk/interfaces.md`, the connector pages that state the rate-limit
   wait, `src/pkg/sdk/errors.go`, `lifecycle.go`.

## Fixes from the review of `4bb1adf4` (issue comment)

- The hold time is the sum of the waits, not "bounded by the 2 min cap".
- "Never shorter than `after`" now reads "never shorter than the clamped
  delay".
- Harness cases for Permanent wrapping RateLimited and the reverse.
- `errors.Join`: the largest delay wins (walked over the whole tree).
- `NATS_ARCHITECTURE.md` says the 2.12 source behaves the same.

## Packages touched

`src/pkg/messaging` (subscriber, new `retry.go`, metrics, tests),
`src/pkg/sdk` (errors, comments, tests), `src/cmd/remote-agent` (a test hook,
below). Docs. No connector code: the four connectors that pass 5 s and the
two that read `Retry-After` need no change. Nothing under
`cmd/*-producer` or `cmd/*-consumer`.

`TestOutput_MultiTargetLastAttemptRemembersNothing` drives one message
through all five attempts. With retries on the schedule its third attempt
came 30 s later and the poll timed out. The gateway got an
`outputBackoff` field (nil in production: the default schedule, each step
raised to the 30 s output ack wait), passed to both output subscriptions, and
that one test sets 1 s steps. The test asserts the same things as before.

## Risks

- Every worker's retry timing changes: a message now takes about 156 s to
  reach the DLQ instead of about a second. Alerts on `DLQGrowing` fire later
  for the same outage. Tests that expected a quick redelivery with the default
  schedule would need a short schedule (none found in the run).
- Remote-agent output durables (`MaxAckPending: 1`): a NAK'd write now
  comes back after 30 s, 30 s, 30 s and 2 min instead of at once (every step
  is raised to the 30 s output ack wait). That is one connection's own
  durable, so no one else waits, but a message held during a stop or
  redeploy is offered again 30 s later.
- Dedup window: the main stream's is 5 min. The default schedule (156 s)
  fits; a rate-limited destination at the 2 min cap can take 8 min, but
  destinations publish nothing back to the stream.

## Verification

Follow-ups after review (2026-11-10): a panic harness test; stall tests
with 4 s steps and a 2 s bound, one of them with ten failing connections for
the per-subscriber cap; a duplicate add refreshes its due time (unit
test). Mutations: plain `Nak()` on the panic path gives 76 ms gaps (fails);
removing the per-subscriber cap stalls the healthy message 16.5 s with 32
waiting (fails); removing the per-connection cap counts 8 waiting instead of
4 (fails); no due refresh fails the unit test.

Done on 2026-11-10: the backoff test measured gaps of 306/609/917/1223 ms
for steps of 300/600/900/1200 ms on 2.15.1, and the same within 5 ms on
2.12.0 (run with a `-modfile` replace, `go.mod` untouched). The stall test
delivered tenant B's message 0.9 s after it was published; with the caps
removed, 8.7 s. Mutations: plain `Nak()` gives gaps of 76 ms (fails); no
compensation gives 1.5 s for a 900 ms step (fails); no caps fails the stall
bound and counts 32 waiting messages; no per-connection cap counts 8 (fails).

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run`,
`go test -race ./...` (messaging, sdk, and every package that subscribes),
`make lint-tenant`, `make lint-connector`. Mutation: plain `m.Nak()` back in
`retry` fails the backoff test; caps removed fail the stall test.
