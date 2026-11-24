# Bifrost sales → Retailhero (template)

The onboarding template **POS sales → Retailhero (Bifrost Basket → Retailhero)**
sends each sale and return a Bifrost till records, and each day close (Z
report), to Retailhero's POS reconciliation (C3), so Retailhero can reconcile
what the tills sold against its own stock.
It is a beta: the Retailhero side is a Proposed contract and its HTTP intake
is not built yet (see [what is still open](#open-questions)).

```text
Basket ──► VRSKY_DATA ─┬─► filter ──► converter ──► HTTP producer ──┬─► Retailhero C3 intake
        (direct source) │   till sales  batch of one    POST JSON      │
                        └─► filter ──► converter ──► HTTP producer ──┘
                            day close   batch on <journal>-Z
```

## How it sits beside till → Business Central

Bifrost's `docs/sales-to-bc.md` describes a temporary road: the till posts each
sale straight to a VRSky webhook, which creates a draft sales invoice in
Business Central. This template is a different road for a different purpose.

| | till → BC (temporary) | this template |
|---|---|---|
| Starts at | the till's outbox, over a signed webhook | Basket's `basket.sale-recorded`, `basket.return-recorded` and `basket.day-closed` events, which Basket publishes into the pipeline |
| Ends at | a draft sales invoice in Business Central | a staged sale in Retailhero C3 |
| Purpose | accounting copy of the sale | stock and sales reconciliation |
| Retired when | Basket posts to the ERP and the two till settings are removed | not affected by that switch |

They run side by side. Both can be live for the same till; they do not share
nodes, and switching off the BC webhook does not touch this pipeline.

## What the pipeline does

1. **Source.** Basket publishes each event into the pipeline itself
   ([Published into VRSky](direct.md), Bifrost ADR 0002): a VRSky envelope on
   `VRSKY_DATA`, subject `vrsky.data.<workspace id>.pipeline.<connection id>`,
   with the Basket event as the payload, the workspace id as `tenant_id` and
   the connection id as `integration_id`. VRSky runs nothing for this source;
   both filters take the event as Basket wrote it.
2. **Filter.** Only `basket.sale-recorded` and `basket.return-recorded`
   events whose `source.kind` is `pos` pass (`event_type` *is one of* the two).
   Retailhero refuses a whole batch on an event type its profile does not map
   (`basket.created`, `basket.priced`, ...), and on an event that did not come
   from a POS. `basket.completed` and `basket.cancelled` map to no fact:
   Retailhero only reports them back as skipped. They are not sent. A day
   close goes the other branch ([below](#the-day-close)).
3. **Converter.** Wraps the event, unchanged, as the single entry of a
   `BIFROST-BASKET-v1` batch (table below). An event without
   `data.journal_sequence` is not converted: it is retried and then
   dead-lettered with `X-Vrsky-Last-Error` naming the field, and nothing is
   sent to Retailhero. A replay from the DLQ sends the same event and fails
   the same way, so such an event has to be sent again by Bifrost with the
   field, or discarded from the DLQ. The converter checks only that the field
   is there; a value that is not a whole number from 1 goes out as written
   and Retailhero judges it.
4. **Producer.** POSTs the batch as JSON to the intake URL. A `5xx`, `429` or
   network error is retried and then dead-lettered. A `4xx` other than
   `401`/`403` is logged and dropped; see [HTTP failed deliveries](http.md#failed-deliveries).

## The batch

Retailhero's profile
([`docs/contracts/pos-source-bifrost-profile.md`](https://github.com/ValueRetail/Retailhero/blob/main/docs/contracts/pos-source-bifrost-profile.md),
Proposed 2026-11-10) takes the Bifrost event as it is and does the mapping to
sale facts itself, money included (below). The converter does not touch the
lines. It builds the wrapper:

| Batch member | From |
|---|---|
| `profileVersion` | fixed `BIFROST-BASKET-v1` |
| `schemaVersion` | fixed `1` |
| `sourceInstanceId` | the event's `source.instance_id` (the profile requires it to equal each event's) |
| `journalId` | the wizard field "Retailhero journal ID" |
| `entries[0].operationId` | `bifrost-sale-<event_id>-r1` for a sale, `bifrost-return-<event_id>-r1` for a return |
| `entries[0].journalPosition` | the event's `data.journal_sequence` as a decimal string (`"42"`); required |
| `entries[0].event` | the whole Basket event |

The wrapper is built with an `each` mapping whose source is `.` (the record
itself) and a `copy` of `.`. The operation ID is a `lookup` on `event_type`,
and `journalPosition` is a `to_string` marked `required`; see
[Filters & converters](filters-converters.md#lookup-and-required-fields).

`journal_sequence` is the counter behind the receipt or credit note number
(`42` for `T07-000042`), in the till's `journal_series`. Sales and returns
share one series per till, so a position never repeats within it. The field
is in Bifrost's events from ValueRetail/Bifrost#269 on, and the shapes here follow
the contract pinned for this path on 2026-11-11 and Bifrost's samples
(`basket/docs/events/basket-sale-recorded.v1.json` and
`basket-return-recorded.v1.json`, ValueRetail/Bifrost#274). The fixtures in
`src/test/fixtures/bifrost` are copied from those samples.

What Retailhero then makes of a sale (`basket.sale-recorded` v1):

| Retailhero fact field | From the event |
|---|---|
| `sourceTransactionId` | `data.sale_reference.value` |
| `storeRef` | `source.location_id` |
| `recordedAt` | `occurred_at` |
| `effectiveAt` | the latest line `at` |
| line `sourceItemCode` | `product.gtin` |
| line `enteredQuantity` | `quantity`, a whole count of base units |
| line `legalOwnerRef` | `legal_entity_id` |
| line net, tax, gross (NOK, scale 2) | `line_ex_vat`, `line_vat`, `line_inc_vat` when present (exact, after `discount`); otherwise derived from `quantity × unit_price` and `vat` |

`removed_line_ids` are lines that were not sold and are ignored.

A return (`basket.return-recorded` v1) is mapped as Retailhero maps a till
`return.completed`: a `monetary-observation`, a refund with negative gross.
Its `data` is the till's completed return (`credit_note_number`,
`original_receipt`, `lines`, the totals and `refund`) plus
`return_reference` and `original_sale_reference`. Bifrost publishes only
returns whose refund was approved. Totals are positive as refunded;
Retailhero signs them.

## The day close

Basket publishes a till's day close (Z report) as `basket.day-closed`
(ValueRetail/Bifrost#538). Retailhero takes it on the same route as sales and
returns, on a journal of its own: the sales journal with `-Z` appended
(`T07-Z` for `T07`, ValueRetail/Retailhero#3535). It runs on a second branch
of the pipeline, `flt-z` → `conv-z` → `dst-z`, beside the sales branch, which
is unchanged. Each node takes one predecessor, so the branches cannot share a
producer; `dst-z` has the same URL and credential as `dst`.

- **Filter.** `event_type` *equals* `basket.day-closed`, `source.kind` is
  `pos`, and `event_id` is not empty. A sale or return does not pass it, and a
  day close does not pass the sales filter.
- **Converter.** The same wrapper as a sale:

| Batch member | From |
|---|---|
| `profileVersion` | fixed `BIFROST-BASKET-v1` |
| `schemaVersion` | fixed `1` |
| `sourceInstanceId` | the event's `source.instance_id` |
| `journalId` | the wizard field "Retailhero journal ID" plus `-Z` |
| `entries[0].operationId` | `bifrost-day-close-<event_id>` |
| `entries[0].journalPosition` | the event's `data.z_number` as a decimal string (`"1"`); required |
| `entries[0].event` | the whole Basket event |

The operation ID is a `template` mapping, since the branch carries one event
type. A day close without `data.z_number` is retried and dead-lettered with
the field named, as a sale without `journal_sequence` is. The fixture
`src/test/fixtures/bifrost/basket-day-closed.v1.json` is copied unchanged
from Bifrost's `basket/docs/events/basket-day-closed.v1.json`.

## One event, one operation ID

The operation ID is `bifrost-sale-<event_id>-r1` for a sale and
`bifrost-return-<event_id>-r1` for a return. `event_id` is the Basket event's
ID, which stays the same on every delivery and is also the JetStream
`Nats-Msg-Id`. The `r1` is the source revision. It is always 1: Bifrost never
sends an amendment or a void, because a paid sale is corrected only by a
return (kassasystemforskrifta § 2-7). A day close is
`bifrost-day-close-<event_id>`, without a revision, the form Retailhero expects
(Retailhero#3535). So:

- Basket publishing the event again, a redelivery from `VRSKY_DATA`, or a
  retry of the HTTP call all send the same ID;
- Retailhero answers a repeat of an operation with the original result
  (C3 contract, idempotency), so a sale is counted once;
- two different sales never share an ID, because their event IDs differ.

The ID keys on `event_id`. If Bifrost re-emits a corrected sale with a new
`event_id` and the same `sale_reference`, it gets a new operation ID, so
Retailhero's `revision-conflict` rule (same business key, different content)
has to refuse it rather than count it twice. An event with no `event_id` is
dropped by the filter, because every such message would otherwise share one
literal operation ID and be answered with the first result.

VRSky's own duplicate suppression (five minutes) is only a first layer. The
operation ID is what holds after that.

## Setting it up

The wizard asks for:

| Field | Meaning |
|---|---|
| Retailhero journal ID | the journal Retailhero registered for this till source. Day closes go on the same ID with `-Z` appended |
| Retailhero intake URL | the C3 batch route; it must be reachable from VRSky and not a private address (or go through the remote agent) |
| Retailhero intake credential | the bearer credential Retailhero issued for this tenant and the POS intake. It is stored as an encrypted secret and sent as `Authorization: Bearer` ([HTTP bearer token](http.md#bearer-token), #850) |

Each value is asked once and written into both branches. The credential is
stored as two secrets, one per destination. After deploy the two journal IDs,
URLs and credentials are separate settings: to change one in the builder,
change it on both branches.

After deploy the wizard shows the connection id, the workspace id and the
subject. On Basket's side:

- set `BASKET_VRSKY_INTEGRATION_ID` to the connection id;
- Basket's tenant must be this workspace's id. VRSky resolves the pipeline's
  stored secrets (the intake credential) under the envelope's tenant, so with
  another tenant the destination cannot read its credential.

Nothing arrives until Basket publishes. Stopping the connection in VRSky
does not stop deliveries; stop Basket's publishing instead (see
[Published into VRSky](direct.md#starting-and-stopping)).

The connection does not start without the intake credential. A wrong or expired credential is answered `401`, which is retried
and then dead-lettered, so nothing is lost; replay from the DLQ once the
credential is fixed.

## Open questions

The template is built against documents, not running systems. These are not
guessed in the template; each one is a place where it will change.

1. **The intake route.** The C3 contract has a two-step flow
   (`POST batches`, then `POST reconciliations`), and the intake
   (Retailhero#2678) has not fixed the route or whether it folds both into one
   call. Retailhero's profile accepts `operationId` as a member of an entry,
   as this template sends it.
2. **The day close on Retailhero's side.** Retailhero's day-close intake
   (ValueRetail/Retailhero#3535, PR #3536) was still open when the
   day-close branch was added (#880). The branch sends what #880 specifies:
   the event unchanged, on `<journal>-Z`, at `z_number`. Check the result
   against Retailhero once that PR lands, in particular whether it reads the
   event's fields as Bifrost names them (`journal_series`, `z_number`) and
   whether it requires the journal ID to be the till's series (`T07`), in
   which case the wizard's journal field must hold the series.
3. **Fields Bifrost events do not carry**, as the profile lists them (and asks
   Bifrost#248 for): the stock location apart from the store; the currency
   scale and the currencies a store sells in (NOK only is accepted); the unit
   of `quantity`; one time for the whole sale instead of one per line; and
   what a `basket.cancelled` means for a sale already recorded. The template
   invents none of them.

Pipelines deployed from this template before #880 have no day-close branch
and do not send day closes. Deploy the template again to get it.

Pipelines deployed from this template before #851/#852 keep their old filter
and converter: they send sales only, without `journalPosition`. Deploy the
template again to get the new nodes.

Pipelines deployed from this template with the NATS source read Basket's
envelope as a message body and wrap it in a second one, so the filter never
sees the event. Deploy the template again to get the `direct` source.

Settled, with the contract pinned for this path on 2026-11-11:

- **Authentication** is a bearer credential from a stored secret (#850, PR
  #862): the wizard stores the token as `http.token_secret_id` on deploy and
  the destination sends it as `Authorization: Bearer`. A `401` from the
  intake is retried and dead-lettered, so nothing is lost.
- **Journal position** (#851): `data.journal_sequence`, sent as
  `journalPosition`. An event without it is dead-lettered with the reason.
- **Revisions**: always `r1`. Bifrost never sends an amendment or a void; a
  paid sale is corrected only by a return (kassasystemforskrifta § 2-7).
- **Returns** (#852): `basket.return-recorded` passes the filter and goes out
  with `bifrost-return-<event_id>-r1`.
- **What arrives** (once an open question): Basket publishes the VRSky
  envelope into `VRSKY_DATA` itself (Bifrost ADR 0002), with the event as the
  payload, `tenant_id` the workspace id and `integration_id` the connection
  id. The template's source is `direct`, so the payload the filter sees is
  the bare Basket event, as in `basket/docs/events/`. A live run through the
  filter, converter and HTTP producer, with an idle webhook source standing
  in for `direct`, showed it; `src/cmd/data-filter/bifrost_sales_test.go`
  holds the filter half against an envelope written as Basket sends it.
- **Money**: `unit_price` is the ex-VAT unit price before discount, and
  Retailhero uses the exact `line_ex_vat`, `line_vat` and `line_inc_vat` when
  the event has them.
