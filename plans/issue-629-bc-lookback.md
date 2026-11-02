# BC consumer: `lookback_days` for backdated records (#629)

Backlog item 143, found in review of #602.

## Problem

With a date `cursor_field` (`postingDate`) the consumer filters `ge W`, where W
is the newest date it has seen. A document posted today with last week's date
is below W and is never read, with nothing in the log.

## Decisions

- **Option `lookback_days`** in the `business_central` block of a consumer
  node (`BCConfig.LookbackDays`). 0 or unset = today's behaviour.
- **Range 0 to 365.** A negative value or one above 365 is an error:
  `startConnection` logs it and does not start the poller (next to
  `validatePictures`), and `fetchAndPublish` returns it, so a config the poller
  already holds fails the poll before any request. A year covers any posting
  correction a bookkeeper makes; a bigger window is a full read each poll, and
  the checkpoint grows with it. The management API (`nodeconfig.go`) only
  checks that the `business_central` block exists, so there is nothing to add
  there; the consumer is the only place BC options are validated.
- **Date fields only.** The cursor type is known only from the data, so the
  option cannot be refused for a DateTime field at config time. A DateTime
  field is `lastModifiedDateTime` and the like, which BC stamps when it writes
  the record, so nothing arrives with an older stamp; a window would read days
  of changes on every poll for nothing. The poll ignores the option for a
  DateTime watermark and logs one Warn per process. It is also ignored
  without `incremental`.
- **Filter.** The stored watermark W is kept as the floor of the tracker
  (`cursorTracker.seed`), and the request carries `ge W - N days`
  (`shiftDate`). The watermark never moves back: `seed` sets the tracker's
  newest date but not `raw`, so a window that finds nothing newer stores
  nothing and the stored one stays.
- **Remembered set.** The tracker keeps every date record that is on or after
  `newest - N` (`window`), and the poll merges it into the same map that
  already holds the future-dated records and the watermark day
  (`cfg.aheadSeen`, saved in the checkpoint `state`). `withoutSentAhead`
  already leaves out a record whose key and cursor value are in that map, so
  the backdated record is sent once and not on the next poll. An edit that
  changes the date is a new version and is sent again.
- **Pruning.** The map is rebuilt from each poll's feed (as before), so a
  record below `newest - N`, or deleted in BC, is not carried over. During a
  poll the tracker prunes when the window has grown to 1,024 entries and again
  when it has doubled, so a first read of a large entity, oldest to newest,
  holds memory in proportion to the window and the work stays linear.
- **Size.** Bounded by the records the window holds, which every poll reads
  anyway. About 55 bytes an entry in the checkpoint JSON (a GUID and a date):
  10,000 records are about 550 KB. No hard cap: dropping entries would send
  them again on every poll, which is worse. The existing Warn at 1,000
  remembered records now also names `lookback_days`.
- **Changing the setting needs no reset.** Lowering it prunes on the next poll
  and sends nothing. Raising it reads the extra days; those records were not
  remembered, so ones already sent go out once more (at-least-once, the safe
  direction, and the same trade as the rest of the cursor code). Documented.
- **#610 stays as is.** A changed `cursor_field` still drops the watermark and
  the remembered set; a first poll has no watermark and reads everything.
- **UI.** The builder's BC form lists the cursor options
  (`PropertyEditor.tsx`, `CONNECTOR_KEYS` in `types/nodeConfig.ts`), which
  also decides which keys survive, so `lookback_days` is added there as a
  numeric key and a field under "Cursor field", shown with `incremental`.

## Acceptance criteria

1. With `lookback_days: 7` and `postingDate`, a record dated inside
   `[W-7, W]` that appears after the watermark moved is published once; the
   next poll does not publish it again.
2. A record dated before `W-7` is not read.
3. `lookback_days: 0` or unset: filter and behaviour unchanged.
4. The remembered set holds only records dated from `newest - N`; it stays
   bounded over a long run and during a large first read.
5. The watermark does not move back when the window finds only older records.
6. A value below 0 or above 365 is rejected, at start and in a fetch.
7. A DateTime `cursor_field` ignores the option and logs one Warn.
8. A restart keeps the window (checkpoint); lowering, raising and clearing
   the setting need no reset.
9. Backlog item 143 struck; the option is in `docs/connectors/business-central.md`.

## Files

- `src/cmd/business-central-consumer/service.go` (config field, validation,
  tracker window, filter shift)
- `src/cmd/business-central-consumer/cursor_lookback_test.go` (new)
- `ui/src/components/Pipeline/PropertyEditor.tsx`, `ui/src/types/nodeConfig.ts`
- `docs/connectors/business-central.md`, `plans/backlog.md`

## Risks

- Raising the setting re-sends records once (documented).
- A big window makes every poll read the whole window and the checkpoint
  large; the Warn and the 365 cap are the guards.
- Rows saved before this change have no window; they behave as `lookback_days`
  0 until a poll with the option on rebuilds the set.

## How we verify

- Table-driven tests in `cursor_lookback_test.go` against the date fake BC
  of `cursor_date_test.go`. They fail with `effectiveLookback` forced to 0.
- From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race ./cmd/business-central-consumer/`, `make lint-tenant`.
- From `ui/`: `npx tsc -b`, lint, `npm run build`, `npx vitest run --coverage`.
