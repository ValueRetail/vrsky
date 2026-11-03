# SAP producer: session cookies per delivery, not per process (#682)

Open questions: none. Calls made without asking are under "Decisions".

## Problem

`sapProducer.Configure` builds one `http.Client` (on `safeurl.NewClient`
since #678) with one `cookiejar` and every delivery of every connection and
tenant uses it. The jar keys cookies by host, so two connections that write
to the same SAP host share the session cookie SAP sets on the CSRF fetch
(`SAP_SESSIONID_<SID>_<client>`, possibly `MYSAPSSO2`). Connection B's
requests then carry connection A's session. If SAP honours the session over
the request's own credential, B's write runs as A's SAP user; if not, CSRF
tokens and sessions still cross tenants.

## Acceptance criteria

1. Session cookies never leave the connection (and so the tenant) they were
   set for: a cookie SAP sets on a request of connection A is never sent on a
   request of connection B, sequentially or under concurrent deliveries.
2. The CSRF flow still works: fetch (token plus session cookie), then the
   write with the token and that session cookie; a `403 X-CSRF-Token:
   Required` re-fetches once in the same session.
3. The client keeps the `safeurl` dialer, the timeout and the no-redirect
   policy. The transport (and its connection pool) stays shared.
4. The OAuth2 token call goes through the same per-delivery session.
5. Table-driven test with two connections of two tenants on one fake SAP
   host that sets a distinct session cookie per CSRF fetch and checks each
   request's cookies against the request's credential; it fails with a
   shared jar (mutation run).
6. Docs: the SAP connector page says how sessions are scoped.

## Decisions

- A fresh cookie jar per delivery, not a jar per connection. The producer
  does not cache CSRF tokens across deliveries (each `write` fetches one), so
  a per-connection jar would buy nothing and would need a bounded map, a
  mutex and cleanup. Per delivery is also stricter: two deliveries of the
  same connection do not share a session either. Round trips are unchanged
  (fetch then write, as today).
- `sessionClient()` copies the configured `http.Client` (same `Transport`,
  `Timeout`, `CheckRedirect`) and gives the copy a new jar. The shared client
  built in `Configure` no longer has a jar, so nothing can fall back to a
  process-wide one.
- The OAuth2 authorizer gets the session client too. `oauthcc.Client` is
  already built per delivery, so the bearer token was never shared across
  connections; this only keeps any cookie the token endpoint sets inside the
  delivery.
- `write`, `fetchCSRF` and `send` take the session client as a parameter.
  Existing tests that call `write` directly pass `p.sessionClient()`.
- `TestConfigure_ClientKeepsTheCookieJar` asserted the old design (a jar on
  the shared client). It is replaced by a test that the session client has
  its own jar, a new one per call, the shared transport, and still follows
  no redirect.

## Files

- `src/cmd/sap-s4hana-producer/service.go`
- `src/cmd/sap-s4hana-producer/session_test.go` (new),
  `producer_test.go`, `sap_contract_test.go`, `configure_client_test.go`
- `docs/connectors/sap-s4hana.md`

Not touched: the consumer (it does no CSRF and has no jar), `pkg/safeurl`,
`pkg/oauthcc`, `mock-sap`.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=3 ./cmd/sap-s4hana-producer/...`,
`go test -race -count=1 ./cmd/sap-s4hana-consumer/... ./cmd/mock-sap/...`,
`make lint-tenant`. Mutation: make `sessionClient` hand out one shared jar;
the new test must fail.
