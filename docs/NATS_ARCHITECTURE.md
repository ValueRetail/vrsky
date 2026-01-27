# VRSky NATS Architecture Decision

**Date**: January 27, 2026  
**Status**: Approved  
**Decision Type**: Architecture Design Record (ADR)

---

## Context

VRSky requires a messaging infrastructure that supports:
- High throughput (millions of messages per day)
- Multi-tenant isolation
- Ephemeral message transport (no long-term persistence in core platform)
- Reference-based messaging for large payloads
- Elastic scaling based on tenant demand

This document describes the **hybrid NATS architecture** chosen to meet these requirements.

---

## Decision

We will implement a **hybrid dual-NATS architecture**:

1. **Platform NATS Cluster** (Shared, HA)
   - 3-5 node HA cluster with JetStream enabled
   - Used for state tracking, dead letter queue, retries
   - Shared across all tenants (platform-wide infrastructure)

2. **Tenant-Scoped NATS Instances** (Ephemeral, NATS Core)
   - Single-node NATS Core per tenant (no JetStream)
   - Used for fast, ephemeral message transport
   - Horizontal scaling: Add new instances when capacity limits reached

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│              Platform NATS Cluster (HA)                     │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐                     │
│  │ Node 1  │  │ Node 2  │  │ Node 3  │                     │
│  └─────────┘  └─────────┘  └─────────┘                     │
│                                                              │
│  JetStream + KV Buckets:                                    │
│  ├─ message_state (TTL: 15min)                              │
│  ├─ integration_locks (TTL: 5min)                           │
│  ├─ retry_queue (TTL: 1hr)                                  │
│  └─ dead_letter_queue (TTL: 7 days)                         │
└─────────────────────────────────────────────────────────────┘
                          ▲
                          │ (State tracking only)
                          │
        ┌─────────────────┼─────────────────┐
        │                 │                 │
        ▼                 ▼                 ▼
  ┌──────────┐      ┌──────────┐      ┌──────────┐
  │ Tenant A │      │ Tenant B │      │ Tenant C │
  ├──────────┤      ├──────────┤      ├──────────┤
  │ NATS-1   │      │ NATS-1   │      │ NATS-1   │
  │ (Core)   │      │ (Core)   │      │ (Core)   │
  │          │      │          │      │ NATS-2   │
  │ 50 integ │      │ 30 integ │      │ (Core)   │
  └──────────┘      └──────────┘      │          │
                                       │ 75 integ │
                                       └──────────┘
        ▲                 ▲                 ▲
        │                 │                 │
        │                 │                 │
   Integrations      Integrations      Integrations
   & Workers         & Workers         & Workers
```

---

## Component Specifications

### Platform NATS Cluster

**Purpose**: Durable state tracking and platform-wide operations

| Property | Value |
|----------|-------|
| **Deployment** | 3-5 node HA cluster (Kubernetes StatefulSet) |
| **NATS Mode** | JetStream enabled + NATS KV |
| **Replication** | R3 (3-way replication for durability) |
| **Resources** | 4 CPU, 8GB RAM per node |
| **Storage** | 100GB SSD per node (JetStream streams) |
| **Uptime SLA** | 99.9% (HA with automatic failover) |

**Use Cases**:
- ✅ Message state tracking (processing status)
- ✅ Dead letter queue (failed messages)
- ✅ Retry queue (messages awaiting retry)
- ✅ Integration locks (prevent duplicate processing)
- ✅ Platform-wide events (tenant provisioning, etc.)
- ✅ Cross-tenant integrations (future feature)

**KV Bucket Schema**:
```yaml
message_state:
  ttl: 15 minutes
  max_bytes: 10GB
  replicas: 3
  
integration_locks:
  ttl: 5 minutes
  max_bytes: 1GB
  replicas: 3
  
retry_queue:
  ttl: 1 hour
  max_bytes: 50GB
  replicas: 3
  
dead_letter_queue:
  ttl: 7 days
  max_bytes: 100GB
  replicas: 3
```

---

### Tenant-Scoped NATS Instances

**Purpose**: Fast, ephemeral message transport within tenant boundary

| Property | Value |
|----------|-------|
| **Deployment** | Single-node NATS Core (Kubernetes Pod) |
| **NATS Mode** | Core only (no JetStream, no KV) |
| **Resources** | 2 CPU, 4GB RAM per instance |
| **Storage** | None (ephemeral, in-memory only) |
| **Uptime SLA** | 99% (Kubernetes auto-restart, 30s-2min gap) |

**Capacity Limits per Instance**:
- **Max Integrations**: 50-100 (scale at 50)
- **Max Throughput**: 100K-500K msgs/sec
- **Max Concurrent Connections**: 500-1000

**Scaling Triggers**:
```
if integrations >= 50:
    provision_new_nats_instance(tenant_id)
    assign_new_integrations_to_new_instance()

if msg_rate_sustained > 100K msgs/sec for 5 min:
    provision_new_nats_instance(tenant_id)
    rebalance_integrations()
```

**Naming Convention**:
```
nats-{tenant-id}-{instance-number}

Examples:
- nats-tenant-a-1
- nats-tenant-a-2
- nats-tenant-b-1
```

---

## Service Discovery & Worker Connectivity

### Internal DNS Registration

When a new NATS instance is provisioned:

1. **Kubernetes Service** created:
   ```yaml
   apiVersion: v1
   kind: Service
   metadata:
     name: nats-tenant-a-1
     namespace: vrsky-tenants
   spec:
     selector:
       app: nats
       tenant: tenant-a
       instance: "1"
     ports:
     - port: 4222
       name: client
   ```

2. **Internal DNS entry** auto-registered:
   ```
   nats-tenant-a-1.vrsky-tenants.svc.cluster.local:4222
   ```

3. **Control Plane tracking**:
   ```sql
   INSERT INTO nats_instances (tenant_id, instance_id, dns_name, status)
   VALUES ('tenant-a', 1, 'nats-tenant-a-1.vrsky-tenants.svc.cluster.local:4222', 'active');
   ```

### Worker Connection Strategy

**Workers connect to ALL tenant NATS instances** for resilience:

```go
// Worker startup: Fetch all NATS instances for this tenant
tenantNATS := controlPlane.GetNATSInstances(tenantID)
// Returns: ["nats-tenant-a-1:4222", "nats-tenant-a-2:4222"]

// Connect to all instances (NATS client handles failover)
nc, err := nats.Connect(
    strings.Join(tenantNATS, ","),
    nats.Name("worker-converter-1"),
    nats.ReconnectWait(2*time.Second),
    nats.MaxReconnects(-1), // Infinite reconnects
)

// Workers subscribe to all relevant subjects
nc.Subscribe("tenant-a.webhook.*", processWebhook)
nc.Subscribe("tenant-a.convert.*", processConversion)
```

**Publishing Strategy**: Round-robin (NATS client handles automatically)

---

## Message Flow Architecture

### Standard Message Flow (Small Payloads <256KB)

```
1. Integration receives data
   └─> Publishes to Tenant NATS: "tenant-a.webhook.received"

2. Worker subscribes to Tenant NATS
   └─> Receives message on "tenant-a.webhook.received"

3. Worker tracks state in Platform NATS KV
   └─> KV.Put("msg-12345", {status: "processing", timestamp: ...})

4. Worker processes message
   └─> Convert, filter, route, etc.

5. Worker publishes result to Tenant NATS
   └─> "tenant-a.producer.send"

6. Producer consumes from Tenant NATS
   └─> Sends to external system

7. Worker updates state in Platform NATS KV
   └─> KV.Put("msg-12345", {status: "completed", timestamp: ...})
   └─> Auto-deleted after TTL (15min)
```

### Large Payload Flow (>256KB)

```
1. Integration receives large payload (e.g., 5MB file)

2. Integration stores in MinIO/S3
   └─> PUT /tenant-a/messages/msg-12345.bin
   └─> Returns: presigned URL (valid 15min)

3. Integration publishes reference to Tenant NATS
   └─> Payload: {
         message_id: "msg-12345",
         payload_ref: "s3://bucket/tenant-a/messages/msg-12345.bin",
         size: 5242880,
         ttl: 900 // 15min
       }

4. Worker fetches payload from storage
   └─> GET presigned URL
   └─> Process large file

5. Worker publishes result (small reference or direct)
   └─> Tenant NATS or storage depending on size

6. Cleanup job deletes from storage after TTL
   └─> DELETE /tenant-a/messages/msg-12345.bin
```

---

## Retry & Error Handling

### State Machine

```
RECEIVED → PROCESSING → COMPLETED
    ↓           ↓
    └─→ RETRY ←┘ (max 3 attempts)
         ↓
    DEAD_LETTER (after max retries)
```

### Retry Logic

```go
func processMessage(msg *nats.Msg) {
    msgID := msg.Header.Get("Message-ID")
    
    // Get current state from Platform NATS KV
    state := platformKV.Get(msgID)
    
    if state.RetryCount >= 3 {
        // Move to dead letter queue
        deadLetterQueue.Publish(msg)
        platformKV.Put(msgID, {status: "dead_letter", retry_count: 3})
        return
    }
    
    // Track processing
    platformKV.Put(msgID, {status: "processing", retry_count: state.RetryCount})
    
    err := doWork(msg)
    if err != nil {
        // Schedule retry with exponential backoff
        retryDelay := time.Duration(math.Pow(2, float64(state.RetryCount))) * time.Second
        retryQueue.Publish(msg, nats.RetryDelay(retryDelay))
        platformKV.Put(msgID, {status: "retry", retry_count: state.RetryCount + 1})
        return
    }
    
    // Success
    platformKV.Put(msgID, {status: "completed", retry_count: state.RetryCount})
}
```

### Dead Letter Queue

**After 3 failed retries**:
1. Message moved to `dead_letter_queue` stream in Platform NATS
2. Tenant notified via webhook/email
3. Message retained for 7 days
4. Admin UI allows manual retry or inspection

---

## Deployment & Operations

### Kubernetes Manifests

**Platform NATS** (StatefulSet):
```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: nats-platform
  namespace: vrsky-platform
spec:
  serviceName: nats-platform
  replicas: 3
  selector:
    matchLabels:
      app: nats-platform
  template:
    metadata:
      labels:
        app: nats-platform
    spec:
      containers:
      - name: nats
        image: nats:2.10-alpine
        args:
        - "-js"
        - "-c"
        - "/etc/nats/nats.conf"
        resources:
          requests:
            cpu: 2
            memory: 4Gi
          limits:
            cpu: 4
            memory: 8Gi
        volumeMounts:
        - name: config
          mountPath: /etc/nats
        - name: jetstream
          mountPath: /data/jetstream
  volumeClaimTemplates:
  - metadata:
      name: jetstream
    spec:
      accessModes: ["ReadWriteOnce"]
      resources:
        requests:
          storage: 100Gi
```

**Tenant NATS** (Deployment, dynamically created):
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nats-tenant-a-1
  namespace: vrsky-tenants
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nats
      tenant: tenant-a
      instance: "1"
  template:
    metadata:
      labels:
        app: nats
        tenant: tenant-a
        instance: "1"
    spec:
      containers:
      - name: nats
        image: nats:2.10-alpine
        args:
        - "-m"
        - "8222"  # Monitoring port
        resources:
          requests:
            cpu: 1
            memory: 2Gi
          limits:
            cpu: 2
            memory: 4Gi
```

### Auto-Scaling Logic

```go
// Control Plane monitors tenant NATS instances
func monitorTenantCapacity(tenantID string) {
    instances := getNATSInstances(tenantID)
    
    for _, instance := range instances {
        metrics := getMetrics(instance)
        
        // Check integration count
        if metrics.IntegrationCount >= 50 {
            provisionNewNATSInstance(tenantID)
            log.Info("Scaling tenant NATS (integration limit)", 
                "tenant", tenantID, 
                "count", metrics.IntegrationCount)
        }
        
        // Check throughput
        if metrics.MsgRateSustained > 100_000 {
            provisionNewNATSInstance(tenantID)
            log.Info("Scaling tenant NATS (throughput limit)", 
                "tenant", tenantID, 
                "rate", metrics.MsgRateSustained)
        }
    }
}
```

---

## Monitoring & Observability

### Key Metrics

**Platform NATS**:
- JetStream stream size (GB)
- KV bucket size (GB)
- Message rate (msgs/sec)
- Consumer lag (messages)
- Node health (up/down)

**Tenant NATS**:
- Integration count per instance
- Message rate (msgs/sec)
- Connection count
- Memory usage (MB)
- CPU usage (%)

**Alerting Thresholds**:
```yaml
alerts:
  - name: TenantNATSHighLoad
    condition: msg_rate > 80K msgs/sec for 5min
    action: Provision new instance
    
  - name: TenantNATSMemoryHigh
    condition: memory > 3GB
    action: Alert + investigate
    
  - name: PlatformNATSKVFull
    condition: kv_size > 80GB
    action: Alert + increase retention
    
  - name: DeadLetterQueueGrowing
    condition: dlq_size > 10K messages
    action: Alert tenant + investigate
```

---

## Security Considerations

### Authentication

**Platform NATS**:
- Service account credentials (control plane only)
- TLS encryption (in-cluster)
- No direct tenant access

**Tenant NATS**:
- Generated credentials per tenant
- Workers authenticate via JWT tokens
- Credentials rotated monthly

### Isolation

**Network Policies**:
```yaml
# Tenant NATS can only be accessed by tenant's workers
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: tenant-nats-isolation
spec:
  podSelector:
    matchLabels:
      app: nats
      tenant: tenant-a
  ingress:
  - from:
    - podSelector:
        matchLabels:
          tenant: tenant-a
```

---

## Cost Analysis

### Resource Usage (Example: 100 Tenants)

**Platform NATS** (shared):
- 3 nodes × 4 CPU × 8GB = 12 CPU, 24GB RAM
- Storage: 300GB SSD
- **Cost**: ~$200-300/month (AWS EKS)

**Tenant NATS** (100 tenants, avg 1.5 instances each):
- 150 instances × 2 CPU × 4GB = 300 CPU, 600GB RAM
- **Cost**: ~$2000-3000/month (AWS EKS)

**Total**: ~$2500/month for 100 tenants = **$25/tenant/month**

**Scaling Economics**:
- Adding 1 tenant = ~$25/month incremental cost
- Shared Platform NATS amortizes across all tenants
- Linear scaling (predictable cost model)

---

## Future Enhancements

### Phase 2 (Post-POC)
- [ ] Multi-region Platform NATS (geo-replication)
- [ ] NATS super-cluster for cross-tenant integrations
- [ ] Auto-scaling based on predictive load
- [ ] NATS KV → PostgreSQL archival pipeline

### Phase 3 (Enterprise)
- [ ] Dedicated Platform NATS per enterprise tenant
- [ ] Custom retention policies per tenant
- [ ] NATS leafnode support for edge deployments

---

## References

- [NATS Documentation](https://docs.nats.io/)
- [JetStream Design](https://docs.nats.io/nats-concepts/jetstream)
- [NATS KV Store](https://docs.nats.io/nats-concepts/jetstream/key-value-store)
- [Project Inception](./PROJECT_INCEPTION.md)
- [Accelerated Timeline](./ACCELERATED_TIMELINE.md)

---

## Decision History

| Date | Version | Changes |
|------|---------|---------|
| 2026-01-27 | 1.0 | Initial hybrid NATS architecture approved |

---

**Status**: ✅ Approved - Ready for implementation

**Next Steps**:
1. Create GitHub issues for implementation tasks
2. Set up NATS Helm chart repository
3. Build control plane NATS provisioning API
4. Implement worker discovery service
5. Create monitoring dashboards
