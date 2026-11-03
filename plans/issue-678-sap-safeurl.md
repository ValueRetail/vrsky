# SAP S/4HANA connectors: check host, api_base_url and token_url with safeurl (#678)

Open questions: none. Calls made without asking are under "Decisions".

Part of #639 (`plans/backlog.md` item 147). Prior art: Sitoo (#616), the HTTP
producer (#660, PR #669) and Brightpearl (#667, PR #674).

## Problem

The SAP S/4HANA consumer and producer build their URLs from node config:
`https://{host}/sap/opu/odata/sap/{service}` or an `api_base_url` override,
and for OAuth2 `https://{host}/sap/bc/sec/oauth2/token` or a `token_url`
override. Every request carries the tenant's Basic credential or bearer
token (and the token call carries the client secret). Both connectors use a
plain `http.Client`, so a tenant can aim them at the cluster network or the
cloud metadata service. `host` goes into the URL unescaped, so a value like
`x@10.0.0.1/` or `evil.example/` moves the request to another host while
looking like the default.

## Acceptance criteria

1. Both connectors build their HTTP client with `safeurl.NewClient` in
   `Configure` (the producer keeps its cookie jar on it), so loopback,
   private, link-local (metadata included), CGNAT, multicast and the Azure
   platform address are refused at dial time, also when a hostname resolves
   to one. The OAuth2 token call uses the same client. No redirects are
   followed.
2. Where it is used, `host` must be one host name with an optional numeric
   port: no scheme, path, userinfo, query, fragment or whitespace.
3. The service root (`api_base_url`, or the URL built from `host`) and, for
   OAuth2, the token URL (`token_url`, or the one built from `host`) pass
   `safeurl.CheckBaseURL`: https, a host, no userinfo, query or fragment, no
   literal blocked IP.
4. Where the check runs:
   - consumer: in `startConnection` (no poller, error logged) and in the
     sample-data endpoint (`ok:false` with the reason; a dial refusal answers
     "target address not allowed" and the address is only logged);
   - producer: on every delivery, as a normal error (not `sdk.Permanent`),
     so the message is retried and then dead-lettered with its payload.
5. The default cloud host (`my347623.s4hana.ondemand.com`) keeps working.
6. `ALLOW_PRIVATE_UPSTREAMS=true` (operator only) allows `http` and private
   addresses, for `mock-sap`. docker-compose sets it on both SAP services so
   the dev stack and the nightly end-to-end (which point SAP at
   `http://mock-sap:8099`) keep working.
7. Table-driven tests that fail without the change; docs updated.

## Decisions

- The start-time check lives in the connectors (consumer start path,
  sample-data, producer `Deliver`), not in the management API, which other
  PRs are changing. The dial is the enforcement; the string check is the
  early, readable error.
- A producer refusal is a plain error. `sdk.Permanent` acks and drops the
  message (`pkg/sdk/lifecycle.go`); a plain error is NAKed, backed off and
  dead-lettered with its payload, so a fixed config can replay it. A dial
  refusal already comes back wrapped in `sdk.Retriable` from the existing
  CSRF/send paths, which is also not Permanent.
- `host` may carry a numeric port (`sap.example.com:44300`): on-prem
  gateways listen on 443xx and the field has never refused one. IPv6
  literals are refused in `host`; use `api_base_url` for those.
- `host` is only checked where it is used: always when there is no
  `api_base_url`, and for OAuth2 when there is no `token_url`.
- The OData pagination link (`__next` / `@odata.nextLink`) stays on the
  service root's origin (changed after review): every page carries the
  credential, and a link to a public host, or an `http` link behind a
  TLS-terminating web dispatcher, would leak it. Same scheme, host and port:
  followed (user name and fragment dropped). Another origin with a path under
  the service root's path (after `path.Clean`; `%2e`, `%2f`, `%5c` and `\`
  refused, so a server cannot decode its way out of the root): the path and
  query are fetched from the configured origin, which keeps gateways that
  answer with an internal host name working. Anything else: the poll stops with an error that names neither the
  link nor the credential. mock-sap answers with a relative `?$skiptoken=P2`,
  which resolves onto the same origin, so the nightly end-to-end is
  unaffected.
- A producer request refused at dial returns
  `sdk.Retriable("sap s/4hana: " + safeurl.ErrBlockedAddress)` without the
  address, and logs the full error (changed after review): the error becomes
  the message's last error, which the tenant sees in the DLQ view. The
  consumer's poll errors only go to the service log; its sample-data endpoint
  already answers "target address not allowed".
- Both connectors log a warning at `Configure` when `ALLOW_PRIVATE_UPSTREAMS`
  is on, as the API consumer does.
- docker-compose sets `ALLOW_PRIVATE_UPSTREAMS: "${ALLOW_PRIVATE_UPSTREAMS:-true}"`
  on `sap-s4hana-consumer` and `sap-s4hana-producer`, as #669 does for the
  HTTP producer. docker-compose is dev and CI only; the Kubernetes manifests
  do not set it. `.github/workflows/e2e-nightly.yml` is not changed.
- Small duplicated check per binary, as Sitoo and Brightpearl do; no change
  to `pkg/safeurl`.

## Files

- `src/cmd/sap-s4hana-consumer/service.go`, `server.go`
- `src/cmd/sap-s4hana-producer/service.go`
- tests: new `safeurl_test.go` and `configure_client_test.go` in both, and
  `pagination_test.go` in the consumer; the start fixture that serves on
  `http://127.0.0.1` sets `allowPrivate`.
- `docker-compose.yml` (the two SAP services)
- `docs/connectors/sap-s4hana.md`

Not touched: `src/pkg/managementapi`, `src/pkg/safeurl`, `docs/SECURITY.md`,
`plans/backlog.md` item 147, `.github/workflows`.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`,
`go test -race -count=1 ./cmd/sap-s4hana-consumer/... ./cmd/sap-s4hana-producer/... ./cmd/mock-sap/...`,
`make lint-tenant` (repo root). Fail without the fix: stash the service
changes, keep the tests, run them.
