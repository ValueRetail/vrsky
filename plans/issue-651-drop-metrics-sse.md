# Remove GET /api/v1/connections/{id}/metrics/stream (#651)

## Open questions

None. Ludvik decided to drop the endpoint rather than wire the UI to it.

## Goal

Delete the metrics stream route nothing reads. The UI polls
`GET /api/v1/connections/{id}/metrics` (`ui/src/hooks/useMetrics.ts`); that
stays.

## What I checked

- `git grep` for `metrics/stream`, `HandleMetricsSSE`, `MetricsSSE` across the
  repo. No caller in `ui/`, `docs/`, `.github/` or the Makefiles. The nightly
  end-to-end (`e2e-nightly.yml`) does not run either script that probes it.
- `HandleMetricsSSE` is only an alias for `HandleMetricsWebSocket`, which also
  serves `GET .../metrics/ws`. That route is a separate endpoint and out of
  scope here, so the handler, `ClientRegistry` and `MetricsCache` stay.

## Changes

| File | Change |
|---|---|
| `src/pkg/managementapi/handler.go` | drop the `metrics/stream` route |
| `src/pkg/managementapi/websocket.go` | drop the `HandleMetricsSSE` alias |
| `src/pkg/managementapi/openapi_registry.go` | drop the registry entry |
| `src/pkg/managementapi/sse_test.go` | the write-timeout test now drives `HandleMetricsWebSocket`; new test that the old route 404s |
| `infrastructure/scripts/e2e-test.sh` | drop tests 4 and 5 (they only probe this route) and the websocat prerequisite |
| `test/e2e_management_api.sh` | drop `test_metrics_stream` |
| comments in `audit_middleware.go`, `cmd/management-api/metrics.go` | stop naming the route |
| `plans/sse-write-deadline.md` | note that the route is gone |
| `plans/backlog.md` | strike item 78 (#651) |

## Risks

Public API removal, so the PR gets `agent:needs-human`. Nothing in the repo
calls it; an external client would get a 404.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./pkg/managementapi/...`, `make lint-tenant`,
`make lint-openapi`; `bash -n` on both scripts. The new test fails if the
route is put back.
