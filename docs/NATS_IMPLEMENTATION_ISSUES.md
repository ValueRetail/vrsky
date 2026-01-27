# NATS Implementation GitHub Issues

**Created**: January 27, 2026  
**Purpose**: GitHub issues for implementing the hybrid NATS architecture  
**Related**: [NATS_ARCHITECTURE.md](./NATS_ARCHITECTURE.md)

---

## Overview

Based on the approved hybrid NATS architecture, these issues define the implementation tasks. These will either create new issues or update existing ones from the consolidation plan.

---

## Issue 1: Update - Build Core Platform Foundation (NATS + Components)

**Existing Issue**: #1  
**Action**: UPDATE with NATS architecture details  
**Timeline**: Week 1-4 (Jan 27 - Feb 23)  
**Team**: 2-3 engineers  
**Priority**: P0 - Critical

### Updated Description

Build the foundational VRSky platform infrastructure with hybrid NATS architecture (Platform NATS + Tenant NATS).

### Objectives

**Hybrid NATS Architecture**:
- ✅ Deploy Platform NATS Cluster (3-node HA, JetStream + KV)
- ✅ Deploy Tenant-scoped NATS instances (single-node, NATS Core)
- ✅ Implement NATS service discovery (Kubernetes DNS)
- ✅ Workers connect to all tenant NATS instances
- ✅ Round-robin publishing strategy

**State Management**:
- ✅ NATS KV for message state tracking
- ✅ Retry queue implementation
- ✅ Dead letter queue implementation
- ✅ TTL-based cleanup (15min for messages, 7 days for DLQ)

**Reference-Based Messaging**:
- ✅ MinIO/S3 integration for large payloads (>256KB)
- ✅ Presigned URL generation
- ✅ Automatic cleanup after TTL

**Component Interfaces**:
- ✅ Consumer interface (Go)
- ✅ Producer interface (Go)
- ✅ Converter interface (Go)
- ✅ Filter interface (Go)

**First Implementations**:
- ✅ HTTP Consumer (webhook receiver)
- ✅ HTTP Producer (REST API caller)
- ✅ Simple orchestrator (state machine)

### Acceptance Criteria

- [ ] Platform NATS cluster running in Kubernetes (3 nodes, JetStream enabled)
- [ ] Tenant NATS instance can be provisioned via API
- [ ] Workers successfully connect to multiple NATS instances
- [ ] Message state tracked in NATS KV (`message_state` bucket)
- [ ] Large payloads (>256KB) stored in MinIO, reference in NATS
- [ ] End-to-end integration: HTTP webhook → NATS → HTTP API call
- [ ] Failed messages moved to dead letter queue after 3 retries
- [ ] All code has 70%+ test coverage

### Technical Specifications

**Platform NATS Deployment**:
```yaml
Replicas: 3
Resources: 4 CPU, 8GB RAM per node
Storage: 100GB SSD per node (JetStream)
KV Buckets:
  - message_state (TTL: 15min)
  - retry_queue (TTL: 1hr)
  - dead_letter_queue (TTL: 7 days)
```

**Tenant NATS Deployment**:
```yaml
Replicas: 1 (per instance)
Resources: 2 CPU, 4GB RAM
Mode: NATS Core (no JetStream)
Naming: nats-{tenant-id}-{instance-number}
```

**Capacity Limits**:
- Max 50 integrations per tenant NATS instance
- Auto-scale: Provision new instance when limit reached

### Dependencies

- Kubernetes cluster (GKE/EKS/AKS)
- Helm
- MinIO or S3 bucket
- PostgreSQL (tenant metadata)

### Related Files

- `cmd/control-plane/` - Tenant & NATS provisioning API
- `cmd/data-plane/` - Message processing runtime
- `pkg/messaging/` - NATS client library
- `pkg/consumer/`, `pkg/producer/`, `pkg/converter/`, `pkg/filter/` - Component interfaces
- `deployments/helm/nats-platform/` - Platform NATS Helm chart
- `deployments/helm/nats-tenant/` - Tenant NATS Helm chart

---

## Issue 2: NEW - NATS Instance Auto-Scaling & Lifecycle Management

**Action**: CREATE NEW ISSUE  
**Timeline**: Week 5-7 (Feb 24 - Mar 16)  
**Team**: 1-2 engineers  
**Priority**: P1 - High

### Description

Implement automatic scaling and lifecycle management for tenant-scoped NATS instances based on capacity limits.

### Objectives

**Auto-Scaling Logic**:
- ✅ Monitor integration count per NATS instance
- ✅ Monitor message throughput per NATS instance
- ✅ Trigger new instance provisioning at 50 integrations or 100K msgs/sec
- ✅ Automatic integration rebalancing

**Lifecycle Management**:
- ✅ Provision new NATS instance via Control Plane API
- ✅ Register instance in service discovery (Kubernetes DNS)
- ✅ Update PostgreSQL tracking table
- ✅ Graceful shutdown and decommissioning
- ✅ Worker connection updates (connect to new instances)

**Monitoring & Metrics**:
- ✅ Prometheus metrics from NATS monitoring endpoint
- ✅ Integration count per instance
- ✅ Message rate (msgs/sec)
- ✅ Memory and CPU usage
- ✅ Connection count

### Acceptance Criteria

- [ ] Control Plane monitors all tenant NATS instances every 30 seconds
- [ ] New NATS instance automatically provisioned when integration count >= 50
- [ ] New NATS instance automatically provisioned when msg rate > 100K sustained (5min)
- [ ] Workers automatically discover and connect to new instances
- [ ] Grafana dashboard shows per-instance metrics
- [ ] Alerts trigger when instance approaching capacity (80%)
- [ ] Graceful decommissioning removes instance without message loss

### Technical Specifications

**Monitoring Loop**:
```go
func monitorTenantCapacity(tenantID string) {
    instances := getNATSInstances(tenantID)
    for _, instance := range instances {
        metrics := fetchMetrics(instance)
        
        if metrics.IntegrationCount >= 50 {
            provisionNewInstance(tenantID)
        }
        
        if metrics.MsgRateSustained > 100_000 {
            provisionNewInstance(tenantID)
        }
    }
}
```

**Provisioning Flow**:
```
1. Create Kubernetes Deployment (nats-{tenant}-{N})
2. Create Kubernetes Service (DNS registration)
3. Wait for pod ready (health check)
4. Insert into PostgreSQL (nats_instances table)
5. Notify workers (reconnect with updated instance list)
```

### Related Files

- `internal/service/nats_lifecycle.go` - Provisioning logic
- `internal/service/capacity_monitor.go` - Monitoring loop
- `internal/repository/nats_instances.go` - Database tracking
- `deployments/k8s/nats-tenant-template.yaml` - Template for new instances

---

## Issue 3: NEW - NATS KV State Tracking & Retry Logic

**Action**: CREATE NEW ISSUE  
**Timeline**: Week 3-5 (Feb 10 - Mar 2)  
**Team**: 1-2 engineers  
**Priority**: P1 - High

### Description

Implement message state tracking using NATS KV store with retry logic and dead letter queue.

### Objectives

**State Machine**:
- ✅ Message states: RECEIVED → PROCESSING → COMPLETED
- ✅ Retry states: RETRY (with backoff)
- ✅ Failed states: DEAD_LETTER
- ✅ Max 3 retry attempts before DLQ

**NATS KV Integration**:
- ✅ Store message state in `message_state` bucket
- ✅ Store retry queue in `retry_queue` stream
- ✅ Store dead letter in `dead_letter_queue` stream
- ✅ TTL-based cleanup (15min, 1hr, 7 days)

**Retry Logic**:
- ✅ Exponential backoff (2^retry_count seconds)
- ✅ Track retry count in state
- ✅ Move to DLQ after 3 failed attempts
- ✅ Idempotency handling (prevent duplicate processing)

**Worker Integration**:
- ✅ Workers update state before/after processing
- ✅ Workers check retry count before processing
- ✅ Workers publish to retry queue on failure
- ✅ Workers publish to DLQ after max retries

### Acceptance Criteria

- [ ] Message state stored in Platform NATS KV with TTL
- [ ] Failed messages retry with exponential backoff
- [ ] Messages moved to DLQ after 3 failed attempts
- [ ] DLQ messages retained for 7 days
- [ ] Tenant can view DLQ messages via API
- [ ] Tenant can manually retry DLQ messages
- [ ] State machine prevents duplicate processing (idempotency)
- [ ] Auto-cleanup removes expired state entries

### Technical Specifications

**State Schema**:
```go
type MessageState struct {
    MessageID     string    `json:"message_id"`
    TenantID      string    `json:"tenant_id"`
    IntegrationID string    `json:"integration_id"`
    Status        string    `json:"status"` // received, processing, completed, retry, dead_letter
    RetryCount    int       `json:"retry_count"`
    LastError     string    `json:"last_error,omitempty"`
    CreatedAt     time.Time `json:"created_at"`
    UpdatedAt     time.Time `json:"updated_at"`
}
```

**KV Operations**:
```go
// Store state
kv.Put(messageID, json.Marshal(state))

// Get state
entry, _ := kv.Get(messageID)
json.Unmarshal(entry.Value(), &state)

// Watch for changes
watcher, _ := kv.Watch(messageID)
```

### Related Files

- `pkg/messaging/state.go` - State machine logic
- `pkg/messaging/retry.go` - Retry logic
- `pkg/messaging/dlq.go` - Dead letter queue
- `internal/worker/processor.go` - Worker state updates

---

## Issue 4: NEW - Service Discovery for Tenant NATS Instances

**Action**: CREATE NEW ISSUE  
**Timeline**: Week 4-5 (Feb 17 - Feb 28)  
**Team**: 1 engineer  
**Priority**: P1 - High

### Description

Implement service discovery mechanism for workers to connect to all tenant NATS instances.

### Objectives

**Kubernetes DNS Integration**:
- ✅ Each tenant NATS instance has Kubernetes Service
- ✅ Predictable DNS naming: `nats-{tenant-id}-{N}.vrsky-tenants.svc.cluster.local`
- ✅ Service discovery via Control Plane API

**Control Plane API**:
- ✅ `GET /api/tenants/{tenant_id}/nats-instances` - Returns list of NATS URLs
- ✅ PostgreSQL tracking table: `nats_instances`
- ✅ Real-time updates when new instances provisioned

**Worker Discovery**:
- ✅ Workers fetch NATS instances on startup
- ✅ Workers connect to all instances (comma-separated URLs)
- ✅ Workers reconnect when new instances added
- ✅ NATS client handles automatic reconnection

**Health Checks**:
- ✅ Liveness probe on NATS instances
- ✅ Mark instance as unhealthy if unreachable
- ✅ Remove unhealthy instances from discovery

### Acceptance Criteria

- [ ] Control Plane API returns list of NATS URLs for tenant
- [ ] PostgreSQL table tracks all active NATS instances
- [ ] Workers connect to all tenant NATS instances on startup
- [ ] Workers automatically reconnect when new instance added
- [ ] Unhealthy instances removed from discovery within 60 seconds
- [ ] Health check endpoint validates NATS connectivity

### Technical Specifications

**Database Schema**:
```sql
CREATE TABLE nats_instances (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    instance_number INT NOT NULL,
    dns_name VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL, -- active, draining, terminated
    integration_count INT DEFAULT 0,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(tenant_id, instance_number)
);

CREATE INDEX idx_nats_instances_tenant ON nats_instances(tenant_id, status);
```

**API Response**:
```json
{
  "tenant_id": "tenant-a",
  "nats_instances": [
    {
      "url": "nats://nats-tenant-a-1.vrsky-tenants.svc.cluster.local:4222",
      "instance_number": 1,
      "status": "active",
      "integration_count": 35
    },
    {
      "url": "nats://nats-tenant-a-2.vrsky-tenants.svc.cluster.local:4222",
      "instance_number": 2,
      "status": "active",
      "integration_count": 15
    }
  ]
}
```

**Worker Connection**:
```go
// Fetch instances from Control Plane
instances := controlPlane.GetNATSInstances(tenantID)
urls := []string{}
for _, inst := range instances {
    urls = append(urls, inst.URL)
}

// Connect to all instances
nc, _ := nats.Connect(
    strings.Join(urls, ","),
    nats.MaxReconnects(-1),
)
```

### Related Files

- `internal/api/nats_instances.go` - API endpoints
- `internal/repository/nats_instances.go` - Database operations
- `pkg/discovery/nats.go` - Discovery client library
- `cmd/worker/main.go` - Worker startup with discovery

---

## Issue 5: NEW - NATS Monitoring & Observability Dashboards

**Action**: CREATE NEW ISSUE  
**Timeline**: Week 6-7 (Mar 3 - Mar 16)  
**Team**: 1 engineer  
**Priority**: P2 - Medium

### Description

Implement comprehensive monitoring and observability for Platform NATS and Tenant NATS instances.

### Objectives

**Prometheus Metrics**:
- ✅ Platform NATS: JetStream size, KV size, stream lag
- ✅ Tenant NATS: Message rate, connection count, memory usage
- ✅ Per-tenant metrics (integration count, throughput)
- ✅ Dead letter queue size

**Grafana Dashboards**:
- ✅ Platform Overview (all NATS health)
- ✅ Tenant Detail (per-tenant NATS instances)
- ✅ Message Flow (end-to-end latency)
- ✅ Capacity Planning (utilization, growth trends)

**Alerting**:
- ✅ Tenant NATS approaching capacity (80%)
- ✅ Platform NATS KV storage high (>80GB)
- ✅ Dead letter queue growing (>10K messages)
- ✅ NATS instance unhealthy/down

**Logging**:
- ✅ Loki integration for structured logs
- ✅ NATS server logs aggregated
- ✅ Worker processing logs
- ✅ Error tracking and correlation

### Acceptance Criteria

- [ ] Prometheus scrapes Platform NATS metrics every 15 seconds
- [ ] Prometheus scrapes Tenant NATS metrics every 30 seconds
- [ ] Grafana dashboard shows real-time platform health
- [ ] Grafana dashboard shows per-tenant capacity utilization
- [ ] Alerts fire when capacity thresholds exceeded
- [ ] Loki aggregates NATS logs with tenant/instance labels
- [ ] Logs queryable by tenant, integration, or message ID

### Technical Specifications

**Prometheus Config**:
```yaml
scrape_configs:
  - job_name: 'nats-platform'
    static_configs:
      - targets: ['nats-platform-0:8222', 'nats-platform-1:8222', 'nats-platform-2:8222']
    scrape_interval: 15s
    
  - job_name: 'nats-tenants'
    kubernetes_sd_configs:
      - role: pod
        namespaces:
          names: ['vrsky-tenants']
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app]
        action: keep
        regex: nats
    scrape_interval: 30s
```

**Key Metrics**:
- `nats_jetstream_storage_bytes` - JetStream storage used
- `nats_kv_bucket_size_bytes` - KV bucket size
- `nats_messages_in_per_sec` - Message ingress rate
- `nats_messages_out_per_sec` - Message egress rate
- `nats_connections` - Active connections
- `vrsky_integration_count{tenant="X",instance="Y"}` - Custom metric

### Related Files

- `deployments/monitoring/prometheus.yaml` - Prometheus config
- `deployments/monitoring/grafana-dashboards/` - Dashboard JSON
- `deployments/monitoring/alerts.yaml` - Alert rules

---

## Issue 6: UPDATE - Multi-Tenant Isolation & Security

**Existing Issue**: #4  
**Action**: UPDATE with NATS security details  
**Timeline**: Week 2-4 (Feb 3 - Feb 23)  
**Team**: 1-2 engineers  
**Priority**: P0 - Critical

### Additional NATS Security Requirements

**Add to existing scope**:

**Tenant NATS Isolation**:
- ✅ Each tenant has dedicated NATS instance(s)
- ✅ Kubernetes NetworkPolicy prevents cross-tenant access
- ✅ NATS credentials unique per tenant
- ✅ JWT-based authentication for workers

**Platform NATS Security**:
- ✅ Only Control Plane can access Platform NATS
- ✅ Service account authentication
- ✅ TLS encryption (in-cluster)
- ✅ No direct tenant access

**Credential Management**:
- ✅ NATS credentials stored in Kubernetes Secrets
- ✅ Automatic rotation (monthly)
- ✅ Revocation on tenant deletion
- ✅ Audit log for NATS access

### Additional Acceptance Criteria

- [ ] Tenant A cannot connect to Tenant B's NATS instance
- [ ] Workers authenticate via JWT tokens
- [ ] NATS credentials rotated monthly
- [ ] All NATS communication encrypted (TLS)
- [ ] Audit log tracks NATS authentication events

### Related Files

- `internal/service/nats_auth.go` - Authentication logic
- `deployments/k8s/network-policies.yaml` - Network isolation
- `pkg/security/jwt.go` - JWT token generation

---

## Summary of New/Updated Issues

### New Issues (Create These)
1. **NATS Instance Auto-Scaling & Lifecycle Management** (P1)
2. **NATS KV State Tracking & Retry Logic** (P1)
3. **Service Discovery for Tenant NATS Instances** (P1)
4. **NATS Monitoring & Observability Dashboards** (P2)

### Updated Issues (Modify These)
1. **Issue #1** - Add detailed NATS hybrid architecture implementation (P0)
2. **Issue #4** - Add NATS security and isolation requirements (P0)

### Total Implementation Effort

| Issue | Priority | Estimated Weeks | Team Size |
|-------|----------|----------------|-----------|
| #1 (Updated) | P0 | 4 weeks | 2-3 engineers |
| #4 (Updated) | P0 | 3 weeks | 1-2 engineers |
| Auto-Scaling (NEW) | P1 | 3 weeks | 1-2 engineers |
| State Tracking (NEW) | P1 | 3 weeks | 1-2 engineers |
| Service Discovery (NEW) | P1 | 2 weeks | 1 engineer |
| Monitoring (NEW) | P2 | 2 weeks | 1 engineer |

**Total**: ~17 engineering weeks (can parallelize to 4-5 calendar weeks)

---

## Next Steps

1. ✅ Review this issue plan
2. Create 4 new GitHub issues
3. Update issues #1 and #4 with NATS details
4. Update project timeline to reflect NATS implementation
5. Assign engineers to issues
6. Begin implementation (Week 1 starts Jan 27, 2026)

---

**Status**: Ready for Approval  
**Created**: January 27, 2026  
**Last Updated**: January 27, 2026
