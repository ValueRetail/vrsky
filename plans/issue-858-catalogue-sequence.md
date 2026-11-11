# #858: Bifrost catalogue producer sends a sequence

Issue: ValueRetail/vrsky#858. Bifrost side: ValueRetail/Bifrost#253
(`docs/catalogue-doors.md`, "Deliveries that arrive out of order").

## Goal

A retried older list must not commit after a newer one and overwrite the
tills' prices. Bifrost's `begin` takes an optional `sequence` that only grows,
and commit answers 409 when the tenant already has that sequence or a higher
one. The producer sends one and treats that 409 as "drop and log".

## Acceptance criteria

- `begin` sends the message's sequence when it has one.
- A 409 at commit for a message with a sequence is acked (dropped) and logged
  as a warning, not retried or dead-lettered.
- Stub tests: a newer import already committed, and the same sequence twice
  (a redelivery after a lost ack). A runner test shows no DLQ copy.
- `docs/connectors/bifrost-catalogue.md`: the ordering section says the guard
  closes the retry case.
- The Retailhero to Bifrost template passes the sequence through.

## Where the sequence comes from (decision)

The source event's time, not a VRSky stream sequence. A DR restore or a
stream rebuild resets a VRSky stream's sequence numbers, and every later
import would then get 409 and be dropped. The source's time keeps growing.

The converter can copy a timestamp but cannot turn it into a number, so the
producer reads `sequence` from the converted message (`{"items": [...],
"sequence": ...}`) in two forms:

- a whole number from 1, sent as is (for a source with its own counter);
- an RFC 3339 timestamp, sent as Unix milliseconds (Bifrost's own example).

Absent or `null`: the import is unnumbered, as before. Anything else is
refused before Bifrost is called, like a payload that is not a list of items
(Permanent: the same message is refused again).

The template maps `recordedAt` to `sequence`. Every Retailhero event contract
under `contracts/events/` carries the time it was recorded as top-level
`recordedAt`. Retailhero does not publish the Bifrost batches yet (see #855),
so this is the field a batch event will carry if it follows those contracts. A
batch without it goes unnumbered.

## Risks

- Two messages with the same millisecond count as one delivery: the second is
  a 409 and dropped. Retailhero's `recordedAt` is the writing transaction's
  start time, so batches written in one transaction would share it. The doc
  says the batches need a `recordedAt` that grows per batch; a source that
  cannot promise that should send a counter.
- A 409 without a sequence is not the guard (a proxy, an API change). It stays
  a refusal (Permanent), not a silent success.
- A message with several Bifrost nodes: each Bifrost tenant has its own guard.
  On a retry the node that already committed gets 409 and is skipped, which
  is the right outcome.

## Files

- `src/cmd/bifrost-catalogue-producer/{service,client}.go`
- `src/cmd/bifrost-catalogue-producer/{stub_test,sequence_test,retailhero_template_test}.go`
- `src/cmd/data-converter/retailhero_template_test.go`
- `ui/src/onboarding/templates.ts`, `ui/src/onboarding/testdata/retailhero-to-bifrost.json`
- `docs/connectors/bifrost-catalogue.md`, `docs/connectors/retailhero-to-bifrost.md`

## Verification

Go: build, vet, gofmt, golangci-lint, lint-openapi, lint-tenant,
lint-connector, `go test -race -count=1 ./...` against a local Postgres 16.
UI: tsc, lint, build, coverage. Mutation runs on the 409 handling, the begin
body and the epoch check.

## Review round 1 (fresh reviewer, changes requested)

- A delta refused with 409 loses its own rows when the newer import held other
  articles (the guard is per tenant). That is Bifrost's design, and the issue
  asks for the drop. Kept, but the drop in delta mode is now logged as an
  error, and the docs say what is lost and how to recover. Whether this
  trade-off is right for the template's delta traffic is for a human (see the
  PR).
- DLQ replay of a sequenced message is usually refused: documented.
- The template's `recordedAt` collision is now a go-live blocker in the doc.
- Numbers through a converter stay below 2^53; clocks must not step back:
  documented.
- `parseSequence` reads exact keys, like `parseItems`. The 409 error text is
  neutral. The bad-sequence log carries tenant and connection.
