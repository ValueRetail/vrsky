# Issue #686: Business Central connectors through safeurl

Part of #639 (backlog item 147). Same fix as Sitoo (#616), Visma (#668),
Brightpearl (#674) and SAP S/4HANA (#680).

## Open questions

None blocking. Decisions taken without asking are listed below and in the PR.

## Goal

The Business Central consumer and producer send the tenant's client secret to
`token_url` and the bearer token to `api_base_url` (or the defaults built from
`aad_tenant_id` and `environment`). Today both go through a plain
`http.Client`, unchecked. After this change:

- `api_base_url` (or the default API root) and `token_url` (or the default
  Entra token URL) pass `safeurl.CheckBaseURL`: https, a host, no userinfo,
  query or fragment, not a literal private, loopback, link-local or metadata
  address.
- Every request (token, API, `@odata.nextLink` pages, pictures, the
  producer's dedupe lookup) goes through `safeurl.NewClient`, so a hostname
  that resolves to a blocked address is refused at dial time. The token
  client gets it through `oauthcc.WithHTTPClient`.
- `@odata.nextLink` never takes the credential off the configured origin and
  never from https to http.
- `ALLOW_PRIVATE_UPSTREAMS=true` (operator only) lifts the address and https
  rules for a mock upstream.

## Acceptance criteria

1. Consumer start (`startConnection`, also used by restore on boot) refuses a
   bad `api_base_url` or `token_url`: no poller starts, the reason is logged,
   no request is sent.
2. The consumer's `/sample-data/` answers `ok:false` with the reason for a bad
   URL and sends nothing. A dial refusal answers "target address not allowed"
   and logs the address; the body never contains `127.0.0.1`, `::1` or
   `dial tcp`.
3. Producer `Deliver` refuses a bad URL with a retryable error (not
   `sdk.Permanent`, which acks and drops) and sends nothing. A dial refusal
   comes back as a retryable error wrapping `safeurl.ErrBlockedAddress`
   without the address, because that text becomes the DLQ's
   `X-Vrsky-Last-Error`.
4. A pagination link: same scheme, host and port as the configured API root
   is followed (userinfo and fragment dropped); another origin whose path is
   under the API root's path is fetched from the configured origin (same rule
   the picture links already follow, and what SAP settled on after review);
   anything else stops the poll with an error that does not name the link.
   Relative links are resolved against the page they came from.
5. The defaults (`https://api.businesscentral.dynamics.com/...`,
   `https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token`) still work.
6. `Configure` builds the safe client; with `ALLOW_PRIVATE_UPSTREAMS=true` it
   reaches loopback.
7. Tests for both connectors fail without the change.
8. `docs/connectors/business-central.md` explains which addresses it calls.

## Affected files

- `src/cmd/business-central-consumer/service.go` (Configure, startConnection,
  the nextLink step in fetchAndPublish, companyURL via a new apiRoot)
- `src/cmd/business-central-consumer/server.go` (sample-data)
- `src/cmd/business-central-consumer/urls.go` (new: checkURLs, followNext)
- `src/cmd/business-central-producer/service.go` (Configure, Deliver,
  entityURL via apiRoot, checkURLs)
- tests in both packages; existing fixtures that serve on `http://127.0.0.1`
  get the escape hatch
- `docs/connectors/business-central.md`

Not touched: `src/pkg/managementapi`, `src/pkg/safeurl`, `src/pkg/oauthcc`,
`docs/SECURITY.md`, `plans/backlog.md`, the cursor and checkpoint machinery.

## Approach

Follow the reviewed Visma/SAP pattern: an `allowPrivate` field set from
`safeurl.AllowPrivateFromEnv()` in `Configure`, `safeurl.NewClient(60s,
allowPrivate)` when no client is injected, a small `checkURLs` per binary.
Checks run in the connectors only (management API is being changed by other
PRs). Producer refusal is `sdk.Retriable`.

## Risks

- A live BC node with an `http` or private `api_base_url`/`token_url` stops
  working after the deploy (consumer: refuses to start on next start;
  producer: retries then dead-letters).
- Redirects are no longer followed. BC's API answers directly; a 3xx now
  shows up as an error instead of being followed.
- An on-prem BC whose nextLink names another host with a path outside the
  configured root stops paging with an error.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`,
`go test -race -count=1 ./cmd/business-central-consumer/... ./cmd/business-central-producer/... ./pkg/oauthcc/...`;
`make lint-tenant` from the repo root. Fail-without-fix: revert the
production code with the tests in place and record which tests fail.
