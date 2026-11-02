# API consumer: refuse private destinations (#639, first connector)

Open questions: none. Calls made without asking are under "Decisions".

## Problem

The API consumer builds its request URL from node config (`base_url` plus an
endpoint `path`, which can itself be a full URL) and sends the node's bearer
token, API key or OAuth access token with it. It uses a plain `http.Client`,
so a tenant can aim it at the cluster network or the cloud metadata service
(`169.254.169.254`, `168.63.129.16`) and read the answer back through the
pipeline or the sample-data endpoint. The same client follows redirects, so
a public host can bounce it onto a private one, and Go forwards custom
headers such as `X-API-Key` on a cross-host redirect.

#616 added `pkg/safeurl` for Sitoo. #639 asks for the rest, one connector per
PR. This one is the API consumer. The others are listed at the end.

## Acceptance criteria

1. The API consumer's client refuses loopback, private, link-local (including
   the metadata addresses), CGNAT, multicast and unspecified addresses at dial
   time, on the address actually connected to, so a hostname that resolves to
   one is refused. This holds for polls, the post-401 OAuth retry and the
   sample-data endpoint.
2. Public `http://` APIs keep working. Redirects: same-host only, never
   https to http, at most 5; any other redirect is an error, not followed.
3. A connection whose `base_url` or endpoint URLs are not absolute
   http/https URLs with a host, or are a literal blocked IP, is not started by
   the consumer, and the management API refuses it when the connection is
   started, with the reason in the error.
4. `ALLOW_PRIVATE_UPSTREAMS=true` (operator only) lifts the address check for
   local development, as for Sitoo.
5. Tests fail without the fix; docs list what stops working after the deploy.

## Decisions

- http stays allowed. SSRF is about which address is reached, and that is
  enforced at dial time whatever the scheme. https-only would break public
  APIs that only speak http (open-data feeds, older vendor APIs) and buy
  nothing against SSRF. The cost is a credential that crosses the network in
  clear text on an http endpoint; that is the tenant's own credential to the
  tenant's own endpoint, and the docs now say so. `safeurl.CheckBaseURL`
  (https only) is unchanged for Sitoo. New: `CheckRequestURL` (http or
  https), and `Options.SameHostRedirects` on a new `NewClientWith`.
- Redirects: follow same-host hops, refuse the rest. Plain refusal would turn
  every http to https upgrade and trailing-slash redirect on a public API
  into a failed poll. Following cross-host redirects would forward
  `X-API-Key` to a host the tenant did not name. The dial-time check still
  runs on every hop, and each hop is re-checked for scheme and literal IP.
- Userinfo and query strings in the URL are allowed here (the query is
  `params`; `https://user:pw@host` is the tenant's own basic auth). Not an
  SSRF vector.
- The string check is the readable early error; the dial is the enforcement.
  Both the consumer and the management API build the URL with one shared
  helper (`safeurl.EndpointURL`), so they cannot disagree about what is
  called.
- The client ignores proxy environment variables, as `safeurl.NewClient`
  does. The api-consumer deployment sets none.

## Files

- `src/pkg/safeurl/safeurl.go`, `safeurl_test.go`: `CheckRequestURL`,
  `EndpointURL`, `Options`, `NewClientWith`, same-host redirect policy.
- `src/cmd/api-consumer/service.go`, `api_poller.go`, `server.go`: safe
  client built in `Configure`, `allowPrivate`, URL check at start and in
  sample-data.
- `src/pkg/managementapi/nodeconfig.go`: check at connection start.
- Existing api-consumer fixtures that use httptest on 127.0.0.1 set
  `allowPrivate` / the dev flag.
- `docs/connectors/api.md`, `docs/SECURITY.md`, `plans/backlog.md` (item 147).

## Verify

`go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race` on `pkg/safeurl`, `cmd/api-consumer`, `pkg/managementapi`,
`make lint-tenant`. Mutation: a plain `http.Client` in `Configure` fails the
new Configure tests; removing the redirect check fails the redirect tests.

## Still open for #639 (one PR each)

- HTTP producer: `http.url` is free-form and the most exposed after this.
  Same client and `CheckRequestURL`, plus the management API check at start.
  It builds its own `http.Client` (also for mTLS), so the dial guard has to
  go in there too. It sends a body, so look at what a refused delivery does
  (poison to the DLQ, not retried forever).
- Visma: `base_url` and `token_url`. Both need `CheckBaseURL` and the safe
  client (the token request carries the client secret).
- Front Systems: `base_url`, and `callback_url` if it is called out.
- Brightpearl: `base_url` override (the derived one comes from datacenter and
  account code, which should be validated as host labels).
- SAP S/4HANA: `host`, `api_base_url` and `token_url`.
- Business Central: `api_base_url` and `token_url` overrides. The #616
  audit did not name it; it has the same shape.
