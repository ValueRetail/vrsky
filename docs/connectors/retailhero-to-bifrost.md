# Template: Retailhero articles and prices to Bifrost tills

The onboarding template **Articles and prices → tills (Retailhero → Bifrost)**
moves articles and prices from Retailhero into the catalogue of the Bifrost
tills. The tills keep pulling from the catalogue-api as they do today.

```text
Retailhero partner account                    VRSky                                       Bifrost
rh.evt.v1.<tenant>.pos_publication.  ─►  nats ─► converter ─► bifrost_catalogue (delta) ─►  catalogue-api
  bifrost-catalogue.batch
```

It is marked **beta**. Retailhero publishes the batches from
ValueRetail/Retailhero PR #2746 (Retailhero#2744, not merged when this was
written), and the converter and producer are tested against that PR's sample
event. Bifrost takes `price_incl_vat` with `vat_code` since
ValueRetail/Bifrost PR #252. What still stands between it and the tills is
under [What blocks it](#what-blocks-it).

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

1. **Source, `nats`.** Binds the durable Retailhero's operator created on the
   tenant's partner account (`create_durable: false`) and takes one batch at
   a time (`batch_size: 1`): a batch is published into the pipeline and acked
   before the next is fetched. Where the durable starts is Retailhero's
   setting (`all` today, `ConsumerSpec::to_config` in its `messaging.rs`), not
   VRSky's. In `delta` mode a full-kind batch is applied as an upsert and
   removes nothing. Loading a store from scratch is the full-list variant
   (#856).
2. **Converter.** Takes `fact.rows` of one batch event and returns
   `{"items": [...], "sequence": <fact.sequence>}`.
3. **Destination, `bifrost_catalogue`, mode `delta`.** One batch is one staged
   import (begin with the sequence, items, commit; see
   [Bifrost catalogue](bifrost-catalogue.md)). `full` is not used: it replaces
   the whole catalogue with the message, and a Retailhero batch is not the
   whole catalogue (a source that can collect a whole list is #841).

## The message the template reads

Retailhero's dispatch (`integration::pos_publication::bifrost_events` in the
Retailhero repository, contract `docs/contracts/pos-publication-bifrost-catalogue.md`,
"Transport to VRsky") runs the Bifrost catalogue profile over one frozen
publication and writes each batch as one event:

| | |
|---|---|
| Subject | `rh.evt.v1.<tenant>.pos_publication.bifrost-catalogue.batch` |
| Stream | `RH_EVT_V1_POS_PUBLICATION` |
| Durable | `vrsky_bifrost-catalogue-batch_<tenant>` (made by Retailhero's operator) |
| `Nats-Msg-Id` | the event id, `bifrost-<publication id>-<batch>` |

The body has the envelope of Retailhero's other events and the batch in
`fact` (trimmed from Retailhero's sample,
`tests/fixtures/pos-publication/bifrost-catalogue/v1/event-batch.json`):

```json
{
  "contractVersion": "pos-publication-events.v1",
  "eventType": "bifrost-catalogue.batch",
  "event": {"id": "bifrost-Pub-1-1", "producer": "pos_publication", "tenantId": "T-A"},
  "stream": {"reference": {"owner": "pos_publication", "kind": "bifrost-catalogue", "id": "Store-1", "tenantId": "T-A"},
             "sequence": "1", "previousEvent": null},
  "recordedAt": "2026-11-11T12:00:00.000000Z",
  "correlationId": "bifrost-Pub-1",
  "fact": {
    "kind": "full",
    "effectiveFrom": "2026-10-31T23:00:00Z",
    "sequence": 1,
    "batch": 1, "batchCount": 3,
    "publicationId": "Pub-1", "targetId": "Target-1", "storeId": "Store-1",
    "profile": "bifrost-catalogue-import", "profileVersion": "1.0",
    "rows": [
      {
        "sourceItemId": "S-A1",
        "importItem": {
          "article_no": "A-100", "gtin": "7000000000015", "name": "Vare A",
          "price_incl_vat": "24.90", "vat_code": "H", "unit": "pcs",
          "category": "Dagligvare", "alcohol": false,
          "effective_from": "2026-10-31T23:00:00Z"
        }
      }
    ]
  }
}
```

The template reads `fact.rows[].importItem` and `fact.sequence`, nothing else.
`fact.kind` doesn't matter, since every batch goes as a delta.

A publication can be several events. Retailhero gives each distinct start
(`effective_from`) its own batch, since Bifrost keeps one row per article in an
import, and it is to split a large batch across events (not yet, see
[What blocks it](#what-blocks-it)). Each event has
the next `fact.sequence`, and each is a delta of its own. Nothing in VRSky
treats one event as the whole list: an article that isn't in a batch stays in
the catalogue as it was.

The fixtures in `src/test/fixtures/retailhero` are events in this shape:
`bifrost-batch-full-1.json` is Retailhero's sample event as it is, and
`bifrost-batch-delta.json` is the delta batch of the profile's
`expected-delta.json` in the same envelope, numbered 4 as the dispatch would
number it after the full package's three batches.

### Retailhero's other events

The partner account also exports `product.version`, `pack.version`,
`gtin.version`, `price.version` and `campaign.version` (streams
`RH_EVT_V1_PRODUCT`, `RH_EVT_V1_PRICING`, `RH_EVT_V1_CAMPAIGN`). They are not
for this template. A Bifrost item needs fields from several of them, which a
converter can't join; `price.version` is an authored price, not an approved
one; and the VAT rate is only in the frozen POS publication. ADR-0063 in the
Retailhero repository says the tills get the frozen publication content,
never a price VRSky works out. Each durable is filtered to one subject, so
binding the batch durable is enough to keep them out.

## Mapping

For each row of `fact.rows`, the converter renames each field of
`importItem` into the item, field for field, and drops everything else
(`sourceItemId`, the event envelope, the rest of `fact`). A field the row does
not have stays absent; it does not become `null`. The fields are exactly those
of Retailhero's wire `ImportItem` (`import_item()` in its
`bifrost_catalogue/mod.rs`): `gtin`, `category` and `min_age` are left out
when empty, the rest are always there.

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

Beside the items, `fact.sequence` goes to the producer as `sequence`, and the
producer sends it on `begin` (see [Sequence](bifrost-catalogue.md#sequence)).
It is a whole number from 1 that grows by exactly one per batch for each
Retailhero tenant and store, across publications. Bifrost refuses the commit
with 409 when it already has that number or a higher one, and the producer
then drops the delivery instead of retrying it.

`recordedAt` is not used. It is the start time of the transaction that wrote
the batches, so every batch of one publication has the same value, and
Bifrost would refuse all but the first. A batch without `fact.sequence` (or
with `null`) is not converted: the converter fails it, so it is retried and
then lands in the DLQ with the field named. Sent unnumbered it would be
applied whatever Bifrost already holds, and a late retry could put an older
price back.

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

## Order

Bifrost keeps one sequence per catalogue and refuses a lower one with 409, and
the producer drops that delivery. So a batch that reaches Bifrost after the
next one is lost: its rows reach the tills only when a later batch or a full
import carries the same articles. It is logged as an error (`dropped the
delivery; its rows reach the tills only if a later import carries them`).
The guard means an older price never overwrites a newer one, but the batches
still have to arrive in order for none to be lost.

What keeps them in order:

- Retailhero's relay publishes the batches in `fact.sequence` order.
- The source holds one batch at a time (`batch_size: 1`). It publishes the
  batch into the pipeline, retrying the same batch if that fails, acks it, and
  only then fetches the next.
- The producer runs as one replica and handles one message at a time.

Where VRSky can still let a later batch through first:

- **A retry.** When the converter or the producer fails on batch n (Bifrost
  answers 5xx, times out or restarts), n waits for its retry (1 s, 5 s, 30 s,
  2 min) while n + 1 goes on and commits. n is then dropped with 409.
- **The converter runs as two pods** on one durable. Batch n and n + 1 can be
  converted at the same time and reach the producer in either order. The
  window is short, because the source forwards n + 1 only after n is stored
  and acked, but a large batch followed by a small one can lose the race.
- **A crash or a lost connection at the source.** The held batch comes back
  when the durable's AckWait (30 s at Retailhero) runs out, and the next one
  can be delivered first.
- **A deploy.** The source and the producer roll over with a new pod
  started before the old one stops. For that moment two source pods bind the
  same durable, and two producer pods run imports, so batch n + 1 can go
  ahead of n.

Retailhero setting the durable's `maxAckPending` to 1 for this event type
closes the last two at the source; VRSky never changes a durable it only
binds.

A batch can also go missing without that error. Every time the source hands
a held batch back (a stop, a deploy, a lost connection) the server counts a
delivery, and after Retailhero's `maxDeliver` (10) the batch leaves the
durable. With one batch held at a time the next one then commits as normal.
VRSky logs nothing; the only sign is the MAX_DELIVERIES advisory on
Retailhero's server, which Retailhero's contract says an operator must watch.

Ordered handling per connection in the converter and the producer is not
built (`plans/backlog.md`). Until it is, watch for the error above, and when
it shows, send the articles again with a newer batch or load the store with a
full import (#856).

## Setting it up

The wizard asks for the Retailhero NATS server, the partner user's NKey seed,
the inbox prefix, the durable, the subject filter, and the Bifrost address and
API key. The rest is fixed in the template: `auth_type: nkey`, the API prefix
`JS.RH.API`, the stream `RH_EVT_V1_POS_PUBLICATION`, `create_durable: false`
and `batch_size: 1`. For tenant `T-A`:

| Field | Value |
|---|---|
| NKey seed | the contents of `vrsky_t_a.nk` |
| Inbox prefix | `_INBOX_vrsky_t_a` |
| Durable | `vrsky_bifrost-catalogue-batch_T-A` |
| Subject filter | `rh.evt.v1.T-A.pos_publication.bifrost-catalogue.batch` |

A server whose certificate is signed by Retailhero's own CA needs that CA on
the source (`ca_cert`), set in the pipeline builder after the wizard: the
wizard's fields are one line. See
[NATS JetStream](nats.md#a-retailhero-partner-account).

**One store per Retailhero tenant.** `fact.sequence` counts per tenant and
store, and Retailhero's durable carries every store of the tenant. The
pipeline sends everything to one Bifrost catalogue, so a second store's
batches would land in the first store's catalogue, or be refused there with
409 when their number is lower. Until there is a durable (or a route) per
store, use this template only for a Retailhero tenant that serves one store.

## Running it locally for a test

For a test on one computer, with Retailhero, Bifrost and VRSky's
`docker-compose.yml` stack all running there. This is for local testing only:
production can't reach the catalogue-api this way, and how it will is still
open in #843.

1. Start the stack with the two connectors this template uses, which
   `make up-core` leaves out:

    ```bash
    make up-core
    docker compose up -d --build nats-consumer bifrost-catalogue-producer
    ```

2. Local addresses are refused unless the operator switch
   `ALLOW_PRIVATE_UPSTREAMS=true` is set on `management-api` (its start check
   reads `base_url` and the NATS server), `nats-consumer` and
   `bifrost-catalogue-producer`. `docker-compose.yml` sets it to `true` on all
   three unless the shell sets it to something else; check with
   `docker compose config | grep ALLOW_PRIVATE_UPSTREAMS`. It also lets
   `base_url` be plain `http`, so Bifrost needs no proxy in front. Never set
   it in production.
3. From inside a container on Docker Desktop, a service on the host is
   `host.docker.internal`, not `localhost` (on Linux, give the services
   `extra_hosts: ["host.docker.internal:host-gateway"]` in an override file). Bifrost's `base_url` is then
   `http://host.docker.internal:<catalogue-api port>`.
4. Retailhero's local broker (`messaging-up`) publishes no host port, and its
   certificate names the host `nats`, which inside VRSky's compose network is
   VRSky's own NATS. Reaching it from `nats-consumer` takes a change on
   Retailhero's side (a published port and a certificate for that name) or a
   Docker network setup that keeps the two names apart. Neither is written
   down yet. Put Retailhero's run CA (`ca.crt`) on the source in the pipeline
   builder.

## What blocks it

- **How VRSky reaches the catalogue-api in production** is open (#843).
- **Retailhero's dispatch is not merged** (Retailhero PR #2746). Nothing
  calls it yet (no route or schedule), and a full batch above about 4,000
  articles passes the stream's 1 MiB message limit until Retailhero splits
  it. Also open: whether Bifrost keeps a per-item `effective_from` later than
  the import start inside a full import.
- **Removals and full lists.** A delta cannot remove an article or withdraw a
  price. Retailhero's profile refuses delistings and retirements for that
  reason. The first load and a clean-up need a full import, which needs the
  whole list in one message (#841) and a named person for the shrink guard.
- **Campaigns and member prices** have no place in Bifrost's catalogue yet
  (ValueRetail/Bifrost#247). The campaign price itself flows as an ordinary
  price.
- **Order inside VRSky** holds only when no deploy runs and every batch goes
  through on its first attempt (see [Order](#order)).
- **One store per Retailhero tenant** (see [Setting it up](#setting-it-up)).

## Tests

- `src/cmd/data-converter/retailhero_template_test.go` runs the deployed
  converter config on embedded JetStream against the batch events in
  `src/test/fixtures/retailhero` and compares with the payloads there. It
  also checks that `fact.sequence` comes out as the same whole number (not
  `recordedAt` or `stream.sequence`) and that an event without it fails.
- `src/cmd/bifrost-catalogue-producer/retailhero_template_test.go` sends those
  payloads through the producer in `delta` mode to the stubbed catalogue-api
  and checks that every field, the price strings included, arrives as written
  with `fact.sequence` on `begin`; that a publication cut into three events
  commits three deltas and keeps the articles of each; and that a batch
  retried after the next one committed is dropped.
- `ui/src/onboarding/templates.test.ts` keeps the template in step with the
  Go test's config and with the connectors' keys, and checks the partner
  account settings and `batch_size: 1`.
