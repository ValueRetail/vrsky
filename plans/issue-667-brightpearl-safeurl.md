# Brightpearl connectors: check the base_url override with safeurl (#667)

Open questions: none. Calls made without asking are under "Decisions".

Part of #639 (`plans/backlog.md` item 147). Prior art: Sitoo (#616) and the
API consumer (#639, `plans/issue-639-api-consumer-safeurl.md`).

## Problem

The Brightpearl consumer and producer build their request URL from node
config: a `base_url` override, or `https://ws-{datacenter}.brightpearl.com/public-api/{account_code}`.
Every request carries the tenant's `brightpearl-staff-token`. Both use a plain
`http.Client`, so a tenant can aim them at the cluster network or the cloud
metadata service, and the consumer hands the answer back through the pipeline
and the sample-data endpoint. The derived URL is not safe either: `datacenter`
goes into the host unescaped, so `x@10.0.0.1/` turns the host into
`10.0.0.1`.

## Acceptance criteria

1. Both connectors build their HTTP client with `safeurl.NewClient` in
   `Configure`, so loopback, private, link-local (metadata included), CGNAT,
   multicast and the Azure platform address are refused at dial time, also
   when a hostname resolves to one. No redirects are followed.
2. A `base_url` override is checked with `safeurl.CheckBaseURL` (https, a
   host, no userinfo, query or fragment, no literal blocked IP):
   - consumer: when the connection starts (no poller, error logged) and in
     the sample-data endpoint (`ok:false` with the reason);
   - producer: on every delivery, as a permanent error (DLQ, not retried).
3. Without an override, `datacenter` must be one host label and
   `account_code` one path segment, checked at the same points.
4. The default (derived) URL keeps working.
5. `ALLOW_PRIVATE_UPSTREAMS=true` (operator only) lifts the address check and
   allows `http`, as for Sitoo.
6. A producer request refused at dial is permanent, not retried.
7. Tests fail without the change; docs updated.

## Decisions

- The check lives in the connectors (consumer start path, sample-data,
  producer `Deliver`), not in the management API's node-config validator,
  where the Sitoo and API-consumer checks also sit. Another worker is
  changing `src/pkg/managementapi` (#662) right now. The connector check is
  the one that matters (with the dial); the management API check is only an
  earlier error. Follow-up noted in backlog item 147.
- Validate `datacenter` and `account_code` too, not only the override. The
  dial check already stops a private address, but without this a datacenter
  value can move the staff token to any public host while looking like the
  default. Datacenter: `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`, case
  insensitive (Brightpearl uses `eu1`, `use1`, `usw1`). Account code:
  `^[A-Za-z0-9_-]{1,64}$`.
- Webhook mode is untouched: it publishes the body it receives and calls
  nothing.
- A delivery refused at dial (`safeurl.ErrBlockedAddress`) is permanent.
  The address a name resolves to will not change by retrying, and a retried
  refusal would loop forever instead of reaching the DLQ.
- The sample-data endpoint answers "target address not allowed" for a dial
  refusal and logs the address, as Sitoo does.
- Small duplicated `checkBaseURL` in each binary, as Sitoo does; no new
  shared package.

## Files

- `src/cmd/brightpearl-consumer/service.go`, `server.go`
- `src/cmd/brightpearl-producer/service.go`
- tests: new `base_url_test.go` and `configure_client_test.go` in both;
  existing fixtures that serve on `http://127.0.0.1` set `allowPrivate`.
- `docs/connectors/brightpearl.md`, `docs/SECURITY.md`,
  `plans/backlog.md` (item 147).

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`,
`go test -race -count=1 ./cmd/brightpearl-consumer/... ./cmd/brightpearl-producer/...`,
`make lint-tenant`. Fail without the fix: revert the service changes, keep
the tests, run them.
