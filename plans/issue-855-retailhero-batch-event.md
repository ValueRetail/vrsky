# #855: the Retailhero to Bifrost template reads Retailhero's batch event

Issue: ValueRetail/vrsky#855. Retailhero PR #2746 (Retailhero#2744, open)
publishes each Bifrost catalogue batch as an event. Read from that PR's diff,
`docs/contracts/pos-publication-bifrost-catalogue.md` ("Transport to VRsky")
and `tests/fixtures/pos-publication/bifrost-catalogue/v1/event-batch.json`.

## What Retailhero sends

- Subject `rh.evt.v1.<tenant>.pos_publication.bifrost-catalogue.batch`,
  stream `RH_EVT_V1_POS_PUBLICATION`, durable
  `vrsky_bifrost-catalogue-batch_<tenant>`, `Nats-Msg-Id` the event id.
- Body: the usual envelope plus `fact` with `kind`, `effectiveFrom`,
  `rows[] = {sourceItemId, importItem}`, `sequence` (whole number from 1,
  +1 per batch per tenant and store, across publications), `batch`,
  `batchCount`, `publicationId`, `targetId`, `storeId`, `profile`,
  `profileVersion`.
- `recordedAt` is shared by every batch of a publication, so it can't be the
  sequence.
- Retailhero asks VRSky to handle the durable one message at a time.

## Acceptance criteria

- The converter maps `fact.rows[].importItem` field for field (the wire
  `ImportItem`: article_no, name, price_incl_vat, vat_code, unit, alcohol,
  effective_from, and gtin, category, min_age when set) and `fact.sequence`
  to `sequence`. `recordedAt` is not used.
- An event without `fact.sequence` is not sent unnumbered: it fails, is
  retried and dead-lettered.
- The source binds Retailhero's durable as a partner account (#859) and
  holds one message at a time (`batch_size: 1`).
- Several events from one publication commit as separate deltas; nothing
  treats a batch as the whole list.
- A batch retried after the next one committed is dropped (409), not retried.
- The template doc says how to run it locally (ALLOW_PRIVATE_UPSTREAMS on
  management-api, nats-consumer, bifrost-catalogue-producer), that this is
  for local testing only, and that production reachability is open in #843.

## Files

- `ui/src/onboarding/templates.ts`, `ui/src/onboarding/testdata/retailhero-to-bifrost.json`,
  `ui/src/onboarding/templates.test.ts`, `ui/src/types/nodeConfig.ts`
  (the NATS source keys from #859, so the template may set them)
- `src/test/fixtures/retailhero/*` (events instead of bare batches)
- `src/cmd/data-converter/retailhero_template_test.go`,
  `src/cmd/bifrost-catalogue-producer/retailhero_template_test.go`
- `docs/connectors/retailhero-to-bifrost.md`, `plans/backlog.md`

No Go code outside tests changes. The producer already takes a whole-number
`sequence` and drops a 409 (#861).

## Decisions

- `fact.sequence` is `required` in the converter. Without it the import would
  go unnumbered and a late retry could put an older price back; failing it
  puts it in the DLQ with the field named.
- The wizard now sets up a Retailhero partner account (NKey seed, `JS.RH.API`,
  inbox prefix, `create_durable: false`), since #859 merged. The CA stays a
  builder setting: the wizard's fields are one line.
- `deliver_policy` is gone from the template: it does nothing for a durable
  VRSky only binds.
- Order inside VRSky is not fully solved here. `batch_size: 1` covers the
  source. A retry in the converter or producer, the converter's two pods,
  and a rolling deploy can still let batch n + 1 commit before n, which then
  gets 409 and is lost. A batch past Retailhero's maxDeliver leaves the
  durable with no VRSky log. That needs platform work (backlog 224) or
  maxAckPending 1 on Retailhero's durable; the doc says so.
- One store per Retailhero tenant: the durable carries every store and the
  pipeline has one Bifrost destination (backlog 225).
- The fixtures are now events. `bifrost-batch-full-1.json` is Retailhero's
  sample event unchanged; `bifrost-batch-delta.json` puts the profile's delta
  batch in the same envelope with sequence 4.

## Verification

Go: build, vet, gofmt, golangci-lint, `go test -race` for the touched
packages and the whole module, lint-tenant. UI: tsc -b, lint, build, vitest
with coverage. Docs: mkdocs build --strict.
