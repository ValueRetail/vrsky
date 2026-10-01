# Metrics and tenant-status streams die after 30 s

## Open questions

None.

## Goal

The two remaining live streams in management-api stay alive past the server's
30 s `WriteTimeout`, the same way the worker-events proxy does since #278.

| Stream | Handler | Used by |
|---|---|---|
| `GET /api/v1/connections/{id}/metrics/stream` | `HandleMetricsSSE` (`websocket.go`) | builder live metrics (`ui/src/services/websocket.ts`) |
| `GET /api/v1/tenants/{tenant_id}/status/stream` | `HandleTenantStatusSSE` (`tenant_sse.go`) | workspace provisioning progress |

## What is wrong (checked in the code, 2026-09-30)

- Neither handler clears the write deadline. After 30 s every write fails,
  the errors are discarded (`_, _ = w.Write`), and the connection stays open,
  so the browser sits on a dead stream and never reconnects. Exactly the bug
  fixed for worker events in #278.
- Neither sets `X-Accel-Buffering: no`, so nginx may batch frames.
- Their heartbeat is 30 s, which is fine for the 60 s proxy idle limit.

## Approach

One small helper in `pkg/managementapi`, used by all three streams:

```go
// beginSSE lifts the server's write deadline and sets the stream headers.
func beginSSE(w http.ResponseWriter)
```

- `http.NewResponseController(w).SetWriteDeadline(time.Time{})` (the
  Logging/Metrics wrappers already `Unwrap` since #278).
- Headers: `Content-Type`, `Cache-Control`, `Connection`, `X-Accel-Buffering: no`.
- The two handlers call it instead of setting headers by hand, and **return
  when a write fails** instead of ignoring it, so a broken stream is closed
  and the browser reconnects.
- The worker-events proxy keeps its behaviour; it only switches to the helper.

## Files

| File | Change |
|---|---|
| `src/pkg/managementapi/sse.go` (new) | `beginSSE` |
| `src/pkg/managementapi/websocket.go` | use it; stop on write error |
| `src/pkg/managementapi/tenant_sse.go` | use it; stop on write error |
| `src/pkg/managementapi/worker_events_proxy.go` | use it (no behaviour change) |
| tests next to each | see below |

No UI, schema, env or ingress changes.

## Tests

Real `http.Server` with a short `WriteTimeout` (e.g. 200 ms), as the #278
test does:

- `TestMetricsSSE_OutlivesWriteTimeout` — a frame pushed after the timeout
  arrives at the client.
- `TestTenantStatusSSE_OutlivesWriteTimeout` — same.
- Both through the real Logging/Metrics wrappers, so a wrapper that forgets
  `Unwrap` fails the test.
- Mutation: remove the `SetWriteDeadline` call → both tests fail.
- `X-Accel-Buffering: no` present on both.

Then `gofmt`, `go test -race ./...`, `golangci-lint run`, `lint-openapi`,
`make lint-tenant`.

## Risks

- A stream with no deadline can be held open by a stalled client. Already the
  case for worker events; the heartbeat write fails on a dead peer and the
  handler now returns.
- Wrapper order in `cmd/management-api` differs from the test — mitigated by
  testing through the real wrappers.

## Rollout

`build-push-acr.sh core` → `deploy-core-azure.sh management-api`. Verify:
open a pipeline in the builder, leave it > 1 min, metrics still update.
