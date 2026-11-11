# Bifrost sales → Retailhero (template)

The onboarding template **POS sales → Retailhero (Bifrost Basket → Retailhero)**
sends each sale a Bifrost till records to Retailhero's POS reconciliation
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
| Starts at | the till's outbox, over a signed webhook | Basket's `basket.sale-recorded` event on the tenant JetStream |
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
2. **Filter.** Only `basket.sale-recorded` events whose `source.kind` is `pos`
   pass. Retailhero refuses a whole batch on an event type its profile does not
   map (`basket.created`, `basket.priced`, ...), and on a sale-recorded that did
   not come from a POS. `basket.completed` and `basket.cancelled` map to no
   fact: Retailhero only reports them back as skipped. They are not sent.
3. **Converter.** Wraps the event, unchanged, as the single entry of a
   `BIFROST-BASKET-v1` batch (table below).
4. **Producer.** POSTs the batch as JSON to the intake URL. A `5xx`, `429` or
   network error is retried and then dead-lettered. A `4xx` other than
   `401`/`403` is logged and dropped; see [HTTP failed deliveries](http.md#failed-deliveries).

## The batch

Retailhero's profile
([`docs/contracts/pos-source-bifrost-profile.md`](https://github.com/ValueRetail/Retailhero/blob/main/docs/contracts/pos-source-bifrost-profile.md),
Proposed 2026-11-10) takes the Bifrost event as it is and does the mapping to
sale facts itself. That includes the money: net, tax and gross come from
`unit_price`, `vat` and `quantity` exactly, with a refusal (`tax-not-exact`)
instead of rounding. The converter cannot do that arithmetic, so it does not
touch the lines. It builds the wrapper:

| Batch member | From |
|---|---|
| `profileVersion` | fixed `BIFROST-BASKET-v1` |
| `schemaVersion` | fixed `1` |
| `sourceInstanceId` | the event's `source.instance_id` (the profile requires it to equal each event's) |
| `journalId` | the wizard field "Retailhero journal ID" |
| `entries[0].event` | the whole Basket event |
| `entries[0].operationId` | `bifrost-sale-<event_id>-r1` |

The wrapper is built with an `each` mapping whose source is `.` (the record
itself) and a `copy` of `.`; see [Filters & converters](filters-converters.md).

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
| line net, tax, gross (NOK, scale 2) | `quantity × unit_price`, `× vat`, the sum |

`removed_line_ids` are lines that were not sold and are ignored.

## One sale, one operation ID

The operation ID is `bifrost-sale-<event_id>-r1`. `event_id` is the Basket
event's ID, which stays the same on every delivery and is also the JetStream
`Nats-Msg-Id`. The `r1` is the source revision: every Bifrost sale is revision 1
today, because no Bifrost event amends or voids a sale. So:

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

1. **Authentication.** Retailhero authenticates VRSky as a tenant workload with
   a bearer credential (ValueRetail/Retailhero#2675, decision 3). The HTTP
   producer can attach a bearer only from an OAuth grant, and a header typed
   into `http.headers` is stored in plain text, so the template sends no
   `Authorization` header. Until the producer takes a stored bearer secret, the
   intake rejects every call with `401`, which the producer retries and then
   dead-letters, so nothing is lost, only held. Whether a `{secret:<id>}` placeholder in a header already does this is being checked in #850.
2. **Journal position** (#851). The profile needs a `journalPosition` per entry. Basket
   events carry none (Bifrost#248 asks). The template sends none, and the
   profile refuses such a batch (`missing-field`). Candidates: the stream
   sequence of the Basket event, or the intake assigning it.
3. **Where the operation ID goes.** The C3 contract says one operation ID per
   source revision; the intake (Retailhero#2678) has not fixed the route, the
   wire encoding, or whether the ID is an `operationId` on each entry (what the
   template does) or something else. The profile today refuses unknown members
   of an entry, so Retailhero has to accept it, or this mapping changes. The
   route is also a two-step flow in the contract (`POST batches`, then
   `POST reconciliations`); whether the intake folds both into one call is open.
4. **What is on the stream.** The template assumes each message body is the
   bare Basket event JSON, as in the examples in Bifrost's
   `basket/docs/events/`. Bifrost's ADR 0002 says events travel in a VRSky
   transport envelope on `VRSKY_DATA`. If the body is that envelope, the
   converter must read the event from its `payload`. The stream name, subjects
   and credentials for the tenant's JetStream are also not documented.
5. **Revisions.** There is no amendment or void event, so every sale is
   revision 1. When Bifrost publishes them (Bifrost#248), the revision in the
   operation ID has to come from the event.
6. **Returns** (#852). `return.completed` is not published as an event yet, so the
   filter lets none through.
7. **Fields Bifrost events do not carry**, as the profile lists them (and asks
   Bifrost#248 for): the stock location apart from the store; the currency
   scale and the currencies a store sells in (NOK only is accepted); whether
   `unit_price` is ex VAT and the VAT rounding rule; the unit of `quantity`;
   one time for the whole sale instead of one per line; and what a
   `basket.cancelled` means for a sale already recorded. The template invents
   none of them.
