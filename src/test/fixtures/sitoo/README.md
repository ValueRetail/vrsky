# Sitoo contract fixtures

`transactions_page.json` is one page of a Sitoo `transactions` collection, shaped
like the real API response (`totalcount` + `items`). It is the single source of
truth for the consumer→producer contract tests:

- `src/cmd/sitoo-consumer/sitoo_contract_test.go` serves it from a stub Sitoo API,
  runs the real consumer, and records the envelopes it publishes into
  `envelopes.golden.json`.
- `src/cmd/sitoo-producer/sitoo_contract_test.go` reads that golden file and
  feeds those envelopes to the real producer, asserting what reaches the wire.

The golden file is written by the consumer test with `-update` and committed, so
a change to what the consumer emits shows up as a diff there AND as a failure in
the producer test if the producer can no longer handle it. That is the join the
two services meet at, and nothing else in the suite covers it — they are separate
binaries that only ever talk through NATS.

Regenerate after an intentional consumer change:

    go test ./cmd/sitoo-consumer/ -run TestSitooContract -update
