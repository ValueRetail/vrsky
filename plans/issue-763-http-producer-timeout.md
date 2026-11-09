# HTTP producer request timeout (#763)

## Finding

The issue says the producer's client has no timeout. It has: `newClient`
passes 30 s to `safeurl.NewClientWith` (`http.Client.Timeout`, which covers
connect, TLS, waiting for the answer and the body), and the `safeurl` dialer
has a 30 s dial timeout plus a 10 s TLS handshake timeout. What is missing is
a test, a way to shorten it in tests, and docs.

## Goal

Pin the behaviour: a target that accepts and never answers fails the delivery
within the bound, as a retriable error, and does not mark the node as taken.

## Acceptance

- Default request timeout 30 s, named constant, overridable on the producer
  struct (tests only). Per-node config: not added. The node editor has no
  timeout field, so it would be a UI, schema and API change for little value;
  left for later.
- `safeurl` dial timeout: already 30 s, unchanged; private-IP refusal untouched.
- Timeout error is not `Permanent`; `Deliver` returns `sdk.Retriable` (NAK, DLQ
  after 5 attempts); with two nodes the timed-out one is retried and the one
  that answered is skipped (#757).
- Test with a hanging httptest server, 200 ms timeout.
- Docs: `docs/connectors/http.md`, troubleshooting HTTP row.

## Files

`src/cmd/http-producer/main.go`, `src/cmd/http-producer/timeout_test.go`,
`docs/connectors/http.md`, `docs/operator/troubleshooting.md`.

## Risks

Low. Default behaviour is unchanged (30 s before and after).

## Verification

`go build/vet`, `golangci-lint`, `go test -race -count=1` on the two packages,
`make lint-tenant`, `make lint-connector`. Mutation: client timeout set to 0,
the no-answer test hangs and fails.
