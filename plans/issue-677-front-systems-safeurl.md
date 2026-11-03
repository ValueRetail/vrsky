# Front Systems connectors: base_url through safeurl (#677)

Part of #639 (backlog item 147). Same shape as Sitoo (#616), Brightpearl
(#674) and the HTTP producer (#669).

## Problem

`front-systems-consumer` and `front-systems-producer` build request URLs from
the node's `base_url` and send both Front Systems keys
(`Ocp-Apim-Subscription-Key`, `x-api-key`) with a plain `http.Client`. A tenant
can point `base_url` at loopback, the cluster network or the metadata service.

## What `callback_url` is

`callback_url` is our own public webhook URL. The consumer never requests it:
it sends it as data in `POST /api/webhooks {"event", "url"}` so Front Systems
calls it, and compares it with the `url` of listed webhooks. No credential of
ours goes to it and no request leaves our pods for it, so `safeurl` (a guard on
our outbound dials) does not apply. It is not checked.

## Acceptance criteria

- Consumer: `base_url` passes `safeurl.CheckBaseURL` (https; http and private
  addresses only with `ALLOW_PRIVATE_UPSTREAMS=true`) before any Front Systems
  call. A refusal at start is permanent: no retry, row set to `error` with a
  readable `last_error`.
- Consumer: a dial refusal (hostname resolving to a blocked address) is
  permanent too, so it is not retried for 6 hours.
- Producer: `base_url` is checked on each delivery. A refusal is a plain
  error, not `sdk.Permanent`, so the message is retried and ends in the DLQ
  with its payload. A dial refusal stays a normal (retriable) error.
- Both build their client with `safeurl.NewClient` in `Configure`; a client
  injected by tests is kept.
- A webhook-only consumer (no `callback_url`) never calls Front Systems and is
  not refused for its `base_url`, as today.
- Public https hosts keep working; `ALLOW_PRIVATE_UPSTREAMS` keeps working.

## Files

- `src/cmd/front-systems-consumer/service.go`, `retry.go`
- `src/cmd/front-systems-producer/service.go`
- tests: new `base_url_test.go` in both; existing fixtures on
  `http://127.0.0.1` set `allowPrivate: true` (the operator switch).
- `docs/connectors/front-systems.md`

Not touched: `src/pkg/managementapi`, `src/pkg/safeurl`, `docs/SECURITY.md`,
`plans/backlog.md` (text for those is in the PR body).

## Risks

- A live connection with an `http://` or private `base_url` stops: consumer
  start goes to `error`, producer messages go to the DLQ.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`,
`go test -race -count=1 ./cmd/front-systems-consumer/... ./cmd/front-systems-producer/...`,
`make lint-tenant`. Fail without the fix: revert the service changes, keep the
tests, run them.
