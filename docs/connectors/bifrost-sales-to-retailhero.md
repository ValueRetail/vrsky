# Bifrost sales → Retailhero (template)

The onboarding template **POS sales → Retailhero (Bifrost Basket → Retailhero)**
sends each sale and return a Bifrost till records to Retailhero's POS reconciliation
(C3), so Retailhero can reconcile what the tills sold against its own stock.
It is a beta: the Retailhero side is a Proposed contract and its HTTP intake
is not built yet (see [what is still open](#open-questions)).

```text
Bifrost tenant JetStream ──► filter ──► converter ──► HTTP producer ──► Retailhero C3 intake
   (nats source)          till sales     batch of one    POST JSON
```

## How it sits beside till → Business Central

Bifrost's `docs/sales-to-bc.md` describes a temporary road: the till posts each
sale straight to a VRSky webhook, which creates a draft sales invoice in
Business Central. This template is a different road for a different purpose.

| | till → BC (temporary) | this template |
|---|---|---|
| Starts at | the till's outbox, over a signed webhook | Basket's `basket.sale-recorded` and `basket.return-recorded` events on the tenant JetStream |
| Ends at | a draft sales invoice in Business Central | a staged sale in Retailhero C3 |
| Purpose | accounting copy of the sale | stock and sales reconciliation |
| Retired when | Basket posts to the ERP and the two till settings are removed | not affected by that switch |

They run side by side. Both can be live for the same till; they do not share
nodes, and switching off the BC webhook does not touch this pipeline.

## What the pipeline does

1. **Source.** A NATS JetStream source reads the stream Bifrost publishes
   Basket events to ([NATS connector](nats.md)). It uses a durable consumer
   with `deliver_policy: all`, so the first start reads what the stream still
   holds. That is safe because every sale carries a fixed operation ID (below).
2. **Filter.** Only `basket.sale-recorded` and `basket.return-recorded`
   events whose `source.kind` is `pos` pass (`event_type` *is one of* the two).
   Retailhero refuses a whole batch on an event type its profile does not map
   (`basket.created`, `basket.priced`, ...), and on an event that did not come
   from a POS. `basket.completed` and `basket.cancelled` map to no fact:
   Retailhero only reports them back as skipped. They are not sent.
3. **Converter.** Wraps the event, unchanged, as the single entry of a
   `BIFROST-BASKET-v1` batch (table below). An event without
   `data.journal_sequence` is not converted: it is retried and then
   dead-lettered with `X-Vrsky-Last-Error` naming the field, so it can be
   replayed once Bifrost sends it.
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

## One event, one operation ID

The operation ID is `bifrost-sale-<event_id>-r1` for a sale and
`bifrost-return-<event_id>-r1` for a return. `event_id` is the Basket event's
ID, which stays the same on every delivery and is also the JetStream
`Nats-Msg-Id`. The `r1` is the source revision. It is always 1: Bifrost never
sends an amendment or a void, because a paid sale is corrected only by a
return (kassasystemforskrifta § 2-7). So:

- a redelivery from Bifrost's stream, a retry of the HTTP call, or a restart
  that reads the event again all send the same ID;
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
| Bifrost NATS server, user, password | where and as whom VRSky reads. The password is stored as a secret. |
| Stream, durable name | the stream that holds Basket events, and a name for the consumer VRSky creates |
| Retailhero journal ID | the journal Retailhero registered for this till source |
| Retailhero intake URL | the C3 batch route; it must be reachable from VRSky and not a private address (or go through the remote agent) |

Before you start it: the NATS user needs the permissions listed on the
[NATS page](nats.md#notes), and the tenant must have Retailhero's intake
credential, which this template cannot send yet.

## Open questions

The template is built against documents, not running systems. These are not
guessed in the template; each one is a place where it will change.

1. **The intake route.** The C3 contract has a two-step flow
   (`POST batches`, then `POST reconciliations`), and the intake
   (Retailhero#2678) has not fixed the route or whether it folds both into one
   call. Retailhero's profile accepts `operationId` as a member of an entry,
   as this template sends it.
2. **What is on the stream.** The template assumes each message body is the
   bare Basket event JSON, as in the examples in Bifrost's
   `basket/docs/events/`. Bifrost's ADR 0002 says events travel in a VRSky
   transport envelope on `VRSKY_DATA`. If the body is that envelope, the
   converter must read the event from its `payload`. The stream name, subjects
   and credentials for the tenant's JetStream are also not documented.
3. **Fields Bifrost events do not carry**, as the profile lists them (and asks
   Bifrost#248 for): the stock location apart from the store; the currency
   scale and the currencies a store sells in (NOK only is accepted); the unit
   of `quantity`; one time for the whole sale instead of one per line; and
   what a `basket.cancelled` means for a sale already recorded. The template
   invents none of them.

Settled, with the contract pinned for this path on 2026-11-11:

- **Authentication** is a bearer credential from a stored secret (#850). Until
  that is merged, the template sends no `Authorization` header and the intake
  answers `401`, which is retried and dead-lettered, so nothing is lost.
- **Journal position** (#851): `data.journal_sequence`, sent as
  `journalPosition`. An event without it is dead-lettered with the reason.
- **Revisions**: always `r1`. Bifrost never sends an amendment or a void; a
  paid sale is corrected only by a return (kassasystemforskrifta § 2-7).
- **Returns** (#852): `basket.return-recorded` passes the filter and goes out
  with `bifrost-return-<event_id>-r1`.
- **Money**: `unit_price` is the ex-VAT unit price before discount, and
  Retailhero uses the exact `line_ex_vat`, `line_vat` and `line_inc_vat` when
  the event has them.
