# VRSky Platform: Fix Remaining Issues - Work Plan

**Date**: March 3, 2026  
**Status**: Planning Phase Complete  
**Scope**: Complete Phase 3 (Integration & Deployment) and address critical gaps from Phases 1-1E

---

## Executive Summary

**Current State**:
- ✅ Phase 1 (Management API): 100% complete with 13 REST endpoints, NATS integration, test data generation
- ✅ Phase 2 (React SPA): 100% complete with full UI, WebSocket support, connection wizard
- ✅ Phase 1E (Filter): Production-ready on k3s cluster (2/3 replicas running)
- ✅ Infrastructure: NATS HA cluster, PostgreSQL, MinIO on Kubernetes
- ⚠️ **CRITICAL GAP**: No Kubernetes manifests for Management API and Data Plane services
- ⚠️ **CRITICAL GAP**: No WebSocket handler implementation in Management API (`/stream` endpoint)
- ⚠️ **CRITICAL GAP**: No metrics subscriber background worker

**Blockers to Production**:
1. Management API not exposed to Kubernetes cluster or UI
2. WebSocket streaming (`/api/v1/connections/{id}/stream`) not implemented
3. Metrics not flowing from pipeline components to Management API to UI
4. Docker Compose stack incomplete (missing app service manifests)

---

## Remaining Work: 4 Major Tasks

### Task 1: Create Kubernetes Manifests for Application Services ⭐ HIGH PRIORITY

**Objective**: Enable Management API and Data Plane components to run on Kubernetes cluster

**Decision**: 
- Create 2 new manifest directories: `infrastructure/kubernetes/management-api/` and `infrastructure/kubernetes/data-plane/`
- Management API: 1 replica (stateless, can scale later)
- Data Plane: Create placeholder (currently undefined role)
- Both services must connect to Platform NATS, PostgreSQL, MinIO

**Deliverables**:
1. `infrastructure/kubernetes/management-api/deployment.yaml`
   - Container: `ghcr.io/valueretail/vrsky/management-api:latest`
   - Ports: 3000 (API), 9090 (Prometheus metrics)
   - Environment: DB_URL from secret, NATS_URL from service DNS
   - Probes: `/health` and `/ready` endpoints
   - Resources: requests (100m CPU, 128Mi RAM), limits (500m CPU, 256Mi RAM)
   - Service: ClusterIP on port 8080 → container 3000

2. `infrastructure/kubernetes/management-api/service.yaml`
   - Type: ClusterIP
   - Port: 8080
   - Selector: app=vrsky-management-api
   - Enable Prometheus scrape at port 9090

3. `infrastructure/kubernetes/data-plane/deployment.yaml` (Placeholder)
   - TBD: Clarify "Data Plane" role (Orchestrator? Pipeline Runner?)
   - For now: Use Filter component as reference

**Acceptance Criteria**:
- ✅ Manifests validate: `kubectl apply --dry-run=client -f infrastructure/kubernetes/management-api/`
- ✅ Service discoverable via DNS: `vrsky-management-api.vrsky-platform.svc.cluster.local`
- ✅ Pod reaches Ready state after 30 seconds

**Effort**: 30 minutes

---

### Task 2: Update `deploy-vrsky-platform.sh` to Deploy Application Services ⭐ HIGH PRIORITY

**Objective**: Automate full stack deployment (infrastructure + applications)

**Decision**:
- Add new function `deploy_management_api()` after infrastructure components
- Add new function `deploy_data_plane()` (placeholder for now)
- Update `main()` to call these functions
- Add corresponding cleanup/uninstall sections

**Deliverables**:
1. Function `deploy_management_api()`:
   - Apply `infrastructure/kubernetes/management-api/deployment.yaml`
   - Apply `infrastructure/kubernetes/management-api/service.yaml`
   - Wait for pod readiness (same pattern as other components)
   - Print service endpoint (for Ingress configuration)

2. Update `print_summary()`:
   - List Management API service and endpoint
   - Show how to verify: `kubectl port-forward svc/vrsky-management-api 8080:8080`

3. Update main() flow:
   ```
   check_prerequisites()
   deploy_platform_nats()
   deploy_postgresql()
   deploy_minio()
   deploy_filter()
   deploy_management_api()    ← NEW
   deploy_data_plane()        ← NEW (placeholder)
   deploy_monitoring()
   deploy_ingress()
   provision_demo_tenant()
   print_summary()
   ```

**Acceptance Criteria**:
- ✅ Script runs without errors: `bash deploy-vrsky-platform.sh`
- ✅ All infrastructure pods reach Ready state
- ✅ Management API pod reaches Ready state
- ✅ Service is accessible: `kubectl get svc -n vrsky-platform`
- ✅ Can connect: `kubectl port-forward svc/vrsky-management-api 8080:8080 && curl http://localhost:8080/health`

**Effort**: 45 minutes

---

### Task 3: Implement WebSocket Handler in Management API ⭐ CRITICAL

**Objective**: Enable real-time metrics streaming from backend to React UI

**Decision**:
- Create `src/pkg/managementapi/websocket.go` (new file)
- Implement `/api/v1/connections/{id}/stream` WebSocket endpoint
- Broadcast metrics from metrics subscriber worker
- Handle client registration/unregistration, graceful disconnection

**Deliverables**:
1. `src/pkg/managementapi/websocket.go`:
   ```go
   type WebSocketHub struct {
       clients    map[string]map[*Client]bool  // connectionID → set of clients
       broadcast  chan MetricsUpdate
       register   chan *Client
       unregister chan *Client
       mu         sync.RWMutex
   }
   
   type Client struct {
       conn         *websocket.Conn
       hub          *WebSocketHub
       connectionID string
       send         chan MetricsUpdate
   }
   
   func (hub *WebSocketHub) Broadcast(msg MetricsUpdate)
   func (c *Client) readPump()
   func (c *Client) writePump()
   func (hub *WebSocketHub) run()
   ```

2. Update `src/cmd/management-api/main.go`:
   - Initialize WebSocketHub in setupServer()
   - Add route: `mux.HandleFunc("/ws/{connID}", wsHandler)`
   - Upgrade HTTP connection to WebSocket
   - Register client with hub

3. Background metrics subscriber (see Task 4):
   - Listen to NATS `vrsky.connections.*.metrics` topic
   - Unmarshal MetricsUpdate
   - Send to WebSocketHub.broadcast channel

**Message Format** (from UI):
```json
{
  "type": "metrics_update",
  "connectionId": "conn-123",
  "timestamp": "2026-03-03T12:00:00Z",
  "data": {
    "messagesProcessed": 1500,
    "messagesAccepted": 1450,
    "messagesRejected": 50,
    "averageLatency": 2.3,
    "currentThroughput": 100
  }
}
```

**Acceptance Criteria**:
- ✅ WebSocket endpoint accessible at `/ws/{connectionId}`
- ✅ Client connects and receives heartbeat pings
- ✅ Metrics updates broadcast to all connected clients
- ✅ Client disconnection handled gracefully
- ✅ No goroutine leaks (verify with `pprof`)
- ✅ TypeScript client can connect: `ws://localhost:3000/ws/conn-123`

**Effort**: 2 hours

---

### Task 4: Implement Metrics Subscriber Background Worker ⭐ CRITICAL

**Objective**: Sink pipeline metrics from NATS into database and WebSocket

**Decision**:
- Create `src/pkg/managementapi/metrics_subscriber.go` (new file)
- Subscribe to NATS: `vrsky.connections.{tenantID}.*.metrics`
- Update database with latest metrics
- Broadcast to WebSocket clients
- Handle subscription failures with exponential backoff

**Deliverables**:
1. `src/pkg/managementapi/metrics_subscriber.go`:
   ```go
   type MetricsSubscriber struct {
       nc        *nats.Conn
       repo      Repository
       wsHub     *WebSocketHub
       logger    *log.Logger
       subPrefix string
   }
   
   type MetricsUpdate struct {
       ConnectionID       string
       TenantID           string
       MessagesProcessed  int64
       MessagesAccepted   int64
       MessagesRejected   int64
       AverageLatency     float64
       LastUpdated        time.Time
   }
   
   func (ms *MetricsSubscriber) Start(ctx context.Context) error
   func (ms *MetricsSubscriber) handleMetricsMessage(msg *nats.Msg)
   ```

2. Update `src/cmd/management-api/main.go`:
   - Create MetricsSubscriber in setupServer()
   - Start in background goroutine
   - Pass reference to handler so `/stream` endpoint can access wsHub

3. Update `src/pkg/managementapi/repository.go`:
   - Add method: `UpdateMetrics(ctx context.Context, connID string, metrics MetricsUpdate) error`
   - Update `connections.metrics` JSONB field with latest values

**Connection Flow**:
```
Pipeline component → NATS: vrsky.connections.tenant-a.conn-123.metrics
                              ↓
                    MetricsSubscriber.handleMetricsMessage()
                              ↓
                    Repository.UpdateMetrics() → PostgreSQL
                              ↓
                    WebSocketHub.Broadcast() → All connected clients
                              ↓
                    React UI: useWebSocket() hook receives update
```

**Acceptance Criteria**:
- ✅ Subscriber connects to NATS on startup
- ✅ Metrics messages parsed and validated
- ✅ Database updated with new metrics
- ✅ WebSocket clients receive broadcast
- ✅ Subscriber reconnects after NATS restart
- ✅ Integration test passes: publish mock metrics → verify DB update + WS broadcast

**Effort**: 1.5 hours

---

### Task 5: Update Ingress for Management API Endpoint

**Objective**: Expose Management API through Kong gateway to external clients

**Decision**:
- Update `infrastructure/kubernetes/ingress/ingress.yaml` to route `/api/v1/*` → Management API service
- Keep existing routes for React UI and other components

**Deliverables**:
1. Add Kong service route (in `ingress.yaml` or separate service definition):
   ```yaml
   - name: vrsky-api
     host: api.vrsky.example.com
     port: 8080  # Management API ClusterIP port
   ```

2. Add route rule:
   ```yaml
   - path: /api/v1/*
     backend: vrsky-management-api:8080
   ```

3. Add WebSocket route (if using Kong):
   ```yaml
   - path: /ws/*
     backend: vrsky-management-api:8080
   ```

**Acceptance Criteria**:
- ✅ Ingress validates: `kubectl apply --dry-run=client -f infrastructure/kubernetes/ingress/ingress.yaml`
- ✅ Kong service created: `kubectl get service -n vrsky-platform`
- ✅ Route reachable from outside cluster

**Effort**: 30 minutes

---

### Task 6: E2E Integration Test

**Objective**: Verify full stack: UI → API → NATS → Pipeline → Metrics → WS

**Decision**:
- Write bash script: `infrastructure/scripts/e2e-test.sh`
- Perform 5 test scenarios end-to-end
- Verify no errors at any layer

**Test Scenarios**:
1. Create connection via API endpoint (REST POST)
   - Expected: 200, connection ID returned, entry in database
2. Start connection via API endpoint (REST POST /start)
   - Expected: 200, NATS publish to `vrsky.connections.*.start` topic
3. Send test message via API endpoint (REST POST /test-message)
   - Expected: 200, message flows through pipeline, metrics updated
4. WebSocket connection established to `/ws/{connId}`
   - Expected: 101 Switching Protocols, heartbeat received
5. Metrics broadcast to WS client after test message
   - Expected: JSON message received with updated message counts

**Deliverables**:
1. `infrastructure/scripts/e2e-test.sh`:
   - Check all services healthy: `kubectl get pods -n vrsky-platform`
   - Test REST endpoints with `curl`
   - Test WebSocket with `websocat` or similar
   - Parse responses and verify expected values
   - Report pass/fail for each scenario

**Acceptance Criteria**:
- ✅ Script runs without manual intervention
- ✅ All 5 test scenarios pass
- ✅ No errors in logs: `kubectl logs -n vrsky-platform -l app=vrsky-management-api`
- ✅ Database has new connection and metrics: `kubectl exec -n vrsky-database postgresql-0 -- psql -U vrsky -d vrsky -c "SELECT COUNT(*) FROM connections;"`

**Effort**: 1 hour

---

## Implementation Sequence

**Week of March 3-7, 2026**:

| Day | Task | Effort | Blocker? |
|-----|------|--------|----------|
| **Mon** | Task 1: Create K8s manifests | 30m | No |
| **Mon** | Task 2: Update deploy script | 45m | Needs Task 1 |
| **Tue** | Task 3: WebSocket handler | 2h | No |
| **Tue** | Task 4: Metrics subscriber | 1.5h | Needs Task 3 |
| **Wed** | Task 5: Update Ingress | 30m | Needs Tasks 1-2 |
| **Wed** | Task 6: E2E integration test | 1h | Needs Tasks 1-5 |
| **Thu** | Bug fixes & documentation | 1h | As needed |

**Total Effort**: ~7 hours  
**Critical Path**: Tasks 1 → 2 → 5 (infrastructure) in parallel with Tasks 3 → 4 (application code)

---

## Success Criteria (Definition of Done)

✅ **Infrastructure**:
- [ ] Management API Kubernetes manifests created and validated
- [ ] deploy-vrsky-platform.sh automatically deploys all services
- [ ] All pods reach Ready state within 60 seconds

✅ **Functionality**:
- [ ] REST API accessible at `/api/v1/connections` via Ingress
- [ ] WebSocket endpoint `/ws/{connId}` establishes connections
- [ ] Metrics subscriber updates database in real-time
- [ ] WebSocket clients receive metric updates without delay (<500ms)

✅ **Testing**:
- [ ] E2E test script passes all 5 scenarios
- [ ] No errors in logs from any component
- [ ] Database verification confirms data persistence
- [ ] `kubectl describe pod` shows no restart loops

✅ **Documentation**:
- [ ] README updated with new K8s deployment steps
- [ ] API documentation includes `/stream` endpoint
- [ ] Troubleshooting guide covers WebSocket connection issues

---

## Risks & Mitigations

| Risk | Likelihood | Mitigation |
|------|------------|-----------|
| WebSocket memory leaks | Medium | Use `pprof` to monitor goroutines, add cleanup in disconnect handler |
| NATS topic mismatch between components | High | Verify topic names in Management API match what pipeline publishes |
| Ingress routing conflicts with existing paths | Low | Use path prefixes carefully, test with `curl` before deployment |
| Single-node k3s cluster memory exhaustion | Medium | Monitor with `kubectl top nodes`, may need to reduce Filter replicas to 1 |
| Database connection pool exhaustion | Low | Set reasonable limits in config, monitor with Prometheus |

---

## Out of Scope (Phase 4+)

- JWT authentication (planned for Phase 4)
- Role-based access control (Phase 4+)
- Prometheus metrics endpoint for Management API (Phase 4+)
- Advanced monitoring dashboards (Phase 4+)
- Webhook notifications (Phase 5+)
- Long-term metrics storage (Phase 5+)

---

## Implementation Notes

1. **No Breaking Changes**: All changes are additive. Existing endpoints unaffected.
2. **Backward Compatible**: Old clients continue to work without WebSocket.
3. **Graceful Degradation**: If NATS down, WebSocket clients still connect but don't receive updates.
4. **Testing**: Write integration tests BEFORE implementation to catch design issues early.
5. **Documentation**: Update API docs as code is written (don't leave for end).

---

**Next Step**: Begin Task 1 (Kubernetes Manifests) immediately. No blockers.

