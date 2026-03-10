# Continuation Prompt for VRSky Phase 3 Implementation

**Session Date**: March 3, 2026  
**Session Duration**: 2 hours (planning + parallel execution)  
**Overall Progress**: 50% complete (Tasks 1-2 done, 3-6 pending)  
**Next Focus**: Application code integration (Tasks 3-4)

---

## Where We Are

### Completed ✅
1. **Kubernetes Infrastructure** (1h)
   - Created 3 production-ready manifest files (263 lines total)
   - Validated all manifests with `kubectl --dry-run=client`
   - Ready to deploy: `infrastructure/kubernetes/management-api/deployment.yaml`, `service.yaml`, data-plane placeholder

2. **Deployment Automation** (45m)
   - Updated `infrastructure/kubernetes/deploy-vrsky-platform.sh`
   - Added `deploy_management_api()` and `deploy_data_plane()` functions
   - Updated execution order and summary output
   - Syntax validated: `bash -n deploy-vrsky-platform.sh` ✅

### Pending (5 hours remaining)
3. **WebSocket Wiring** (2h) - Wire existing components into setupServer()
4. **Metrics Integration** (1.5h) - Verify metrics flow from NATS → cache → clients
5. **Ingress Routes** (30m) - Add `/api/v1/*` and `/ws/*` to API gateway
6. **E2E Test Script** (1h) - Create 5-scenario integration verification

---

## Critical Discovery: Existing Infrastructure

**Important**: The WebSocket infrastructure is **already built** in the codebase:
- ✅ SSE handler in `websocket.go` (216 lines)
- ✅ Client registry in `client_registry.go` (174 lines)  
- ✅ Metrics cache in `metrics_cache.go` (329 lines)
- ✅ NATS subscriber in `nats_subscriber.go` (228 lines)

**This is NOT a greenfield implementation.** Tasks 3-4 are:
- Wiring these components into the server startup (main.go)
- Verifying the metrics flow works end-to-end

---

## What Needs to Happen Next

### Task 3: Wire WebSocket Into Server Setup (2 hours)

**Edit**: `src/cmd/management-api/main.go` in the `setupServer()` function

**Add around line 160** (after creating the Handler):

```go
// Initialize WebSocket components
clientRegistry := managementapi.NewClientRegistry(logger)
metricsCache := managementapi.NewMetricsCache(logger)

// Pass to handler
restHandler.SetWebSocketDeps(clientRegistry, metricsCache)

// Start metrics subscriber background worker
subscriber := managementapi.NewMetricsSubscriber(nc, repo, clientRegistry, metricsCache, logger)
go func() {
    if err := subscriber.Start(ctx); err != nil {
        logger.Error("metrics subscriber failed", "error", err)
    }
}()
```

**Add to Handler struct**:
```go
func (h *Handler) SetWebSocketDeps(reg *ClientRegistry, cache *MetricsCache) {
    h.clientRegistry = reg
    h.metricsCache = cache
}
```

**Test**: `go run ./cmd/management-api/main.go` should start without errors

---

### Task 4: Verify Metrics Subscriber (1.5 hours)

**File**: `src/pkg/managementapi/nats_subscriber.go` (already exists)

**Verify**:
1. It subscribes to: `vrsky.metrics.{tenantID}.>`
2. It calls `metricsCache.UpdateConnectionMetrics()` correctly
3. It registers listener with clientRegistry
4. Reconnection logic works after NATS restart

**Test**:
1. Publish mock metrics to NATS: `nats pub vrsky.metrics.tenant-1.consumer.conn-123 '{...}'`
2. Verify MetricsCache updated
3. Verify WebSocket clients notified
4. All done in `metrics_subscriber_test.go`

---

### Task 5: Update Ingress Routes (30 minutes)

**Edit**: `infrastructure/kubernetes/ingress/ingress.yaml`

**Add routes**:
```yaml
- path: /api/v1/*
  backend: vrsky-management-api:8080
- path: /ws/*
  backend: vrsky-management-api:8080
```

**Validate**: `kubectl apply -f infrastructure/kubernetes/ingress/ingress.yaml --dry-run=client`

---

### Task 6: E2E Test Script (1 hour)

**Create**: `infrastructure/scripts/e2e-test.sh`

**Test scenarios**:
1. Create connection via REST
2. Start connection via REST
3. Send test message via REST
4. WebSocket connection established
5. Metrics broadcast to client

**Example**:
```bash
#!/bin/bash
set -e

API="http://api.vrsky.local:8080"

# Test 1: Create connection
CONN_ID=$(curl -X POST $API/api/v1/connections \
  -H "Content-Type: application/json" \
  -d '{"name":"test"}' | jq -r '.id')

# Test 4: WebSocket connection
websocat ws://api.vrsky.local:8080/ws/$CONN_ID &
WS_PID=$!
sleep 2

# Verify metrics received
kill $WS_PID

echo "✅ All tests passed"
```

---

## File Changes Summary

| File | Change | Status |
|------|--------|--------|
| `infrastructure/kubernetes/management-api/deployment.yaml` | Created | ✅ |
| `infrastructure/kubernetes/management-api/service.yaml` | Created | ✅ |
| `infrastructure/kubernetes/data-plane/deployment.yaml` | Created | ✅ |
| `infrastructure/kubernetes/deploy-vrsky-platform.sh` | Updated | ✅ |
| `src/cmd/management-api/main.go` | Update setupServer() | ⏳ Task 3 |
| `src/pkg/managementapi/handler.go` | Add SetWebSocketDeps() | ⏳ Task 3 |
| `infrastructure/kubernetes/ingress/ingress.yaml` | Add routes | ⏳ Task 5 |
| `infrastructure/scripts/e2e-test.sh` | Create new | ⏳ Task 6 |

---

## Code Entry Points

### Understanding Existing Code

Before starting, understand these files:

1. **`src/pkg/managementapi/websocket.go`**
   - `HandleMetricsWebSocket()` — SSE handler (line 45)
   - `WebSocketClient` — client state (line 12)
   - Understand how SSE streaming works

2. **`src/pkg/managementapi/client_registry.go`**
   - `NewClientRegistry()` — constructor
   - `RegisterClient()` — add client
   - `BroadcastToConnection()` — send to all clients

3. **`src/pkg/managementapi/metrics_cache.go`**
   - `NewMetricsCache()` — constructor
   - `UpdateConnectionMetrics()` — store metrics
   - `AddListener()` — register for updates
   - `notifyListeners()` — dispatch to listeners

4. **`src/pkg/managementapi/nats_subscriber.go`**
   - `NewMetricsSubscriber()` — constructor
   - `Start()` — begin subscription
   - `handleMetricsMessage()` — process metrics

5. **`src/cmd/management-api/main.go`**
   - `setupServer()` function (line 127) — where to add initialization
   - Study dependency injection pattern

---

## Testing Strategy

### Local Testing (before Kubernetes)

```bash
# Terminal 1: Start management API
go run ./cmd/management-api/main.go

# Terminal 2: Publish mock metrics
nats pub vrsky.metrics.tenant-1.connection.conn-123 '{"messagesProcessed":100}'

# Terminal 3: Connect via WebSocket/SSE
curl -N http://localhost:3000/api/v1/connections/conn-123/metrics/stream

# Expected: See metric updates flowing
```

### Kubernetes Testing

```bash
# Deploy everything
cd infrastructure/kubernetes
./deploy-vrsky-platform.sh

# Port-forward to Management API
kubectl port-forward -n vrsky-platform svc/vrsky-management-api 8080:8080

# Run E2E test
bash infrastructure/scripts/e2e-test.sh
```

---

## Acceptance Criteria

### Task 3 ✅
- [ ] Management API starts: `go run ./cmd/management-api/main.go` (no errors)
- [ ] WebSocket components initialized: ClientRegistry, MetricsCache created
- [ ] NATS subscriber started as background worker
- [ ] Handler can access WebSocket dependencies
- [ ] Tests pass: `go test ./cmd/management-api/...`

### Task 4 ✅
- [ ] Metrics flow: NATS → subscriber → cache → clients
- [ ] ClientRegistry receives updates from cache
- [ ] WebSocket clients receive broadcast messages
- [ ] Reconnection logic works (tested with NATS restart)
- [ ] Tests pass: `go test ./pkg/managementapi/...`

### Task 5 ✅
- [ ] Ingress manifests validate
- [ ] Kong routes `/api/v1/*` to Management API
- [ ] Kong routes `/ws/*` to Management API
- [ ] External clients can reach API via Ingress

### Task 6 ✅
- [ ] E2E test script exists and is executable
- [ ] All 5 scenarios pass
- [ ] No manual intervention needed
- [ ] Reports pass/fail status clearly

---

## Troubleshooting Reference

**Problem**: Management API won't start  
**Solution**: Check NATS_URL and DB_URL environment variables in config.go

**Problem**: WebSocket client doesn't connect  
**Solution**: Verify ClientRegistry.RegisterClient() is called in handler

**Problem**: Metrics not flowing to client  
**Solution**: Check MetricsCache.AddListener() in metrics_subscriber.go

**Problem**: Pod won't reach Ready state  
**Solution**: Check logs: `kubectl logs -n vrsky-platform -l app=vrsky-management-api`

---

## Timeline Estimate

- **Task 3** (Wiring): 2 hours (code review → understand → implement → test)
- **Task 4** (Integration): 1.5 hours (verify → test → fix issues)
- **Task 5** (Ingress): 30 minutes (copy pattern from existing routes)
- **Task 6** (Testing): 1 hour (write script → test locally → verify)
- **Buffer** (debugging): 1 hour

**Total**: ~6 hours from this point  
**Target completion**: March 4, 2026 (next business day)

---

## Documents to Reference

- `.sisyphus/plans/fix-remaining-issues.md` — Full 400+ line implementation plan
- `.sisyphus/PHASE_3_CHECKPOINT.md` — This session's discoveries
- `AGENTS.md` — Code standards and project governance
- `docs/PROJECT_INCEPTION.md` — Architecture philosophy
- `README.md` — Quick start and overview

---

## Quick Commands

```bash
# Verify current state
cd /home/ludvik/vrsky
go build ./cmd/management-api
bash -n infrastructure/kubernetes/deploy-vrsky-platform.sh

# View completed work
cat infrastructure/kubernetes/management-api/deployment.yaml
cat infrastructure/kubernetes/deploy-vrsky-platform.sh | grep -A 20 "deploy_management_api"

# Start fresh session
source ./AGENTS.md
export KUBECONFIG=$(pwd)/kubeconfig
```

---

## Next Agent Instructions

1. **Read this entire document first** (5 minutes)
2. **Review the PHASE_3_CHECKPOINT.md** (10 minutes) 
3. **Examine existing WebSocket code** (20 minutes)
4. **Start Task 3**: Wire setupServer() (2 hours)
5. **Start Task 5** in parallel: Update Ingress (30 minutes)
6. **Verify Task 4** (NATS subscriber already exists)
7. **Create Task 6** (E2E test script)

**No blockers. Infrastructure is ready. Ready to code.**

