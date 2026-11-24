# #880: the till's day close in the Bifrost sales template

Issue: ValueRetail/vrsky#880. Bifrost's Basket publishes a till's day close
(Z report) as `basket.day-closed` (ValueRetail/Bifrost#538, sample
`basket/docs/events/basket-day-closed.v1.json`, Bifrost main at d8c8d44).
Retailhero takes it on the same source-batch route as sales and returns, on
its own journal `<series>-Z` (ValueRetail/Retailhero#3535).

## Acceptance criteria

- The day-close sample becomes one `BIFROST-BASKET-v1` batch on journal
  `T07-Z` (the sales journal `T07` plus `-Z`), with one entry:
  `operationId` `bifrost-day-close-<event_id>`, `journalPosition` the
  decimal string of `data.z_number`, `event` the event unchanged.
- A day close without `data.z_number` is not sent: retried, then
  dead-lettered with the field named, as the sales branch does for
  `journal_sequence`.
- Sales and returns map exactly as before: the sales filter and converter
  and their testdata do not change.
- Other event types reach neither branch; a day close does not reach the
  sales branch and a sale or return does not reach the day-close branch.
- The wizard asks for the journal ID, URL and credential once, as today.
- Tests, fixtures, template testdata and the connector doc are updated.

## Design

### A second branch

```text
src ─┬─► flt   ─► conv   ─► dst     sales and returns (unchanged)
     └─► flt-z ─► conv-z ─► dst-z   day close
```

Every filter, converter and producer node takes the first edge into it as
its one predecessor (`_last_processed_by`), so two converters cannot feed
one producer. Fan-out from the consumer is supported: the filter runs every
entry whose predecessor is the consumer. So the day close gets its own
filter, converter and HTTP producer. `dst-z` has the same config as `dst`:
same URL, same bearer.

- `flt-z`: `event_type` equals `basket.day-closed`, `source.kind` equals
  `pos`, `event_id` not empty.
- `conv-z`: the same wrapper as the sales converter. `journalId` is a static
  value the wizard fills. `operationId` is a `template`
  `bifrost-day-close-{event_id}`: there is one event type on this branch, so
  the sales branch's `lookup` by type is not needed, and the filter already
  refuses an event without `event_id`. `journalPosition` is `to_string` of
  `data.z_number`, `required`.

### The journal ID: one wizard field, written twice

A wizard field writes one place (`nodeId`, `objectPath`, `key`). The
converter cannot build `T07-Z` itself: `template` and `lookup` read the
input record, and the journal ID is not in the event.

So `TemplateField` gets `copies`: more places the wizard writes the same
value, each with an optional `suffix` appended to a non-empty value. The
journal field copies into `conv-z`'s `mappings.3` with suffix `-Z`; the URL
and credential fields copy into `dst-z`. No new wizard field. The template
tests treat a copy like a field (the node exists, the key is one the
connector reads, a converter value lands on a static mapping, a secret
ships empty), and a suffix is refused on a secret or numeric field.

The credential copied into `dst-z` is minted as its own stored secret on
deploy, the way every secret field is (`materializeSecrets` per node). Two
secrets with the same value; rotating the credential in the builder later
means changing it on both destinations. Sharing one secret id would need a
post-deploy step in the wizard; not worth it for one template.

After deploy the two journal values are separate static mappings. Changing
the sales journal in the builder does not change the day-close one.

## Files

- `ui/src/onboarding/templates.ts` (`TemplateField.copies`, the template)
- `ui/src/onboarding/OnboardingWizard.tsx` (write copies)
- `ui/src/onboarding/templates.test.ts`, `OnboardingWizard.test.tsx`
- `ui/src/onboarding/testdata/bifrost-day-close-filter.json`,
  `bifrost-day-close-to-retailhero.json` (new)
- `src/test/fixtures/bifrost/basket-day-closed.v1.json` (copied verbatim from
  Bifrost), `intake-day-close-batch.json` (golden)
- `src/cmd/data-filter/bifrost_sales_test.go`,
  `src/cmd/data-converter/bifrost_sales_test.go`,
  `src/cmd/http-producer/bifrost_sales_test.go`
- `docs/connectors/bifrost-sales-to-retailhero.md`

No Go code changes: the filter, converter and producer already do all of it.

## Verification

- `go test -race` for `cmd/data-filter`, `cmd/data-converter`,
  `cmd/http-producer`; `go vet`, `gofmt -l`, `golangci-lint run` on them.
- UI: `npx tsc -b`, `npm run lint`, `npm run build`,
  `npx vitest run --coverage`.
- `mkdocs build --strict` if mkdocs is available.
