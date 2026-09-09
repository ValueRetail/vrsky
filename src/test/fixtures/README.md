# Retail connector contract fixtures

One directory per retail integration, each holding the golden envelopes its
consumer publishes. See `src/test/contract` for what these are and why.

Each integration is two binaries that never call each other — they meet only in
the envelope one publishes and the other consumes. The goldens here ARE that
meeting point:

- `cmd/<name>-consumer/*_contract_test.go` runs the real consumer against a stub
  vendor API and writes `<name>/envelopes.golden.json`.
- `cmd/<name>-producer/*_contract_test.go` replays exactly those envelopes
  through the real producer and asserts what reaches the wire.

A consumer change that alters the envelope shows up as a diff here, and as a
failure on the producer side if the producer can no longer send it.

Regenerate after an intentional consumer change — and check the producer test
still passes before committing the new golden:

    go test ./cmd/<name>-consumer/ -run Contract -update

## What each one pins

| connector | modes | the normalisation being held still |
|---|---|---|
| sitoo | poll, webhook | poll emits a JSON **array** of records; the webhook forwards Sitoo's event body verbatim, an **object**. The producer POSTs whichever it gets. |
| front-systems | webhook | webhook-only — no polling path exists. |
| brightpearl | poll, webhook | poll publishes the unwrapped `response`; the webhook forwards the callback body. |
| business-central | poll | the OData `{"@odata.context":…,"value":[…]}` wrapper is stripped to the `value` array. |
| visma | poll-array, poll-object | Visma answers some resources with an array and others with a single object; the consumer wraps the latter so the producer always sees an array. |
| sap-s4hana | poll-odata-v2, poll-odata-v4 | v2 nests records in `{"d":{"results":[…]}}`, v4 in `{"value":[…]}`; both are unwrapped to the same array. |

Those normalisations are the reason each producer has one write path. Nothing
else in the suite checks them end to end.
