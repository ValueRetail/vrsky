# #851 and #852: journal position and returns in the Bifrost sales template

Issues: ValueRetail/vrsky#851 (journalPosition), #852 (returns). Template from
#817 (PR #849). Contract: pinned for the Retailhero ↔ VRSky ↔ Bifrost path on
2026-11-11; Bifrost's samples on its branch `basket-sales-to-retailhero`
(ValueRetail/Bifrost#274), at 25e9c20.

## Acceptance criteria

- `entries[].journalPosition` = `data.journal_sequence` as a decimal string.
  An event without it is not sent: it is dead-lettered with a reason.
- The filter passes `basket.sale-recorded` and `basket.return-recorded` with
  `source.kind = pos` (and an `event_id`, as before).
- `operationId` is `bifrost-sale-<event_id>-r1` or
  `bifrost-return-<event_id>-r1`.
- Fixtures and goldens from Bifrost's samples (`operator` left out; neither
  sample has it now).
- The doc's open questions: 2, 5 and 6 settled, 1 by #850.

## Design (decisions)

The converter had no way to pick a text by a field's value, and no way to
refuse a record. Two additive, opt-in mapping features, so existing configs
behave as before:

- `lookup` mapping type: `values` maps the source's value to a text, filled
  in like a `template` expression. Used for the operation ID by event type.
  Two filter/converter/producer branches would have needed the intake URL,
  journal ID and credential twice in the wizard.
- `required: true` on a mapping that reads one field: a record whose source is
  missing, null or "" (or a lookup value with no entry) is not converted. The
  converter returns an error, so the message is NAKed, retried on the usual
  backoff and dead-lettered with the field in `X-Vrsky-Last-Error`. That is
  the only route to the DLQ the platform has (no "dead-letter now"); the
  retries cost about 2.5 minutes per such message. The DLQ copy is the
  original event; replaying it fails again, so Bifrost has to send the event
  again with the field (or it is discarded from the DLQ). The
  streamed path (over the rehydrate cap) does the same.
- Filter operator `in` (comma-separated list, exact match per entry). The
  filter's rules are flat and/or, so "sale or return, and from a POS" needed
  it. `starts_with`/`ends_with` would have let any future `basket.*-recorded`
  type through, which Retailhero refuses a whole batch on.
- The editor knows the new type and operator; the guard tests keep the UI
  lists in step with Go. The Required box shows on lookup rows, and on other
  rows once set, so existing rows keep their controls.
- `journal_sequence` is written with `to_string`, which formats a JSON number
  in plain decimal (`42`). A sequence above 2^53 would lose precision through
  float64; a till counter does not get there.

## Independence from #850

This PR does not touch the http producer code or the destination node. The
doc's "Open questions" is rewritten here; #850 changes "Setting it up". They
can merge in either order; the second may need a merge of main.

## Files

- `src/cmd/data-converter/{main,streaming}.go`, `bifrost_sales_test.go`
- `src/cmd/data-filter/main.go`, `bifrost_sales_test.go`
- `src/cmd/http-producer/bifrost_sales_test.go` (return batch)
- `src/test/fixtures/bifrost/`: `basket-sale-recorded.v1.json` (updated),
  `basket-return-recorded.v1.json`, `intake-sale-batch.json` (updated),
  `intake-return-batch.json`
- `ui/src/onboarding/{templates.ts,templates.test.ts}`, testdata JSON,
  `ui/src/types/nodeConfig{,.test}.ts`,
  `ui/src/components/Pipeline/{ConverterMappingsEditor,PropertyEditor}.tsx`,
  `ConverterMappingsEditor.test.tsx`
- `docs/connectors/{bifrost-sales-to-retailhero,filters-converters}.md`,
  `docs/operator/troubleshooting.md`

## Verification

Go and UI checks as CI runs them. Mutation runs: required check off in the
buffered path, in the streamed path, and the `in` operator's empty-entry
guard; each made its test fail.

## Review round 1 (fresh reviewer, changes requested), fixed

- The lookup values textarea lost new lines while typing: it now keeps the
  typed text and parses it on each change.
- `required` is honoured only on rename, copy, to_string, to_number and
  lookup (`requiredApplies`), and the Required box shows on any row where it
  is set, so a flag left after a type change can be cleared.
- A required-field failure now fails the message before any branch
  publishes, so a sibling branch does not publish again on every retry.
- The Test Transform preview reports a missing required field.
- The streamed path emits the error event and names the record.
- `checkRequired` reads "." as the record only for copy, like the mappings.
- A Go guard keeps the UI's filter operators in step with `evaluateRule`
  (an unknown operator passes every message).
- Docs: a DLQ replay of an event without the field fails again; pipelines
  deployed earlier keep the old nodes; values are not range-checked.
- Not changed: a data-filter pod on an old build during a rollout would pass
  `in` rules as unknown. All workers roll out from the same merge.
