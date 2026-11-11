# Template: Retailhero articles and prices to Bifrost tills (#816)

Open questions: the Retailhero event shape (see Risks). Calls made without asking are under "Decisions".

## Goal

An onboarding template that moves Retailhero articles and prices into the Bifrost catalogue: `nats` source, a data-converter, the `bifrost_catalogue` producer in `delta` mode. Documented mapping, VAT per item, single-master rule.

## Acceptance criteria

- Template `retailhero-to-bifrost` in `ui/src/onboarding/templates.ts`, beta, with the mapping documented in `docs/connectors/retailhero-to-bifrost.md`.
- VAT as a rate per item: documented how it travels and what happens while Retailhero has no rate.
- End to end test from Retailhero's fixtures to a stubbed catalogue-api: converter on the embedded harness, producer against its stub.
- Docs say only one master may feed a store and that the BC to Bifrost price path is off for those stores.
- UI and Go checks pass.

## Touched

`ui/src/onboarding/{templates.ts,templates.test.ts,testdata/}`, `ui/src/types/nodeConfig.ts` (a `consumer/nats` entry), `src/cmd/data-converter` and `src/cmd/bifrost-catalogue-producer` (new test files only), `src/test/fixtures/retailhero/`, `docs/connectors/`, `mkdocs.yml`, `plans/backlog.md`.

## Sources read

Retailhero PR #2685 (merged): `docs/contracts/pos-publication-bifrost-catalogue.md`, fixtures `expected-full.json`, `expected-delta.json`, `sender.json`. Retailhero#2675 (transport), #2680 (partner export), #2687 (VAT rate). Bifrost `format.rs` (`ImportItem`), #249 and PR #252.

## Decisions

- The profile emits rows both as `CatalogueItem` (`vat_rate`) and `importItem` (`vat_code`, the wire form). The template reads `importItem`: that is what the import endpoints take, and Bifrost's wire carries codes.
- One message is one import batch. A full package gives one article several rows with different `effective_from`; Bifrost keeps one row per article per import, so they must not share a message.
- Converter is `each` + `rename` only. `to_number` goes through a float; prices stay strings.
- Mode `delta`. `full` replaces the catalogue with one message (#841 would collect a whole list).
- `deliver_policy: new` on the NATS source, so a start does not replay the stream.
- The template has no VAT field. The rate is the sender's input on the Retailhero side until Retailhero#2687; a product without one is refused there. Nothing defaults it here.
- `user_password` auth, because the wizard's secret field is one line and a `.creds` file is not.
- Servers asked through `nats.servers.0` (the same pattern as `api.endpoints.0`).
- The Go end-to-end is two tests joined by golden files, since the converter and the producer are separate `main` packages: converter output must equal the golden, the golden goes through the producer.

## Risks

- The event shape on the partner account is not decided (Retailhero#2680). The fixtures are the profile output cut per batch; if the event differs, the converter paths change. Hence beta.
- Bifrost does not accept `price_incl_vat` yet (#249, PR #252). Hence beta.

## Verification

`go test -race` on both packages, `go vet`, `golangci-lint`; UI `tsc -b`, lint, build, `vitest --coverage`. Mutation: price rename changed to `to_number` fails the Go test.
