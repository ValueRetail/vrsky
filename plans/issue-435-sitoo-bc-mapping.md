# Sitoo ⇄ Business Central templates (#435)

Bring back the two retail onboarding templates that `plans/template-fields.md`
took out of the gallery, this time with a data-converter node and a real
field mapping between them. Nothing here has run against the live Sitoo or
Business Central APIs; both templates ship marked beta until Ludvik's live
test (end of this file) passes.

## Open questions for Ludvik

None of these can be settled from the repo. The templates make the call
written after each one; the live test is what confirms or refutes it.

1. **Is the Sitoo fixture a real transaction?** The only Sitoo payload in the
   repo is `src/test/fixtures/sitoo/transactions_page.json`
   (`transactionid`, `receiptnumber`, `datecreated`, `currency`, `total`,
   `totaltax`, `items[{itemid, sku, quantity, priceeach}]`). It reads as
   hand-written. If real Sitoo transactions name these fields differently
   (or POS sales live under `orders`, not `transactions`), the mapping table
   below changes and nothing else does. *Call: map the fixture's fields.*
2. **Sitoo SKU = BC item number?** Lines are matched on
   `lineObjectNumber` = Sitoo `sku`, stock on Sitoo `sku` = BC `number`. If
   the stores use EAN/GTIN instead, the mapping should use BC `gtin` and
   Sitoo's EAN field. Item variants are a harder case: a SKU like
   `VR-TSHIRT-M-BLK` is usually a BC item *variant*, which a line names with
   `itemVariantId` (a GUID the converter cannot look up), and
   `items.inventory` sums all variants. *Call: SKU = item number, no
   variants.*
3. **Prices include VAT.** In the fixture `priceeach` is gross (2 × 499.50 +
   250.00 = 1249.00 = `total`, `totaltax` is 25 % VAT of the net). BC
   `pricesIncludeTax` is read-only and comes from the customer, so the BC
   customer the template posts to must have *Prices Including VAT* on, or
   every line is taxed twice. The converter cannot divide by 1.25. *Call:
   send gross prices; say so in the wizard help.*
4. **Sales invoice, not sales order.** A POS sale is already paid and
   handed over, and dedupe needs to find the document after it is posted.
   BC API v2.0 `salesInvoices` lists drafts and posted invoices alike, so the
   dedupe GET on `externalDocumentNumber` keeps finding a receipt after
   someone posts it. A posted `salesOrder` leaves `salesOrders`, so with
   orders every posted receipt would be created again on the next poll
   (found in review). Lines go in the same POST (deep insert), which BC
   documents for sales documents. *Call: one draft `salesInvoices` POST per
   receipt with `salesInvoiceLines` inline. Check both in the live test.*
5. **How does Sitoo want absolute stock?** The producer POSTs to
   `accounts/{a}/sites/{s}/{resource}`. The template asks for the resource
   as `warehouses/<warehouse id>/warehouseitems` and sends
   `[{sku, decimaltotal}]`, up to 500 per request. Unknowns: whether Sitoo
   warehouses sit under the site or the account, whether the path needs a
   `.json` suffix, whether it wants `PUT`, the batch size limit, and whether
   it only takes relative changes through `warehousetransactions` (which
   BC's absolute `inventory` cannot feed without reading Sitoo first).
   *Call: POST to warehouseitems under the site.*
6. **BC location code.** The standard API v2.0 `items.inventory` is the total
   over all locations. A per-location level needs a custom API page or item
   ledger entries, which no connector reads. *Call: no location field; the
   template syncs the total and the wizard says so.*
7. **Returns and discounts.** Sitoo returns (negative quantities), discount
   lines and payment lines are mapped like any other line. An invoice is the
   wrong BC document for a return. *Call: out of scope; the live test should
   include one sale only.*
8. **Currency and posting date.** Neither is sent. BC takes the currency
   from the customer (sending the local currency code is an error in some
   BC versions) and the date from today's work date. The converter cannot
   cut `2026-09-09T08:14:22Z` down to `2026-09-09`. *Call: omit both.*
9. **Receipt numbers across stores.** Dedupe matches on the receipt number
   alone. If several Sitoo sites feed one BC company and their numbers can
   repeat, a receipt from one store is skipped as a "duplicate" of another
   store's. A `template` mapping `{siteid}-{receiptnumber}` would fix that
   (`externalDocumentNumber` is Code[35]). *Call: one site per pipeline, the
   receipt number as it is.*
10. **Both templates together.** A draft invoice does not lower BC
    `inventory`; posting it does. Until someone posts, the 15-minute stock
    push sends Sitoo the pre-sale level and undoes the sale there. *Call:
    document it; run them together only if invoices are posted promptly.*
11. **Scale.** The Sitoo poller re-reads every receipt on each poll (no
    cursor yet) and dedupe costs one BC GET per receipt per poll. A store
    with a few thousand receipts reaches BC's request limits within days.
    *Call: the POS template cannot leave beta before the Sitoo cursor exists
    (backlog item 62).*

## What the converter could not express, and what changed

The converter mapped the top-level fields of each record and nothing else.
Two things in this mapping were out of reach:

- **Nested lines.** A receipt's `items[]` must become `salesInvoiceLines[]`
  with every line's fields renamed. New mapping type **`each`**:
  `{type: "each", source, target, mappings: [...], drop_unmapped}` applies
  the nested mappings to every object of the `source` array and writes the
  new array to `target`. Non-objects are kept; a missing or non-array
  source is left alone.
- **One record per request.** Sitoo and BC consumers publish one page of
  records per message (a JSON array). BC's `POST salesInvoices` takes one
  object, and the BC producer's dedupe check requires one. New node flag
  **`split_records`**: when the result is an array, publish each element as
  its own message (`_split_index`, `_split_count` in metadata,
  `record_count` set to 1). All parts are rendered before any is published;
  offload and publish go part by part, so a failure part-way NAKs the input
  with the earlier parts already out. Redelivery sends every part again, so
  the destination must be idempotent: the BC template sets
  `dedupe_fields: ["externalDocumentNumber"]`. A part's envelope ID is
  derived from the input's ID, the node and the position, so a redelivery
  inside JetStream's dedup window is dropped by the stream itself. A node
  that only splits (no mappings, no format) is not a no-op. The streaming
  path (payloads over the 128 MB rehydrate cap) declines `split_records`
  with an error, like the formats it cannot stream.

Also fixed on the way: `to_string` printed a JSON number of a million as
`1e+06`. It now writes plain decimal (`1000000`), which matters for stock
levels. Existing configs that turn large or tiny floats into strings see
that change.

Both additions are opt-in; existing converter configs behave as before.

## The producers took the raw page too (found in review)

Every node of a pipeline publishes on the same data subject. The other
producers (kafka, http, db, file, sftp, salesforce, …) take a message only
when `_last_processed_by` names the node feeding them. The Sitoo and BC
producers never checked, so with a converter in front each would also have
written the source's unconverted page: BC items straight into Sitoo, a Sitoo
page into BC. Both now load the connection's `edges` with its nodes and apply
the same rule. A pipeline with the source wired straight in behaves as
before; a node with no incoming edge stored still takes everything.

## POS sales → ERP (Sitoo → Business Central)

`sitoo` consumer (`resource: transactions`, `poll_interval_seconds: 300`)
→ converter → `business_central` producer (`entity: salesInvoices`,
`method: POST`, `dedupe_fields: ["externalDocumentNumber"]`).

Converter: `drop_unmapped: true`, `split_records: true`. Input is one Sitoo
page (array of transactions); output is one salesInvoice object per
transaction.

| Sitoo (source) | BC salesInvoice (target) | Transform | Missing data |
|---|---|---|---|
| (none) | `customerNumber` | `static`, wizard value | Wizard blocks deploy until filled |
| `receiptnumber` | `externalDocumentNumber` | `rename` | Field absent: invoice has no external number; dedupe check is skipped with a warning, so a re-poll creates a duplicate |
| `items[]` | `salesInvoiceLines[]` | `each`, nested `drop_unmapped: true` | No `items`: invoice with no lines (BC accepts an empty draft) |
| (none) | `salesInvoiceLines[].lineType` | `static` `"Item"` | n/a |
| `items[].sku` | `salesInvoiceLines[].lineObjectNumber` | `rename` | No sku: line without item, BC rejects it (400, DLQ) |
| `items[].quantity` | `salesInvoiceLines[].quantity` | `to_number` | Absent or unparseable: no quantity, BC defaults to 0 |
| `items[].priceeach` | `salesInvoiceLines[].unitPrice` | `to_number` (`"499.50"` → `499.5`) | Absent or unparseable (`"499,50"`): BC uses the item's own price |
| `transactionid`, `datecreated`, `siteid`, `currency`, `total`, `totaltax`, `items[].itemid` | dropped | `drop_unmapped` | see questions 3 and 8 |

Fixture transaction 90210 becomes:

```json
{
  "customerNumber": "<wizard value>",
  "externalDocumentNumber": "S1-2026-000412",
  "salesInvoiceLines": [
    { "lineType": "Item", "lineObjectNumber": "VR-TSHIRT-M-BLK", "quantity": 2, "unitPrice": 499.5 },
    { "lineType": "Item", "lineObjectNumber": "VR-CAP-OS-RED", "quantity": 1, "unitPrice": 250 }
  ]
}
```

and the BC producer POSTs exactly that body to
`…/companies(<company id>)/salesInvoices`, after a GET filtered on
`externalDocumentNumber eq 'S1-2026-000412'` finds nothing.

Wizard values: Sitoo account number, site id (numbers), API id, API
password (secret); BC Entra tenant id, environment (default `Production`),
company id, client id, client secret (secret); BC customer number.

## ERP inventory → POS (Business Central → Sitoo)

`business_central` consumer (`entity: items`, `filter: type eq 'Inventory'`,
`page_size: 500`, `poll_interval_seconds: 900`, not incremental) → converter
→ `sitoo` producer (`resource: warehouses/<id>/warehouseitems`,
`method: POST`).

Not incremental on purpose: BC's `lastModifiedDateTime` on an item does not
change when stock moves (`inventory` is computed from ledger entries), so an
incremental poll would never send a stock change.

Converter: `drop_unmapped: true`, no split (Sitoo takes an array). Input is
one BC page of items; output is an array of Sitoo warehouse items.

| BC item (source) | Sitoo warehouse item (target) | Transform | Missing data |
|---|---|---|---|
| `number` | `sku` | `rename` | Absent: item without sku, Sitoo rejects the batch (400, DLQ) |
| `inventory` | `decimaltotal` | `to_string` (`12` → `"12"`, `1000000` → `"1000000"`) | Absent: item with no quantity |
| `id`, `displayName`, `unitPrice`, `gtin`, `type`, … | dropped | `drop_unmapped` | Prices are not synced |

Fixture items `{number: "1896-S", inventory: 12, …}` become
`[{"sku": "1896-S", "decimaltotal": "12"}, …]`, POSTed unchanged to
`…/accounts/<a>/sites/<s>/warehouses/<id>/warehouseitems`.

Wizard values: BC Entra tenant id, environment, company id, client id,
client secret; Sitoo account number, site id, API id, API password; Sitoo
resource with the warehouse id in it.

## Files

| File | Change |
|---|---|
| `src/cmd/data-converter/main.go` | `each` mapping, `split_records`, stable part IDs, `to_string` decimal |
| `src/cmd/data-converter/streaming.go` | decline `split_records` over the cap |
| `src/cmd/data-converter/template_mapping_test.go` | template mapping tests (independent author) |
| `src/cmd/sitoo-producer/service.go`, `src/cmd/business-central-producer/service.go` | take only the predecessor's output |
| `src/cmd/*-producer/route_test.go` | the gate, table-driven |
| `ui/src/onboarding/testdata/*.json` | each template's converter config, read by the Go tests (under `ui/` so the UI image build, which copies only `ui/src`, still finds it) |
| `ui/src/onboarding/templates.ts` | the two templates |
| `ui/src/types/nodeConfig.ts` | `CONNECTOR_KEYS` for sitoo and business_central, `split_records`, mapping types |
| `ui/src/onboarding/templates.test.ts` | template converter config equals the testdata; wizard values in converter mappings land on a static mapping's value; mapping types known |
| `docs/connectors/sitoo.md`, `business-central.md`, `filters-converters.md` | how to use the templates, the new mapping type and flag |
| `plans/template-fields.md`, `plans/backlog.md` | question 1 answered; backlog items 62 and 63 |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| Sitoo fixture through the real converter with the template config | one salesInvoice per transaction, lines mapped, numbers are numbers | drop `split_records`; rename `lineObjectNumber`; `to_number` → `rename` on `priceeach` |
| BC fixture through the converter | Sitoo items with `sku` and string `decimaltotal` | `to_string` → `rename` |
| `each` and `split_records` unit cases | missing source, non-array source, non-object elements, empty array, split-only node, stable part IDs | |
| producer gate, both producers | raw source page skipped, converter output written, direct pipelines unchanged | `accepts` always true |
| template converter config = testdata | the Go tests test what the wizard deploys | change a mapping in `templates.ts` only |
| `CONNECTOR_KEYS` guards | sitoo and BC keys are ones the connectors read; ids are numeric | mark `account_id` non-numeric |

## Verify

UI: `npx tsc -b`, lint, `npm run build`, `npx vitest run --coverage`.
Go: `go test -race` for the converter and the four Sitoo/BC packages,
`go vet`, `golangci-lint run`, `make lint-tenant`.
Live: the checklist in the PR, run by Ludvik on the BC trial and a Sitoo
sandbox.
