# Template: Retailhero articles and prices to Bifrost tills

The onboarding template **Articles and prices → tills (Retailhero → Bifrost)**
moves articles and prices from Retailhero into the catalogue of the Bifrost
tills. The tills keep pulling from the catalogue-api as they do today.

```text
Retailhero partner account        VRSky                               Bifrost
rh.evt.v1.<tenant>.…  ─►  nats source ─► converter ─► bifrost_catalogue (delta) ─► catalogue-api
```

It is marked **beta**. It deploys, and its converter and producer are tested
against Retailhero's fixtures, but it cannot run end to end yet: Retailhero
does not publish the Bifrost batches (see
[What Retailhero publishes](#what-retailhero-publishes) and
[What blocks it](#what-blocks-it)).

## Who is master

Retailhero is the master for articles and prices in the Bifrost pilot stores
(ValueRetail/Retailhero#2675). **Only one master may feed a store.** The
existing Business Central to Bifrost price path
([`docs/bc-prices.md` in the Bifrost repository](https://github.com/ValueRetail/Bifrost/blob/main/docs/bc-prices.md))
has to be switched off for every store this template feeds. Two masters writing
to one catalogue overwrite each other's prices article by article, and the
last writer wins with nothing in either system to say so. This template does
not check it: the catalogue-api accepts both. Switching the BC path off for a
store is a step in the rollout of that store, done before the first message is
sent.

Stores that stay on Business Central keep the BC path and don't use this
template.

## What the pipeline does

1. **Source, `nats`.** A durable pull consumer on the partner account's stream,
   filtered to the subjects that carry the Bifrost batches. `deliver_policy`
   is `new`, so a start does not push the whole stream into the pipeline (see
   [NATS JetStream](nats.md)). A durable that is new or renamed therefore
   misses the catalogue as it stands and only gets the batches published
   after it exists. In `delta` mode a full-kind batch is applied as an
   upsert and removes nothing, so it does not replace the catalogue either.
   Loading a store from scratch is the full-list variant (#856).
2. **Converter.** Takes the `rows` of one batch and returns
   `{"items": [...]}`.
3. **Destination, `bifrost_catalogue`, mode `delta`.** One batch is one staged
   import (begin, items, commit; see [Bifrost catalogue](bifrost-catalogue.md)).
   `full` is not used: it replaces the whole catalogue with the message, and a
   Retailhero batch is not the whole catalogue (a source that can collect a
   whole list is #841).

## The message the template expects

Retailhero's Bifrost catalogue target profile
(`docs/contracts/pos-publication-bifrost-catalogue.md` in the Retailhero
repository, a Proposed contract) maps a published package into rows, and each
row comes out twice: as Bifrost's `CatalogueItem` and as the wire `importItem`.
The template reads the wire form. **One message is one import batch** of the
profile (a full import, or one delta group):

```json
{
  "kind": "delta",
  "effectiveFrom": "2026-12-15T23:00:00Z",
  "rows": [
    {
      "sourceItemId": "S-B2",
      "importItem": {
        "article_no": "B-200", "gtin": "7000000000039", "name": "Vare B",
        "price_incl_vat": "25.00", "vat_code": "F", "unit": "pcs",
        "category": "Mat", "alcohol": false,
        "effective_from": "2026-12-15T23:00:00Z"
      }
    }
  ]
}
```

It has to be one batch per message because Bifrost holds one row per article
in an import (the last duplicate wins). A full package gives the same article
several rows with different `effective_from`; they sit in different batches,
and sent as one message the earlier prices would be lost.

This is what VRSky needs, not something Retailhero publishes today. The
fixtures in `src/test/fixtures/retailhero` are the profile's own expected
output (Retailhero `tests/fixtures/pos-publication/bifrost-catalogue/v1`), cut
into one message per batch. When Retailhero publishes the batches, the
converter's `rows` / `importItem` paths follow wherever that event puts them
(inside `fact`, if it is built like Retailhero's other events), and the
subject filter names its subject.

## What Retailhero publishes

Checked against Retailhero's main branch on 2026-11-11, after
ValueRetail/Retailhero#2680 closed (PRs #2692 and #2720).

The per-tenant partner account exports exactly these subjects, one durable
pull consumer per tenant and type:

| Subject | Stream | Durable for tenant `T-A` |
|---|---|---|
| `rh.evt.v1.<tenant>.product.product.version` | `RH_EVT_V1_PRODUCT` | `vrsky_product-version_T-A` |
| `rh.evt.v1.<tenant>.product.pack.version` | `RH_EVT_V1_PRODUCT` | `vrsky_pack-version_T-A` |
| `rh.evt.v1.<tenant>.product.gtin.version` | `RH_EVT_V1_PRODUCT` | `vrsky_gtin-version_T-A` |
| `rh.evt.v1.<tenant>.pricing.price.version` | `RH_EVT_V1_PRICING` | `vrsky_price-version_T-A` |
| `rh.evt.v1.<tenant>.campaign.campaign.version` | `RH_EVT_V1_CAMPAIGN` | `vrsky_campaign-version_T-A` |

- The list lives in `infrastructure/local/receiving/config/nats/streams.json`
  (`partners`) and is described in `docs/operations/local-receiving.md`,
  "VRSky partner accounts".
- The message body is the owner's outbox row, published unchanged by the
  relay with `Nats-Msg-Id` set to the event id. Its shape is the event
  contract (`contracts/events/master-data/v1.schema.json`,
  `contracts/events/pricing/v1.1.schema.json`): top-level `contractVersion`,
  `eventType`, `event` (tenant, producer, id), `stream` (the object, its
  per-object `sequence`, the previous event), `recordedAt` (RFC 3339, UTC),
  `correlationId` and `fact` (the version itself).
- Events arrive in dependency order, not version order across types.
  Retailhero asks consumers to be idempotent on the event id and not to drop
  an event because its version is lower than one already seen.
- The account signs in with an NKey seed (no JWT), has no JetStream of its
  own, uses the API prefix `JS.RH.API` and the inbox prefix
  `_INBOX_vrsky_t_<x>`, and may not ask for stream info. Retailhero's operator
  creates the durables; the account may create only those same durables. The
  `nats` source reads such an account once PR #859 is merged
  ([NATS JetStream](nats.md)).

None of these events is a Bifrost import batch, and the template cannot build
one from them:

- A Bifrost item needs fields from several events (SKU and name from
  `product.version`, the barcode from `gtin.version`, the price from
  `price.version`). A converter maps one message at a time and can't join
  them.
- `price.version` is an authored version. Its approval is a separate
  `price.decision` event, which is not exported. Retailhero's ADR-0063 says
  the tills get the frozen publication content, never a price VRSky works out.
- The VAT rate is frozen in the POS publication package (target A profile
  1.3, Retailhero#2687), not carried on these events.

The Bifrost batches come from Retailhero's Bifrost catalogue profile
(`core/receiving/src/integration/pos_publication/bifrost_catalogue`, PR #2685,
contract `docs/contracts/pos-publication-bifrost-catalogue.md`, Proposed). It
is a pure function from a frozen package to batches and refusals, with no
transport: dispatch is outside Retailhero#2676. Nothing publishes its output
on NATS yet.

A message of another shape (a campaign or a product event that the filter lets
through) leaves the converter without `items` and is dropped by the producer
as "not a list of items", with a log line. Narrow the subject filter to the
batches to keep that quiet.

## Mapping

The converter renames each field of `importItem` into the item, field for
field, and drops everything else (`sourceItemId`, the batch header, the
profile's `catalogueItem` copy). A field the row does not have stays absent; it
does not become `null`.

| Bifrost item field | From | Notes |
|---|---|---|
| `article_no` | `importItem.article_no` | Retailhero SKU |
| `gtin` | `importItem.gtin` | absent if the article has none |
| `name` | `importItem.name` | short name in the sender's locale |
| `price_incl_vat` | `importItem.price_incl_vat` | the frozen VAT-inclusive price, copied |
| `vat_code` | `importItem.vat_code` | H, F, L or N |
| `unit` | `importItem.unit` | `pcs` |
| `category` | `importItem.category` | optional |
| `min_age` | `importItem.min_age` | optional |
| `alcohol` | `importItem.alcohol` | |
| `effective_from` | `importItem.effective_from` | UTC start of the price; prices are sent before they apply |

The mapping lives in `ui/src/onboarding/templates.ts` and is the same file the
Go test reads (`ui/src/onboarding/testdata/retailhero-to-bifrost.json`).

### Prices are strings

Retailhero writes the price as an exact two-decimal string and Bifrost takes
decimal strings. The converter uses `rename` only, never `to_number`, because
`to_number` goes through a float. The test checks that every price reaches the
catalogue-api as the string Retailhero wrote.

### VAT

VAT travels per item. Retailhero's profile gives each row a rate of 25, 15, 12
or 0 percent, and writes it on the wire as the code H, F, L or N, which
Bifrost's per-tenant map turns back into the rate (the Norwegian default map).
A tenant with another map needs another profile version.

**Retailhero has no VAT rate on its product master yet**
(ValueRetail/Retailhero#2687). Until it has, the rate is supplied by the
sender, which is Retailhero's side of the profile: one rate per product, in
the sender configuration. The template therefore has no VAT field, and it does
not default, repair or derive a rate. A product without a rate is refused by
the profile and never reaches VRSky, so it is missing from the tills rather
than sold at a guessed VAT. When Retailhero freezes the rate on the product,
the profile reads it from there and nothing changes here.

## Setting it up

The wizard asks for the Retailhero NATS server, a user and password, the
stream, a durable name, the subject filter, and the Bifrost address and API
key. An account that signs in with a `.creds` file needs the pipeline builder
instead, which has a box for the whole file (the wizard's secret field is one
line).

Retailhero's partner account signs in with an NKey seed instead, and needs the
API prefix `JS.RH.API`, its inbox prefix and `create_durable: false` (see
[What Retailhero publishes](#what-retailhero-publishes)). The wizard can't set
those yet. Once PR #859 is merged, set them on the source in the pipeline
builder. Use the stream, durable and subject Retailhero names for the batch
event; each durable is filtered to exactly one subject.

## What blocks it

- **Bifrost must accept `price_incl_vat`** (ValueRetail/Bifrost#249, PR #252).
  Its import takes `price_ex_vat` only today and refuses the whole batch when `price_ex_vat` is missing (the field is required).
  Whether it also keeps `vat_code` or switches to `vat_rate` next to the
  inclusive price is for that change to settle; if it takes `vat_rate`, the
  converter maps `importItem.vat_code` to `catalogueItem.vat_rate` instead.
- **Retailhero must publish the batches.** The partner account (#2680) carries
  product, pack, GTIN, price and campaign events, which the template can't use
  (see [What Retailhero publishes](#what-retailhero-publishes)). The batches
  need a dispatch on Retailhero's side that runs the Bifrost profile and
  publishes each batch as its own event on a subject exported to the partner
  account. No Retailhero issue for that was found on 2026-11-11. Also open:
  whether Bifrost keeps a per-item `effective_from` later than the import
  start inside a full import.
- **Removals and full lists.** A delta cannot remove an article or withdraw a
  price. Retailhero's profile refuses delistings and retirements for that
  reason. The first load and a clean-up need a full import, which needs the
  whole list in one message (#841) and a named person for the shrink guard.
- **Campaigns and member prices** have no place in Bifrost's catalogue yet
  (ValueRetail/Bifrost#247). The campaign price itself flows as an ordinary
  price.
- **Order.** A retried message can commit after a newer one. See "Retries are
  safe" in [Bifrost catalogue](bifrost-catalogue.md). Bifrost now has a
  `sequence` guard on begin (Bifrost#253); the producer doesn't send it yet
  (#858).

## Tests

- `src/cmd/data-converter/retailhero_template_test.go` runs the deployed
  converter config on embedded JetStream against Retailhero's fixtures and
  compares with the item lists in `src/test/fixtures/retailhero`.
- `src/cmd/bifrost-catalogue-producer/retailhero_template_test.go` sends those
  lists through the producer in `delta` mode to the stubbed catalogue-api and
  checks that every field, the price strings included, arrives as written and
  that articles the batches don't mention stay.
- `ui/src/onboarding/templates.test.ts` keeps the template in step with the
  Go test's config and with the connectors' keys.
