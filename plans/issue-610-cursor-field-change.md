# BC consumer: changing cursor_field must drop the old watermark (#610)

Backlog item 144, found in review of #602.

## Problem

The Business Central consumer keeps its watermark in the node's checkpoint row
(`LastProcessedMessageID`) and the records it remembers for it in the row's
`state` JSON (`ahead`). Neither says which `cursor_field` they belong to. Edit
the field on a running connection and the next start sends the old value as a
filter on the new field: `postingDate gt 2026-10-29T10:00:00Z` (BC answers 400
on every poll), or a date taken from one column applied to another (records
hidden without an error).

## Decisions

- **Store the field in the checkpoint state** as `cursor_field`, next to
  `pictures` and `ahead` (`pictureState`, `pictures.go`). It is written on
  every save of an incremental node, through `BCConfig.checkpointState()`.
  `nodeState()` is unchanged, so what the node remembers is still decided in
  one place and the existing state tests keep their meaning.
- **Compare at the start of each incremental fetch.** Stored field present
  and different from `effectiveCursorField()` means changed. Then the fetch
  ignores the stored watermark (reads with no cursor filter), starts the
  remembered `ahead` set empty, and logs one Warn. At the end it stores the
  new watermark, or, when the new field gave none (only future-dated records,
  say), stores an empty one (`replace`), so the old field's value cannot stay.
  The check is per fetch, so it works for a restart and for a config the
  poller already holds.
- **A legacy row (watermark, no recorded field) matches the configured
  field.** The alternative, treating it as changed, re-reads and re-sends the
  whole entity for every incremental BC connection on the first deploy of this
  fix. Most of those never changed the field, so the cost is high and the
  benefit small. The fetch forces a save so the field is recorded at once
  (`fieldUnrecorded`), after which a change is seen. Known gap: a node whose
  field was changed before this upgrade is not detected; the docs say to
  use Resend everything (there is no checkpoint reset).
- **No row, no change.** A first run has nothing to drop.
- **Pictures state is kept.** `pictures` is keyed by record id and is
  independent of the cursor field.
- **A fetch that fails halfway saves nothing**, so the next poll sees the
  same mismatch and drops the watermark again. It re-reads from the start
  until one fetch completes, which is the same at-least-once trade as the rest
  of the cursor logic.
- **Not covered:** a non-incremental node records no field (it has no
  watermark in use). Turning `incremental` off, changing the field and turning
  it on again can still pick up the watermark an earlier incremental run left.
  That is the existing stale-watermark behaviour of toggling `incremental`,
  not new here.

## Acceptance criteria

1. Same field across polls (and across restarts): the watermark is kept, the
   filter is unchanged.
2. Field changed: the next request has no filter on the old value; every
   record is read and published once; the following poll filters on the new
   field's watermark.
3. A legacy checkpoint without a field name keeps its watermark (no re-read,
   same filter) and is stamped with the field on the first poll; a later
   change is then detected.
4. Field switched back (A, B, A): each switch reads from the start; the
   watermark of the field left behind is not reused.
5. When the new field yields no watermark, the stored watermark is cleared.
6. Table-driven tests against a fake BC that rejects a filter literal that
   does not fit its field; they fail without the fix.

## Touches

`src/cmd/business-central-consumer` (`service.go`, `pictures.go`,
`cursor_field_change_test.go`), `docs/connectors/business-central.md`,
`plans/backlog.md` (strike 144). The checkpoint is read and written through
`pkg/checkpoint` (in-memory store in tests); no Postgres schema change, no
migration (the field lives in the existing `state` JSON).

## Risks

- First deploy: every incremental BC node writes one extra checkpoint save
  (the field stamp). No messages are sent.
- A real config change re-sends the whole entity once. That is the point, and
  it is logged at Warn.
- Rollback: an older binary ignores the `cursor_field` key in `state`.

## Verification

`go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race` on `./cmd/business-central-consumer/`, `make lint-tenant`.
The new tests were run with the change check disabled and fail (see the PR).
