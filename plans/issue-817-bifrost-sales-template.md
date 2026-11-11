# #817: template, Bifrost Basket sales to Retailhero POS reconciliation

## Open questions

Each is also in `docs/connectors/bifrost-sales-to-retailhero.md`. None is
guessed in the template.

1. Auth. Retailhero wants a tenant-workload bearer credential
   (Retailhero#2675). The HTTP producer sends a bearer only from an OAuth
   grant. The template ships without credentials. Needs a stored-bearer option
   on the producer (#850; secrets handling, human review).
2. Journal position. Required per entry, absent from the events. Not sent
   (#851).
3. Where the operation ID sits in the intake body, and the route. Retailhero
   #2678 is open. Assumed: `entries[].operationId`.
4. Whether the stream body is the bare Basket event or the VRSky transport
   envelope (Bifrost ADR 0002), and the stream name and subjects.
5. Revisions: every sale is revision 1 until Bifrost publishes amend/void.
6. Returns: no `return.completed` event yet (#852).
7. The "fields Bifrost events do not carry" list in the Retailhero profile
   (stock location, currency scale, ex-VAT price and rounding, quantity unit,
   sale instant, cancel-after-sale).

## Goal

A pipeline template that takes Bifrost till sales off the tenant's JetStream
and posts them to Retailhero's C3 intake, once each, however often a delivery
repeats.

## Acceptance criteria

1. Template `bifrost-sales-to-retailhero` in `ui/src/onboarding/templates.ts`:
   nats source, filter, converter, http producer, with wizard fields for the
   server, user, password, stream, durable, journal ID and intake URL.
2. The operation ID is a pure function of the Basket event ID (and revision
   1): the same event always gives the same ID, another event another.
3. Test from a Basket fixture to a stubbed Retailhero intake: converter output
   equals one golden batch, and the producer sends that batch unchanged.
4. Mapping doc, and how it sits beside the till to BC webhook.

## Decisions made without asking

- **No line mapping in VRSky.** The issue says to map lines with `each`. The
  Retailhero profile (BIFROST-BASKET-v1, Proposed) takes the raw Bifrost event
  in the batch and does the mapping itself, including exact money
  (`quantity x unit_price x vat`, refusing a non-whole minor unit). The
  converter has only float `to_number`, so mapping lines here would round
  money. The converter wraps the event instead. `each` is used for the wrapper
  (`entries`), not for lines.
- **Converter change: source `.`.** There was no way to put the whole record
  inside a wrapper. `getNestedField(obj, ".")` now returns the record, and
  `each` with source `.` treats it as a one-element array. Both unreachable
  before (a source of `.` matched nothing). Documented in
  `docs/connectors/filters-converters.md`, tested in
  `cmd/data-converter/bifrost_sales_test.go`.
- **Filter node.** The profile refuses a whole batch on `basket.created`,
  `basket.priced` and the rest, and `source-not-pos` on a web sale-recorded.
  A filter keeps `basket.sale-recorded` from `source.kind = pos`.
  `basket.completed` and `basket.cancelled` are not sent: they map to no fact.
- **Operation ID** `bifrost-sale-<event_id>-r1`, as an `operationId` on the
  entry. 49 bytes, under the 128-byte identifier limit.
- **`journalPosition` omitted** rather than invented. The real profile will
  refuse these batches until it is settled; the template is marked beta.
- **No Authorization header.** A header in `http.headers` is stored in plain
  text. See open question 1.
- **Wizard `asList` field flag** so a single URL becomes `servers: [url]`.
- **`deliver_policy: all`**, safe because of the operation ID.
- **Password auth** (`user_password`), not `.creds`: the wizard's masked input
  is one line and a `.creds` file is not.
- `CONNECTOR_KEYS` gets `consumer/nats` and `filter` entries (the template
  tests refuse a node whose connector has none).

## Files

- `ui/src/onboarding/templates.ts`, `OnboardingWizard.tsx`, `templates.test.ts`,
  `OnboardingWizard.test.tsx`, `testdata/bifrost-sales-*.json`
- `ui/src/types/nodeConfig.ts`
- `src/cmd/data-converter/main.go` and `bifrost_sales_test.go`
- `src/cmd/data-filter/bifrost_sales_test.go`
- `src/cmd/http-producer/bifrost_sales_test.go`
- `src/test/fixtures/bifrost/` (three Basket events copied from
  ValueRetail/Bifrost `basket/docs/events/`, one golden batch)
- `docs/connectors/bifrost-sales-to-retailhero.md`, `filters-converters.md`,
  `mkdocs.yml`; `plans/backlog.md`

## Risks

- The intake contract can still change; the batch members come from the
  Proposed profile.
- The new `.` source applies to every mapping type that reads `source`
  (to_string of a record would stringify the map). Only `copy` and `each` are
  documented.

## Verification

Go: `go build`, `go vet`, `golangci-lint`, `go test -race` on data-converter,
data-filter, http-producer; `make lint-tenant`, `make lint-connector`. UI:
`npx tsc -b`, lint, `npm run build`, `npx vitest run --coverage`. Mutations:
break the `.` branch, the operation ID expression and the filter's `pos` rule;
each fails a test.
