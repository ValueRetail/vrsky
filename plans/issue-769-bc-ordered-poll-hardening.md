# #769: Business Central ordered polls, confirm the order once, and the stop/start race

Follow-up to #733 / PR #755 (`plans/issue-733-bc-ordered-resume.md`).

## Goal

1. A stopped incremental poll stores how far it got only when BC is known to
   honour `$orderby` on the cursor field. Today the only check is the pages the
   poll read, so a BC that ignored the order but happened to return those pages
   ascending would get a watermark that skips older records on pages never read.
2. A fast stop+start must not let the old poller's last checkpoint save land
   after the new poller has started from (and saved) its own.

## Acceptance

- A probe, once per process per tenant, company, entity and cursor field:
  `$top=1` with `$orderby=<field> asc` and `$orderby=<field> desc` (the
  configured filter, no cursor clause). The order is confirmed when the first
  value ascending is strictly below the first value descending, and the last
  value the poll has read lies between them. Otherwise the node polls without
  `$orderby` from then on and logs a Warn. The result is cached in memory.
- A fake BC that ignores `$orderby` is detected and the node falls back to
  unordered polling; a stopped poll then stores no watermark.
- A fake BC that honours `$orderby` stays ordered, and the probe runs once.
- A stop followed by a start: the new poller does not read the checkpoint until
  the old one has exited (bounded wait), so the old save cannot overwrite the
  new one. Deterministic test, no long sleeps, passes under `-race`.
- Mutation check on each fix: break it, see the test fail, restore.
- `docs/connectors/business-central.md` describes the probe and the wait.

## Decisions

- **When to probe.** Only when an ordered poll's first page carries a next
  link, and before following it. A stopped poll can only store a resume point
  after a fully handled page followed by another request, so single-page polls
  never need the order and never pay for the probe. Two `$top=1` requests per
  key per process otherwise.
- **The bracket.** Equal first values ascending and descending fail the check:
  a server that ignores `$orderby` returns the same first record both ways, and
  an entity whose records all share one value has nothing to resume past
  anyway. The poll-side check uses the last (largest) value read so far, not
  the first: a record edited between page 1 and the probe moves up, which can
  lift the probed minimum above page 1's first value but not above its last.
- **Inconclusive probe** (a failed request other than 400, an empty answer, an
  unreadable value): nothing is cached, this poll is treated as unordered for
  a stop (stores nothing, as before #733), and the next multi-page poll probes
  again. A 400 to either probe is a "no" and is cached, like the 400 to the
  ordered first page.
- **Disorder already seen on page 1** is a "no" without probing.
- **Cache key** includes the tenant, so no verdict crosses tenants even for the
  same company URL.
- **The wait.** The stop moves the cancelled poller to a `stopping` map. The
  next start's poller goroutine waits for it to exit before its first poll,
  so the NATS command goroutine is not blocked at all. The wait is bounded by
  `2 × saveTimeout + 5s` (a cancelled poll's save gets a second attempt on an
  uncancelled context, see `saveProgress`); past it the new poller starts
  anyway with a Warn, which costs duplicates at worst, as before. A poller
  stopped while it waits still waits out its predecessor before it counts as
  exited, so a stop, start, stop, start chain cannot skip one.

## Files

- `src/cmd/business-central-consumer/service.go`: probe, cache, `poller.done`,
  `stopping` map, the wait.
- `src/cmd/business-central-consumer/ordered_resume_test.go`: the fake handles
  `$top` probes and `desc`; tests for both fixes; the "ignores $orderby" case
  now expects the fallback.
- `src/cmd/business-central-consumer/stop_start_test.go` (new): the race.
- `docs/connectors/business-central.md`.

## Risks

- A false "no" (for example a single-value entity at probe time) turns ordered
  resume off until restart. That is the pre-#733 behaviour, with a Warn.
- The probe adds two requests on the first multi-page poll per process.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race -count=1 ./cmd/business-central-consumer/ ./pkg/sdk/...`. From
the root: `make lint-tenant`, `make lint-connector`.

## Left for a human

`postingDate` on `generalLedgerEntries` has not been checked against live BC
(the 2026-11-03 check covered `lastModifiedDateTime` only). Needs credentials;
for Ludvik on the next live check.
