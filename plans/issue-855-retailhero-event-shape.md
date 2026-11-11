# #855: Retailhero to Bifrost template against what Retailhero publishes

Issue: ValueRetail/vrsky#855. Retailhero#2680 closed on 2026-11-11 (PRs
#2692, #2720).

## What was checked (read only, Retailhero main at b4001f4c)

- `infrastructure/local/receiving/config/nats/streams.json` (`partners`) and
  `docs/operations/local-receiving.md` ("VRSky partner accounts"): the partner
  account exports `product.version`, `pack.version`, `gtin.version`,
  `price.version` and `campaign.version` on
  `rh.evt.v1.<tenant>.<owner>.<type>`, one durable per tenant and type,
  `vrsky_<type>_<tenant>`, created by Retailhero's operator.
- `core/platform/src/platform/relay.rs` and
  `core/receiving/src/integration/outbox_relay/owners.rs`: the relay publishes
  each outbox row's payload unchanged, `Nats-Msg-Id` = event id.
- `contracts/events/master-data/v1.schema.json`,
  `contracts/events/pricing/v1.1.schema.json`: every event has top-level
  `contractVersion`, `eventType`, `event`, `stream`, `recordedAt`,
  `correlationId`, `fact`.
- `docs/contracts/pos-publication-bifrost-catalogue.md` and PR #2685: the
  Bifrost profile is a pure mapping with no transport; "Dispatch is not part
  of #2676".
- ADR-0063: the tills get the frozen publication content, never a price VRSky
  computes.

## Finding

None of the exported events is a Bifrost import batch, and the template
cannot build one from them (a join across event types, unapproved price
versions, VAT only in the frozen package). Nothing publishes the profile's
batches. So the converter paths, fixtures and subject cannot be matched to a
real event yet: there is none.

## What this change does

- `docs/connectors/retailhero-to-bifrost.md`: drops the "This shape is an
  assumption" warning. States what Retailhero publishes, with sources, why the
  template can't use it, and that the batch message is what VRSky needs from a
  Retailhero dispatch that does not exist yet. "Setting it up" says the
  partner account needs NKey, `JS.RH.API`, the inbox prefix and
  `create_durable: false`, which come with PR #859. "What blocks it" names the
  missing dispatch.
- `ui/src/onboarding/templates.ts`: the summary and the stream, durable and
  subject help say the same. No node config changes, so nothing depends on
  PR #859.

## Decisions

- The PR does not close #855. Its acceptance criteria assume a real event to
  match, and there is none. It refs #855, and #855 stays open, blocked on a
  Retailhero dispatch event.
- The converter, fixtures and goldens stay as they are: they are Retailhero's
  own profile output and still describe the batch content.
- The template keeps `auth_type: user_password` until PR #859 merges, so this
  PR can merge in any order.

## Verification

UI: tsc, lint, build, coverage (templates.test.ts covers the template). Go is
unchanged; the template tests still pass against the same config.
