# Ordered resume for Business Central incremental polls (#733)

## Open questions

None. The live check on 2026-11-03 (issue comment) showed that BC keeps its
place across 40 server pages with `$orderby=lastModifiedDateTime asc`, with
no duplicate ids and about the same cost as key order.

## Goal

An incremental poll that stops early (the page cap from #699, a next link
off the API from #686/#690, a failed page) stores how far it got, so the
next poll continues there instead of re-reading the same pages. Nothing is
skipped and nothing is sent twice that the remembered set can keep out.

## Acceptance criteria

- Every incremental poll asks for `$orderby=<cursor_field> asc`. A
  non-incremental poll and the sample-data preview do not.
- A stopped incremental poll stores a watermark from the pages it handled in
  full (records published, pictures published):
  - DateTime cursor (`gt`): the largest value read that is strictly below the
    last value read. Records on the last value may lie on pages not read;
    they come back on the next poll. The ones already sent are remembered so
    they are not sent twice.
  - Date cursor (`ge`, #602, with or without `lookback_days`, #629): the
    newest date read, as after a full poll, with the records on that date
    and the lookback window remembered. `ge` reads the last date again, so
    the unread records on it come back.
  - Remembered records the stopped poll did not get back to (cursor value at
    or after the last value read) are kept, so future-dated records (#591)
    are not sent again.
  - Nothing read, or only one value: the stored watermark stays.
- The node checks the feed really is ascending. A value lower than the one
  before it means BC ignored the order: the stopped poll stores nothing, as
  before, and logs a Warn.
- BC answering 400 to the first page of an ordered request, when the same
  request without `$orderby` succeeds: the node logs a Warn, polls without
  `$orderby` from then on (until a restart), and a stopped poll does not
  advance, as before.
- A resend (all records again) that stops early stores the same kind of
  watermark and ends the resend; the next poll sends the rest. A recovery
  read past a frozen pre-#591 watermark that stops early stores nothing, as
  before (its "re-saved" check needs the end of the feed).
- The page-cap Warn says whether the poll advanced. Docs updated.

## Affected files

- `src/cmd/business-central-consumer/service.go`
- `src/cmd/business-central-consumer/pagecap_test.go` (incremental cases now
  advance), new `ordered_resume_test.go`
- `docs/connectors/business-central.md`
- `plans/backlog.md` (item 175)

## Approach

- `pollURL(cursor, ordered)` adds `$orderby` beside `$filter`; `entityURL`
  stays as the unordered URL for the preview.
- The tracker observes a page after the page is handled, so a page that
  failed to publish is not counted. It also keeps the last value read, the
  watermark candidate below it, the records on the last value, and whether
  the feed went backwards.
- The paging loop breaks with the stop reason instead of returning; a
  stopped incremental poll goes through `saveStopped`, which stores the
  watermark and the remembered set with the existing `saveProgress` (retry,
  pending save after a cursor_field change) and returns the stop reason.
- `get` returns a `statusError` so a 400 can be told apart; its text is
  unchanged.

## Risks

- A custom `filter` with `$orderby`: independent query options, so the
  filter is unchanged. BC sorts the filtered set.
- A cursor field BC cannot sort on: covered by the 400 fallback. A BC that
  accepts the option but returns key order is caught by the order check only
  when the disorder falls inside the pages read; otherwise older records on
  unread pages are skipped. The live check showed BC honours the order;
  documented as a limit.
- A date cursor stuck inside one day (more records on it, or on today and
  later, than a poll reads) "resumes" from the date it started on. That is
  logged as not advanced (review finding).
- A record modified while a poll pages gets a later value and comes at the
  end of the ordered feed, or on the next poll; it is not skipped.
- More than a page cap of records on a single value (one date, or one
  timestamp) still cannot be passed; nor can a lookback window larger than
  the cap, because each poll starts at W - N. Documented.
- A stop now saves a checkpoint, also when the poll was cancelled by a stop
  command (the save is retried on an uncancelled context, as after a full
  poll).
- Cost of `$orderby` on large tables: within noise on the trial; to watch on
  a real tenant.

## How we verify

- A fake BC that honours `$filter` (`gt`/`ge`), `$orderby` and pages with
  next links. Polls are repeated until one completes, and every record must
  arrive exactly once: DateTime and Date cursors, ties across the stop
  point, lookback, future-dated records, a failed page, an off-origin link,
  a resend. Non-incremental polls carry no `$orderby` and store nothing. A
  fake that ignores `$orderby` and one that rejects it.
- The new tests fail on main; mutation runs on "strictly below the last
  value", the carry-over of remembered records and observe-after-publish.
- `go build`, `go vet`, `gofmt`, `golangci-lint`, `go test -race`,
  `make lint-connector`, `make lint-tenant`.

## Review of #755 (second round)

- Tests pin the stopped-recovery guard and the "resend with nothing to
  resume from stays a resend" guard.
- The remembered set keeps the latest observation of a record: the
  future-dated set is merged last, on the full and the stopped path.
- The 400 fallback was already limited to 400; a test with 503 and 429 now
  pins it.
- The fake pages by sort key, as BC does, and a randomized test (300 seeds:
  edits while paging, restarts, failed pages) checks exactly-once delivery.
