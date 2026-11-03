# Visma connectors: refuse private destinations (#661, part of #639)

Open questions: none. Calls made without asking are under "Decisions".

## Problem

The Visma consumer and producer take `base_url` and `token_url` from node
config. They send the tenant's client secret to `token_url` and the bearer
token to `base_url`, through a plain `http.Client` (`&http.Client{}`, and the
`oauthcc` default for the token request). A tenant can aim either at the
cluster network or the cloud metadata service. `base_url` and `token_url` are
only checked for being non-empty. #616 (Sitoo) and #646 (API consumer) fixed the
same hole with `pkg/safeurl`.

## Acceptance criteria

1. `base_url` and `token_url` go through `safeurl.CheckBaseURL` (https only, no
   userinfo/query/fragment, no literal private IP) when a connection starts:
   the consumer's `startConnection` and sample-data endpoint, the producer's
   `Deliver`, and the management API when the connection is started (the
   user-facing error).
2. Every HTTP client the connectors use, API and token request, comes from
   `safeurl.NewClient`, so a private, loopback, link-local or metadata address
   is refused at dial time, including a hostname that resolves to one.
3. Unset `token_url` still means `https://connect.visma.com/connect/token`; the
   real Visma hosts work as before.
4. `ALLOW_PRIVATE_UPSTREAMS=true` lifts both checks (http and private
   addresses) for local development.
5. Tests that fail without the change, for consumer, producer and management
   API. Docs updated.

## Decisions

- One shared check, `pkg/vismaurl.Check(baseURL, tokenURL, allowPrivate)`, used
  by the consumer, the producer and the management API, so they cannot
  disagree. It lives in its own small package; `pkg/safeurl` is unchanged
  (another worker is using it for the HTTP producer).
- `token_url` goes through `CheckBaseURL` too. Its message names `base_url`, so
  the helper rewrites the field name to `token_url`.
- https only, no public-http exception (unlike the API consumer): Visma
  Connect and every Visma.net API are https, and the token request carries the
  client secret.
- No redirects (`safeurl.NewClient` returns the 3xx). Visma Connect and the
  Visma APIs answer directly; a redirect would forward the Bearer token to
  another host.
- The consumer on a refused URL: logs the error and does not start the poller
  (same as other config errors in `startConnection`). The producer returns a
  permanent error, so the envelope goes to the DLQ and is not retried.
  Sample-data answers `ok:false` with the reason.
- The producer reads its config on every delivery and checks it each time. The
  consumer reads it once at start; a changed config takes effect on the next
  start, which checks it.
- Existing tests that point at httptest servers on 127.0.0.1 set
  `allowPrivate: true` on the fixture, as the Sitoo tests do. No assertion
  changes.

## Files

- `src/pkg/vismaurl/vismaurl.go`, `vismaurl_test.go`: shared check.
- `src/cmd/visma-consumer/service.go`, `server.go`: `allowPrivate`, safe client
  in `Configure`, check in start and sample-data.
- `src/cmd/visma-producer/service.go`: same for `Configure` and `Deliver`.
- `src/pkg/managementapi/nodeconfig.go`, `visma_urls.go`: check at start.
- Tests in both cmd packages and managementapi.
- `docs/connectors/visma.md`, `docs/SECURITY.md`, `plans/backlog.md` item 147.

## Risks

- A tenant that set a non-https or private `base_url` or `token_url` stops
  working after the deploy. Documented; the management API now says why when
  the connection is started.
- `oauthcc` itself keeps its default client (Business Central still uses it,
  tracked in #639); the Visma connectors inject theirs through
  `WithHTTPClient`.

## Verify

`go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...`,
`go test -race -count=1` on `pkg/vismaurl`, `cmd/visma-consumer`,
`cmd/visma-producer`, `pkg/managementapi`; `make lint-tenant`. Mutation: the new
tests fail with the old plain-client code and with the check calls removed.
