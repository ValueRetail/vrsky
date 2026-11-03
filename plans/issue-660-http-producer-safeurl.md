# HTTP producer: refuse private destinations (#660, part of #639)

Open questions: none. Calls made without asking are under "Decisions".

## Problem

The HTTP producer sends every envelope to `http.url` from node config, with the
node's custom headers and, for `auth_type=oauth`, a bearer token. It uses
`http.DefaultTransport` (or a clone of it for mTLS), so a tenant can aim it at
the cluster network or the metadata service (`169.254.169.254`,
`168.63.129.16`) and read the answer back through the activity feed. The same
client follows any redirect, so a public host can bounce it onto a private one,
and Go forwards custom headers on a cross-host redirect. It also honours proxy
environment variables.

#639 moves one connector per PR onto `pkg/safeurl`. The API consumer went first
(#646, `plans/issue-639-api-consumer-safeurl.md`). This is the HTTP producer.

## Acceptance criteria

1. Every outbound request, mTLS or not, goes through a `safeurl` client that
   refuses loopback, private, link-local (metadata), CGNAT, multicast and
   unspecified addresses at dial time, on the address actually connected to, so
   a hostname that resolves to one is refused. The buffered path, the streamed
   path (`DeliverStream`) and the OAuth retry after a 401 all use it.
2. A node with `http.tls` keeps working: the client certificate is presented and
   the configured CA is the one trusted, on top of the same checked dialer.
3. Public `http://` stays allowed. Redirects: same host only, never https to
   http, at most five; anything else is an error and nothing is forwarded.
4. A `url` that is not an absolute http(s) URL, or is a literal blocked IP, is
   refused before any request: by the management API when the connection is
   started (with the reason), and by the producer on every delivery.
5. `ALLOW_PRIVATE_UPSTREAMS=true` (operator only) lifts the address check, as
   for the API consumer and Sitoo.
6. Tests fail without the change; the connector page lists what stops working.

## Files

- `src/pkg/safeurl/safeurl.go`: `Options.TLSConfig`, passed to the transport.
- `src/cmd/http-producer/main.go`: `allowPrivate` read in `Configure`;
  `newClient` (safe client plus otelhttp, optional TLS config) used for the
  default client and the mTLS client; `clientFor`; `CheckRequestURL` at the top
  of `sendHTTPRequest`.
- `src/pkg/managementapi/nodeconfig.go`: `validateHTTPProducerURL`.
- Tests: `src/cmd/http-producer/ssrf_test.go` (new), existing fixtures that
  deliver to httptest on 127.0.0.1 set the dev flag, `src/pkg/safeurl`,
  `src/pkg/managementapi/nodeconfig_api_test.go`.
- `docs/connectors/http.md`, `docs/SECURITY.md`, `plans/backlog.md` (item 147),
  `docker-compose.yml` (dev flag on `http-producer`).

## Decisions

- Same policy as the API consumer: http stays allowed, same-host redirects.
  SSRF is about which address is reached and the dial enforces that whatever
  the scheme. Some partners only speak http.
- `Options.TLSConfig` on `safeurl.NewClientWith` rather than patching the
  transport in the producer. The producer still wraps the transport in
  otelhttp, so traces and `traceparent` are unchanged.
- A refused delivery returns an ordinary error, not `sdk.Permanent`. Permanent
  acks and drops the message; an ordinary error is retried on the backoff and
  then dead-lettered with its payload, so nothing is lost when a tenant fixes
  the URL and replays. No request is made, so the retries cost nothing. The
  activity feed gets an error event.
- The producer has no start hook (configs load lazily per connection), so the
  early check is in `sendHTTPRequest` and in the management API at start. The
  dial is the enforcement.
- Proxy environment variables are now ignored (a proxy would turn the check
  into one on the proxy). No deployment sets them for the producer.
- Unconfigured producers (struct literals in tests) still get a guarded
  client, built on first use from the `allowPrivate` field; the env var is read
  only in `Configure`.
- `docker-compose.yml` sets `ALLOW_PRIVATE_UPSTREAMS` (default true) on
  `http-producer` only. `make up-core` and the load harness send to the bundled
  `httpbin` at `http://httpbin/post`, a private address; without it the first
  pipeline in the dev stack would dead-letter. Not a production file.

## Risks

- Existing production connections that deliver to a private or in-cluster
  address, or whose partner redirects to another host, stop delivering after
  the deploy. The docs list both. Messages for them go to the DLQ, not lost.
- `http.DefaultTransport` settings that the mTLS client used to inherit (proxy,
  dial and idle timeouts, HTTP/2) are now the safeurl transport's: same dial
  timeout and keep-alive, `ForceAttemptHTTP2`, 10 idle conns, 90 s idle
  timeout, 10 s TLS handshake. Only the proxy differs.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1 ./cmd/http-producer/...
./pkg/safeurl/... ./pkg/managementapi/...`, `make lint-tenant`.

Mutation: replace `newClient` with a plain `http.Client` (default transport,
mTLS config copied in) and delete the `CheckRequestURL` call in
`sendHTTPRequest`; the new tests must fail.
