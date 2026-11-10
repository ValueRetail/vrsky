# Salesforce producer: numbers keep their digits, nulls behave the same on REST and Bulk (#765)

## Open questions

None. Decisions are listed below.

## Goal

The Salesforce producer sends what the payload says. Numbers are not
reformatted, and an explicit null clears a field on a Bulk upsert as it does
on REST.

## Acceptance criteria

1. Records are decoded with `json.Number`; REST bodies, the external-id part
   of an upsert URL and Bulk CSVs carry the original digits (1000000,
   12345678, 9007199254740993, 0.10).
2. A JSON null on a Bulk `upsert` is `#N/A` in the CSV. Absent fields stay
   empty (unchanged).
3. A JSON null on a Bulk `insert` is an empty field (decision below), and
   the docs say so.
4. An `upsert` node with no `external_id_field` fails the delivery
   (retried, then DLQ) instead of acking and dropping.
5. Tests assert the exact REST body, URL and uploaded CSV, for both
   operations; a test pins null vs absent.
6. `docs/connectors/salesforce.md` describes numbers and nulls on each path.

## Touched

- `src/cmd/salesforce-producer/service.go` (decode, `csvValue`, `csvTable`,
  empty external id), `bulk.go` (passes `op` to the CSV code; `sendable`
  treats a null single column as nothing to send).
- `src/cmd/salesforce-producer/payload_test.go` (new), `bulk_test.go`
  (signature).
- `docs/connectors/salesforce.md`.

## Salesforce behaviour relied on

Bulk API 2.0, "Prepare CSV Files" (Salesforce Bulk API 2.0 developer guide):
empty field values are ignored when records are updated; to set a field to
null use `#N/A`. REST: a JSON `null` in the sObject body sets the field to
null. I could not fetch the Salesforce docs page from the worker (it is
rendered client-side), so the `#N/A` rule is quoted from the issue and the
guide as I know it, not re-read live. It is not tested against an org.

## Decisions

- **Insert + null on Bulk = empty field**, not `#N/A`. `#N/A` is documented
  for updates; behaviour on insert is not, and a new record has nothing to
  clear. The remaining difference to REST (a field default applies on Bulk,
  REST `null` overrides it) is documented.
- **Upsert with no `external_id_field`** returns a retriable error (DLQ after
  five attempts) rather than `sdk.Permanent`, which acks and logs and would
  drop the message again. Refusing at start in the management API was not
  chosen: that is outside this package.
- **Trailing data after the JSON value** stays an error (as `json.Unmarshal`
  gave), via a check after `Decoder.Decode`.
- **Single-column null on upsert** is "nothing to send", as an empty value was
  before; otherwise `#N/A` would make it a sendable row that blanks the
  external id.
- A literal `#N/A` string value clears the field on a Bulk upsert. Salesforce
  has no escape; documented, not handled.

## Risks

- Result matching on Bulk compares uploaded values with the values in
  Salesforce's result rows. `#N/A` is expected to be echoed as uploaded; if
  Salesforce echoed it as empty, a refused row with a null would not match
  and the delivery would fail as "unreadable results" (no resend, DLQ).
  Not verified against an org.

## Verification

`go build`, `go vet`, `golangci-lint`, `go test -race` on
`cmd/salesforce-producer`, `make lint-tenant`, `make lint-connector`;
mutation: removing `UseNumber` or the `#N/A` branch fails the new tests.
