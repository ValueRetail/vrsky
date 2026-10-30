# BC consumer: an Edm.Date cursor_field must set a watermark (#602)

Backlog item 140, found in review of #592.

## Problem

`cursorTracker.observe` (`src/cmd/business-central-consumer/service.go`)
parsed the cursor value with `time.Parse(time.RFC3339)`. An `Edm.Date` value
(`2026-10-29`) fails that, so no watermark was ever stored and every
incremental poll read and sent the whole entity again.

## Decisions

- **Parse both forms.** `parseCursor` tries RFC 3339, then `2006-01-02`.
  A value that is neither is skipped as before.
- **Filter: `field ge 2026-10-29`**, the date unquoted (the OData v4 Edm.Date
  literal; a quoted string is a type error in BC). DateTime stays
  `field gt <instant>`. The form follows the stored watermark's shape
  (`isDateCursor`), so there is no new config.
- **`ge`, not `gt`, for a date.** A date has no time of day. With `gt` a
  record added later on the watermark day is never returned, and that is
  loss, not a narrow window as with a millisecond stamp. With `ge` BC returns
  the watermark day on every poll, and the node leaves out the records of
  that day it has already sent. The remembered set is the same mechanism as
  #591's (`cfg.aheadSeen`, key to cursor value, in the checkpoint `state`
  JSON): the tracker collects `atWatermark` (records dated on the newest
  date, reset when a newer date shows up) and the poll merges it into the
  set it saves. Records seen again are still fed to the tracker, so the set
  is rebuilt every poll and the previous day's keys drop out once the
  watermark moves.
- **Ceiling for a date: yesterday (UTC), not today.** The #591 rule is
  "nothing later than now". For a date, a watermark on today's UTC date can
  be a day ahead of BC's own today (BC up to 12 hours behind UTC in the
  Americas), and a record BC dates with its today then falls below the
  watermark and is lost. Yesterday (UTC) is behind BC's today everywhere
  (offsets are at most 14 hours). A date after that ceiling is sent once,
  stays out of the watermark, and goes into the remembered set; when it
  becomes yesterday it is not sent again and the watermark catches up. Cost:
  the filter returns the last two days on every poll, which the remembered
  set suppresses.
- **Warn bound for a date:** later than tomorrow (UTC). Today and tomorrow
  are BC's own date in another time zone, not a bad stamp.
- **Size Warn** (over 1,000 remembered, once per process) gets its own text
  for a date cursor: it is the watermark day that is large there.
- **Not done:** an edit that leaves the date alone is not re-sent (the date is
  not a modification marker); a record added with a date before the
  watermark is not seen. Both are properties of a date cursor, documented.
  BC's blank date `0001-01-01` is not a watermark (it equals the zero time
  the tracker starts from), which is what we want.
- DateTime behaviour is unchanged: same ceiling, `gt`, nothing remembered for
  the watermark record. Frozen-watermark recovery (#591) is untouched; it
  parses RFC 3339 only, and no date watermark was ever stored.

## Acceptance criteria

1. A date `cursor_field` sets the watermark on the first poll and advances it.
2. The filter is `<field> ge YYYY-MM-DD`; the next poll sends only new
   records.
3. A record added later on the watermark day is sent once, not lost, not
   repeated; the state survives a restart and is pruned when the watermark
   moves.
4. Dates after the ceiling follow #591 (sent once, out of the watermark, no
   second send when they become current).
5. DateTime fields behave as before.
6. Table-driven tracker tests and poll tests against a fake BC that honours
   `ge`/`gt` on dates and rejects any other literal, failing before the fix.

## Touches

`src/cmd/business-central-consumer` (service.go, cursor_date_test.go),
`docs/connectors/business-central.md`, `plans/backlog.md` (strike 140).

## Review

- Docs corrected: a date cursor never reads a record added with a date before
  the watermark, and nothing is logged. The "Date fields" section now says
  so and recommends `lastModifiedDateTime` unless the date field is never
  backdated.
- Backlog 143 (backdated records need a `lookback_days` window) and 144
  (changing `cursor_field` keeps the old watermark) added, both found in this
  review.
- Open question: the filter form `field ge 2026-10-29` (date unquoted) is the
  OData v4 `Edm.Date` literal, but it has not yet been tried against a real
  Business Central sandbox.
