# Plan: Business Central ↔ VRSky ↔ POS ↔ Basket

Written 2026-09-22. Answers the four questions from that day: can VRSky
receive from Business Central, how do we get a BC to test against, can POS
sales reach BC through VRSky, and how does the colleague's Basket feed both
POS and BC through VRSky.

## Open questions (need answers from people, not code)

1. **Accounting shape** — does each receipt become a posted *sales invoice*
   in BC, or does the day's takings become one *journal* batch? Changes the
   converter mapping (Phase 3), not the pipeline. Ask the accountant.
2. **Who owns the master data key** — POS `article_no` must equal BC item
   `number`. Confirm BC is the source of truth for the catalogue (the
   architecture note's Fig. 2 says "ERP → katalog").
3. **Basket event contract** — the colleague decides whether the Basket
   forwards `till-events` unchanged or emits its own normalized events.
   Recommendation: its own events; the till's payload is the till's business.
4. **Trial vs paid BC** — the free trial is enough for all of this. Buy
   Essentials (813,20 kr/user/month ex VAT, annual) only if the trial is
   allowed to expire before a pilot.
5. **Cloudflare admin for `vrsky.valueretail.no`** — not needed for the
   test (see Phase 0), needed before any partner behind Telenor sends to us.

## Goal

One end-to-end chain proven on a real Business Central trial tenant:

```
BC items ──(bc consumer, poll)──> VRSky ──converter──> PUT catalogue-api ──> tills pull
Web shop cart ──> Basket                                    (Bifrost module, later via VRSky)
Till sale ──(outbox)──> Basket                              (direct, Basket API)
Basket events ──(webhook)──> VRSky ──converter──> BC salesOrders / salesInvoices
                                    └─converter──> BI / archive (second producer)
```

"Done" = the three flows in Phase 5 run green against the trial tenant with
an empty DLQ.

## Affected files

VRSky (`~/Value-Retail/vrsky`):
- `src/cmd/business-central-consumer/service.go` — incremental cursor
  (persist last `lastModifiedDateTime` per connection; today the docs call
  it "a follow-up").
- `src/cmd/webhook-consumer/server.go` — accept a path suffix after
  `/webhook/{connectionId}` **or** leave alone and change the till (see
  Phase 2, pick one).
- `docs/connectors/business-central.md` — record the live-tenant results
  (the SAP page is the model: what was proven, with what config).
- `ui/src/onboarding/templates.ts` — a "Basket → BC" template once the event
  JSON is agreed (optional, demo value).
- New converter mappings (stored in connection config, exported as JSON
  fixtures under `src/test/fixtures/business-central/`).

Draupnir (`~/Draupnir`):
- `crates/outbox/src/http.rs` — the `/v1/till-events` path is hard-coded
  onto `base_url`; make it configurable **if** we choose the till-side fix.
- `crates/catalogue-api` — no change expected; it already exposes
  `PUT /v1/catalogue/items` and the staged import.

Bifrost (`~/Bifrost`):
- `module/bifrost/src/BasketClient.php` — "the one file VRSky replaces
  later". Phase 4 step 11.

Nothing in `infrastructure/` unless we decide to keep the cluster running
(Phase 0).

## Approach

### Phase 0 — Environment (people: Ludvik / boss; ~1 hour of clicking, then waiting)

0.1 **Sign up for the free BC trial** at dynamics.microsoft.com/business-central
    with a *company Entra account* (not gmail). Viral trial: no expiry while
    someone signs in; deleted after 45 idle days. Set Company Information →
    User Experience = Premium if we need manufacturing/service later.
0.2 **Register an Entra app**: Azure portal → App registrations → new;
    API permission *Dynamics 365 Business Central → API.ReadWrite.All*
    (application); create a client secret; note tenant ID + client ID.
0.3 **Grant it in BC**: search *Microsoft Entra Applications* → New → paste
    client ID → State Enabled → permission set `D365 BUS FULL ACCESS`.
0.4 **Find the company GUID**: `GET https://api.businesscentral.dynamics.com/v2.0/<tenant>/Production/api/v2.0/companies`
    (or Companies page → the id column).
0.5 **Decide where VRSky runs for the test.** Two options, both fine:
    - *Laptop / till PC, docker-compose* — zero cost, everything on
      `localhost`, no DNS, no Telenor problem. BC is reached *outbound* by
      the consumer, so BC never needs to reach us. Inbound webhooks (Basket,
      till outbox) come from the same machine or LAN. **Recommended for
      Phases 1–4.**
    - *AKS prod cluster* — `az aks start -g vrsky-prod -n vrsky-prod`
      (Ludvik runs it). Needed only for Phase 5 if the Basket is hosted
      elsewhere. Remember Telenor blocks `sslip.io`; a cloudflared quick
      tunnel from the editor is the stopgap.
0.6 Enter BC credentials **only in the VRSky editor** (they are minted into
    encrypted tenant secrets). Never paste them into chat, files, or git.

### Phase 1 — Prove BC ↔ VRSky on a real tenant (Claude; ~1 day after 0.x)

1.1 Deploy the onboarding template *ERP inventory → POS (Business Central →
    Sitoo)* but with an `http` producer pointing at a local httpbin/sink
    instead of Sitoo. Confirm: token acquired, `items` page fetched,
    `@odata.nextLink` followed on a >20-row entity, envelope published.
1.2 Use the *show data structure* preview (aux port 9310) against the trial
    — first time it runs on real data.
1.3 Fix whatever a real tenant reveals (expect: `$filter` quoting, 20-row
    default page size, `environment` casing, 429s during CRONUS bulk reads).
1.4 **Incremental cursor**: persist the max `lastModifiedDateTime` seen per
    connection; next poll adds `lastModifiedDateTime gt <cursor>`.
    Test the outcome, not the config string: publish twice, assert the
    second poll delivers only rows modified after the first
    ([[feedback_test_the_outcome]]).
1.5 Reverse direction smoke: `business_central` producer POSTs one item to
    the trial and it appears in the Items list.
1.6 Record results in `docs/connectors/business-central.md` the way
    `sap-s4hana.md` does.

Exit: BC consumer + producer both live-proven; cursor merged.

### Phase 2 — POS ↔ VRSky (Claude + Ludvik in Draupnir; ~2 days)

2.1 **Path mismatch**, pick one and do it:
    - (a) Draupnir: make `HttpSink` take the full URL (or a configurable
      path) instead of appending `/v1/till-events`. Smallest, keeps VRSky's
      ingress contract untouched. **Preferred.**
    - (b) VRSky: let the webhook ingress accept `/webhook/{id}/…` and
      ignore the suffix.
2.2 **Catalogue down**: connection *BC items → converter → http producer
    `PUT http://<catalogue-api>:8390/v1/catalogue/items`* with
    `Authorization: Bearer` + `X-Tenant-Id` headers. Converter maps
    `number → article_no`, `displayName → name`, `unitPrice → price_ex_vat`,
    `gtin`/`ean13` if present, VAT code from `taxGroupCode` or a constant.
    Then `cargo run -p till` with `CATALOGUE_URL` set and confirm the till
    pulls the new price (its shrink guard must *not* fire — send the full
    item list on first run via the staged import, deltas after).
2.3 **Sales up, demo shortcut** (till → VRSky directly, no Basket yet):
    `BASKET_URL=http://localhost:9100/webhook/<connId>` on the till. Confirm
    the outbox drains and envelopes arrive. This is for testing only — the
    architecture note's rule 1 says the till talks to the Basket, not to
    the integration layer. Don't demo it as the final shape.
2.4 **Converter to BC**: till sale → `salesOrders` deep insert
    (`customerNumber` = per-store walk-in customer, `salesOrderLines[]` with
    `lineType: Item`, `lineObjectNumber` = article_no, `quantity`,
    `unitPrice`). Put the till entry id in `externalDocumentNumber`.
    Switch to `salesInvoices` if open question 1 says so.
2.5 **Idempotency**: NATS dedup is 5 min; BC POST is not idempotent.
    Converter (or a tiny pre-check in the producer) does
    `GET salesOrders?$filter=externalDocumentNumber eq '<id>'` and skips on
    hit. Test with a forced redelivery (`MaxDeliver` path), assert one order
    in BC.
2.6 Seed the trial: one walk-in customer per store, items whose `number`
    equals the till's `article_no` set.

Exit: a sale rung on the till appears as one order in BC; a price changed
in BC appears on the till.

### Phase 3 — Basket ↔ VRSky (colleague + Claude; ~1 week, mostly theirs)

3.1 Colleague: outbound event emitter — POST JSON to a URL, `Idempotency-Key`,
    HMAC `X-Signature` (sha256, hex) over the body, retry on 5xx/429 with
    backoff, park 4xx. Same shape as Draupnir's outbox, which is a good
    reference implementation (`crates/outbox`).
3.2 Colleague: accept `POST /v1/till-events` from the till (the stub in
    `crates/basket-stub` shows the exact request).
3.3 Together: **agree the event JSON** — at minimum `basket.fulfilled`
    (lines with locked prices, store, tender, customer ref) and
    `order.placed`. Write it down in Bifrost `docs/contract.md`'s style.
3.4 Claude: one webhook connection per event type, HMAC secret minted in the
    editor, two producer branches: converter → `business_central`
    (`salesOrders`/`salesInvoices`), converter → `file`/`cloud_storage`
    (BI/archive). Multi-producer fan-out is supported
    (`TestValidateDAG_AllowMultipleProducers`; producers pick their turn
    from `_last_processed_by` + predecessor). If DLQ triage gets confusing,
    split into two connections.
3.5 Bifrost: change `BasketClient.php` to post to the VRSky webhook URL
    instead of the basket-svc directly; the module's 2 s/4 s timeouts and
    "never break a cart" rule stay.

Exit: web cart → Basket → VRSky → BC order, with the archive copy written.

### Phase 4 — Infrastructure (boss / IT; blocking for anything off-laptop)

4.1 Cloudflare admin → `vrsky.valueretail.no` A record → 20.251.107.2,
    cert-manager issues the cert, ingress host updated
    (`infrastructure/kubernetes/ui/ingress.yaml`,
    `…/ingress/webhooks-ingress.yaml`). Removes the Telenor block.
4.2 Decide cluster uptime for the pilot window; `cost-parking.md` has the
    numbers.
4.3 `ENCRYPTION_KEY` backup confirmed before any prod connection is created
    with BC credentials — losing it loses the secrets.

### Phase 5 — End-to-end on the trial tenant (~2 days)

5.1 Flow A: change an item price in BC → within one poll interval the till
    sells at the new price.
5.2 Flow B: ring a sale on the till → Basket → VRSky → exactly one BC order,
    `externalDocumentNumber` = till entry id; force a redelivery, still one.
5.3 Flow C: add to cart in the PrestaShop demo shop → Basket → VRSky → BC
    (intended line = quote or nothing; fulfilled line = order — per open
    question 3).
5.4 Kill the network between till and Basket mid-day, restore, assert every
    queued sale arrives once (the outbox's own test, now across three
    systems).
5.5 DLQ empty, Grafana dashboards show the three connections, write the
    tutorial page `docs/tutorials/retail-chain.md`.

## Risks

- **BC trial signup needs an Entra tenant** the company controls. If IT is
  slow, create a free standalone Entra tenant for the test — but then the
  trial is tied to a throwaway directory.
- **Duplicate orders in BC** if idempotency (2.5) is skipped. Cheap to build,
  expensive to clean up in an ERP.
- **Catalogue shrink guard** on the till refuses a first delta that looks
  like a tiny catalogue. First load must be a full staged import.
- **Colleague's timeline** — Phase 3 is the long pole and not ours.
- **Telenor + sslip.io** — anything hosted behind Telenor cannot reach the
  prod ingress until 4.1. Local compose sidesteps it entirely.
- **BC rate limits** (6 000 req / 5 min / user, 5 concurrent) — the
  per-order lookup in 2.5 doubles calls; fine at retail volumes, watch it if
  CRONUS bulk seeding is scripted.
- **The one-PR-per-chunk rule** — each phase's VRSky changes ship as one PR;
  Draupnir/Bifrost changes go in their own repos.

## How we verify it's done

- Phase 1: `docs/connectors/business-central.md` has a "Live-verified"
  section with the entity, page count and cursor behaviour, like SAP's.
- Phase 2: Draupnir `till` shows the BC price; BC Sales Orders list shows
  the till's receipt id in External Document No.
- Phase 3: the Basket's emitter is exercised by a contract test in the
  colleague's repo against VRSky's HMAC verifier (we can give them the
  verifier's test vectors).
- Phase 5: all five checks above pass; recorded with timestamps and
  connection IDs in the tutorial page.
- Standard gates every PR: `cd src && make test && make lint-tenant`,
  `cd ui && npm test && npx tsc --noEmit`, gofmt clean, CI green *before*
  merge (not after — #247 went red that way).

## Estimate

~3–4 weeks calendar. Claude's share is roughly 5 working days; the rest is
waiting on Phase 0 (BC signup), Phase 3 (colleague), Phase 4 (DNS).
