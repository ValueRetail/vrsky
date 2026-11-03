# Page cap for SAP S/4HANA and Business Central polls (#699)

## Open questions

None.

## Goal

A gateway that keeps returning next links cannot hold a poll forever. Both
consumers stop a poll after 10 000 pages.

## Acceptance criteria (from the issue)

- A per-poll page cap (constant, 10 000) in both consumers. When hit, the
  poll stops with a Warn log naming the connection and the cap.
- Pages already fetched stay published.
- Business Central: the watermark is not advanced by a poll that hit the cap.
- Tests with a fake that always returns a next link: the poll stops at a
  small cap set through a test hook, and nothing hangs.
- One line on each connector page.

## Affected files

- `src/cmd/sap-s4hana-consumer/service.go`, `pagecap_test.go`
- `src/cmd/business-central-consumer/service.go`, `pagecap_test.go`
- `docs/connectors/sap-s4hana.md`, `docs/connectors/business-central.md`
- `plans/backlog.md` (item 171)

Not touched: `src/cmd/sap-s4hana-producer`, `src/pkg/managementapi`.

## Approach

- `maxPagesPerPoll = 10000` constant and a package-level `pageCap` var
  (initialised from it) that tests lower.
- At the top of the page loop in `fetchAndPublish`, `if page >= pageCap`:
  log a Warn (the poller's logger already carries `connection_id`; the
  message also has `page_cap`) and return a new `errPageCap`.
- Business Central: returning an error leaves before the watermark is saved,
  which is what a poll stopped by an off-origin next link (#690) does. The
  next poll reads the same window again. Advancing to the watermark of the
  pages read is not safe: the code does not assume the feed is ordered by
  the cursor field, and a cursor past records not yet read would skip them.
- `runPoller` does not log `errPageCap` again as an Error; the Warn is the
  log line.
- SAP has no watermark; the stopped poll just ends.

## Risks

- A feed with more than 10 000 pages makes a Business Central incremental
  poll re-read the same 10 000 pages every time and never advance. Visible
  in the Warn each poll. Fixing that needs an ordered feed, which is out of
  scope; the cap turns a hang into a repeated, logged stop.
- `pageCap` is a package variable, so the tests that lower it do not run in
  parallel.

## How we verify

- Table-driven tests with a never-ending fake: the poll ends with
  `errPageCap` after exactly `cap` requests, `cap` published pages, a Warn
  with the connection and cap, and (Business Central) the stored watermark
  unchanged or absent. A poll whose last page is page `cap` is not capped.
- Without the cap check the tests fail on the 10 s context timeout.
- `go build`, `go vet`, `gofmt`, `golangci-lint`, `go test -race`,
  `make lint-tenant`.
