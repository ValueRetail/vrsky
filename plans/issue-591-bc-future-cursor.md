# BC consumer: a far-future cursor must not freeze the watermark (#591)

Backlog item 128, found in review of #457.

## Problem

`cursorTracker` (`src/cmd/business-central-consumer/service.go`) keeps the
newest `cursor_field` value of a fetch and the next poll asks for
`<field> gt <watermark>`. One record stamped 2099 sets the watermark to 2099,
and no later record is ever newer, so the node goes quiet.

## Decisions

- **Bound: now + 5 minutes** (`maxCursorSkew`), against an injectable clock
  (`bcConsumer.now`). The cursor is a last-modified field BC stamps itself,
  so real drift is seconds; five minutes covers drift and a poll interval.
  The cost of a bogus stamp inside the bound is a hidden window of at most
  that length. There is no overlap in this consumer (the filter is `gt`), so
  "now + overlap" from the backlog line has nothing to anchor to. Not
  configurable: no case has asked for it.
- A record past the bound **is published**, is not a watermark candidate,
  and is logged at Warn once (`record_id`, `cursor_value`, `bound`).
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
- A **watermark already stored beyond the bound** (a node frozen before this
  fix) is dropped with a Warn and the entity is read once from the start.
  This sends everything again once; the alternative is that the node stays
  frozen. If this pod's own clock were far behind, every poll would be a
  full read; accepted, a wrong clock breaks more than this.
- Resend (`resendAll`) clears `aheadSeen`, so everything goes out again.
- Non-incremental nodes are untouched.

## Acceptance criteria

1. A record beyond now + 5 min does not move the stored watermark; later
   sane records are still read and the watermark follows them.
2. That record is published once and logged at Warn with its id; it is not
   re-sent on later polls, after a restart, or when the clock reaches it;
   an edit to it is sent.
3. Records with sane timestamps behave as before (no state written, nothing
   withheld).
4. Table-driven tracker tests and poll tests against a fake BC that honours
   `gt`.

## Touches

`src/cmd/business-central-consumer` (service.go, pictures.go state struct,
tests), `docs/connectors/business-central.md`, `plans/backlog.md`.
