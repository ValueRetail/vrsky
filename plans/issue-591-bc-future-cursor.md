# BC consumer: a far-future cursor must not freeze the watermark (#591)

Backlog item 128, found in review of #457.

## Problem

`cursorTracker` (`src/cmd/business-central-consumer/service.go`) keeps the
newest `cursor_field` value of a fetch and the next poll asks for
`<field> gt <watermark>`. One record stamped 2099 sets the watermark to 2099,
and no later record is ever newer, so the node goes quiet.

## Decisions

- **Ceiling: now** (the injectable clock `bcConsumer.now`), changed in
  review. The first version let any value up to now + 5 minutes become the
  watermark; with a `gt` filter, a watermark in the future hides every record
  BC stamps before it, so a held-back record caught up on a poll a few
  minutes early, or a bogus stamp inside the 5 minutes, lost records. Now
  nothing later than now becomes the watermark. If BC's clock runs ahead of
  the pod's, a fresh record is held one poll and then taken without a second
  send (test `BCClockAheadOfThePod`).
- **`maxCursorSkew` (5 minutes)** is only the Warn line and the line past
  which a stored watermark counts as frozen. Not configurable.
- A record dated after now **is published**, is not a watermark candidate,
  and, when more than 5 minutes ahead, is logged at Warn once (`record_id`,
  `cursor_value`, `bound`).
- **Not re-sent every poll.** The watermark stays behind the record, so the
  filter returns it every poll. The node keeps `aheadSeen` (record key to the
  cursor value it was sent with) in the existing checkpoint `state` JSON,
  next to the picture state, and leaves out a record whose key and cursor
  value match. Key is `id`, else `number`, else a hash of the record. An edit
  changes the cursor value, so it is sent again. The set holds only the
  far-future records the last complete poll carried, so a record whose date
  is corrected or that is deleted drops out. The same check lets a record
  that the clock has now reached pass without a second send, and the
  watermark moves on.
- State is swapped in only after the whole fetch succeeded, like the
  watermark: a failed poll re-sends at most what it already sent (the
  pipeline's at-least-once bargain).
- A **watermark already stored more than 5 minutes ahead** (a node frozen
  before this fix) is replaced, with a Warn, by a bounded recovery (changed
  in review; the first version read the whole entity again, which could be
  100k+ records to destinations that may not dedupe, and again after a
  failed run):
  - Without pictures, the code before the fix saved only when the watermark
    moved, so the checkpoint's `LastProcessedAt` is when it froze, and every
    record stamped before that poll was read. Resume from `LastProcessedAt`
    minus 1 hour (RFC3339 UTC, never later than now minus 1 hour). The hour
    covers the length of that poll and BC's clock running behind.
  - With pictures (on now, or a `pictures` object in the stored state) every
    poll saved, so `LastProcessedAt` is not the freeze time; the same if it
    is zero. Read from the start. Considered: resuming from now minus a bound
    with a Warn. Rejected: it drops whatever changed between the freeze and
    the bound, silently apart from a log line. A full read costs a replay;
    the other costs data.
  - Not covered: a frozen node that read an even later far-future value
    saved again then, so `LastProcessedAt` is that save; records changed
    between the two saves are older than the window. The Warn carries
    `watermark`, `last_processed_at` and `resume_from` so an operator can
    use Resend everything.
  - The recovery's start is stored explicitly at the end of the poll when the
    read found nothing to take a watermark from (review finding 3: otherwise
    `saveProgress` copied the frozen watermark back). For a full read that
    is "", so the next poll reads from the start again.
  - `loadCursor` returns the checkpoint row; `saveProgress` stores
    `LastProcessedAt` from the same injectable clock.
- A remembered set over 1,000 records logs a Warn once per process: the
  cursor field is probably a business date.
- Resend (`resendAll`) clears `aheadSeen`, so everything goes out again.
- Non-incremental nodes are untouched.

## Acceptance criteria

1. A record dated after now does not move the stored watermark; later
   records are still read and the watermark follows them. No record BC
   stamps before the stored watermark is lost.
2. That record is published once, and logged at Warn with its id when more
   than 5 minutes ahead; it is not re-sent on later polls, after a restart,
   or when the clock reaches it; an edit to it is sent.
3. Records dated at or before now behave as before (no state written,
   nothing withheld).
4. A watermark frozen by the old code is recovered by reading from
   `LastProcessedAt` - 1 h (no pictures) or from the start (pictures, or no
   `LastProcessedAt`), and is not written back when the recovery finds only
   future-dated records.
5. Table-driven tracker tests and poll tests against a fake BC that honours
   `gt`.

## Touches

`src/cmd/business-central-consumer` (service.go, pictures.go state struct,
tests), `docs/connectors/business-central.md`, `plans/backlog.md` (strike
128, add 140: `Edm.Date` cursor fields never set a watermark).
