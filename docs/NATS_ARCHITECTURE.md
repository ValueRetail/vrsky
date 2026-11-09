# VRSky NATS Architecture Decision

**Date**: January 27, 2026  
**Status**: Approved — **partially implemented**, see the note below  
**Decision Type**: Architecture Design Record (ADR)

!!! warning "Per-tenant NATS instances are provisioned but not routed to (#209)"

    Everything in this document about the **platform** NATS is live. The
    **tenant-scoped** half is not, and the gap is invisible unless you look for
    it: instances are provisioned, connections are placed onto them in the
    database, and the autoscaler meters those placements — but no data reaches
    them. The services that move data are standing per-node-type deployments
    (ADR 0004) that dial the platform NATS from their own pod environment.

    So a connection "placed on" a dedicated instance still moves its traffic
    over the shared one. Nothing is broken today, because prod runs a single
    platform NATS. It becomes wrong the moment a tenant is told they have a
    dedicated instance.

    The "Worker Connection Strategy" section below describes per-connection
    worker pods that **no longer exist** — #201 and #205 replaced them. Read it
    as the original design, not as current behaviour.

    Before building any of this, [#209](https://github.com/ValueRetail/vrsky/issues/209)
    needs a product answer: is a dedicated instance sold as *performance
    isolation* or as *data isolation*? They have different implementations, and
    only one of them can be honestly claimed by a shared-broker design.

---

## Context

VRSky requires a messaging infrastructure that supports:

- High throughput (measured at ~15,000 msg/s sustained end-to-end on a single dev host — see [LOAD.md](LOAD.md))
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
   - Used for the data stream (`VRSKY_DATA`), its redeliveries and the
     dead-letter stream (`VRSKY_DLQ`)
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
│  JetStream streams:                                         │
│  ├─ VRSKY_DATA  vrsky.data.>  (72 h)                        │
│  └─ VRSKY_DLQ   vrsky.dlq.>   (7 days)                      │
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

| Property        | Value                                        |
| --------------- | -------------------------------------------- |
| **Deployment**  | 3-5 node HA cluster (Kubernetes StatefulSet) |
| **NATS Mode**   | JetStream enabled + NATS KV                  |
| **Replication** | R3 (3-way replication for durability)        |
| **Resources**   | 4 CPU, 8GB RAM per node                      |
| **Storage**     | 100GB SSD per node (JetStream streams)       |
| **Uptime SLA**  | 99.9% (HA with automatic failover)           |

**Use Cases**:

- ✅ The data stream: every pipeline message, one durable consumer per worker
- ✅ Retries: JetStream redelivery of a NAK'd message, with backoff
- ✅ Dead letter queue: the `VRSKY_DLQ` stream (failed messages)
- ✅ Platform-wide events (tenant provisioning, etc.)
- ✅ Cross-tenant integrations (future feature)

The original design also had KV buckets for message state, integration
locks, a retry queue and a dead-letter queue. They were never built: retries
are JetStream redeliveries and the DLQ is a stream. What exists is in
`pkg/messaging` (`messaging.go`, `subscriber.go`, `dlq.go`):

| Stream       | Subjects       | Retention                    | Duplicate window |
| ------------ | -------------- | ---------------------------- | ---------------- |
| `VRSKY_DATA` | `vrsky.data.>` | 72 h, size caps, oldest out  | 5 min            |
| `VRSKY_DLQ`  | `vrsky.dlq.>`  | 7 days                       | default (2 min)  |

---

### Tenant-Scoped NATS Instances

**Purpose**: Fast, ephemeral message transport within tenant boundary

| Property       | Value                                       |
| -------------- | ------------------------------------------- |
| **Deployment** | Single-node NATS Core (Kubernetes Pod)      |
| **NATS Mode**  | Core only (no JetStream, no KV)             |
| **Resources**  | 2 CPU, 4GB RAM per instance                 |
| **Storage**    | None (ephemeral, in-memory only)            |
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

!!! danger "Not implemented — describes deleted per-connection workers"

    The per-connection worker pods this section describes were removed in #201
    and #205. Nothing in the running system executes this strategy; the standing
    connector services dial one NATS URL from their pod environment. Kept as a
    record of the intended design, because it is one of the candidate answers to
    #209.

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

### What a worker does with a message

There is no state store: the attempt count is JetStream's own delivery count
(`NumDelivered`) on the worker's durable consumer, which has
`MaxDeliver: 5` and the backoff 1 s, 5 s, 30 s, 2 min
(`messaging.DefaultBackoff`). `Subscriber.dispatch` in
`pkg/messaging/subscriber.go` runs the handler and then:

```
handler returns nil                      → Ack
handler returns an error, delivery < 5   → Nak (redelivered after the backoff)
handler returns an error, delivery = 5   → copy to vrsky.dlq.<tenant>.pipeline.<conn>
                                           with X-Vrsky-Last-Error, X-Vrsky-Delivered
                                           and X-Vrsky-Worker, then Ack
                                           (if the DLQ publish fails: Nak)
handler panics                           → Nak
```

SDK connectors don't call this directly. The SDK runner maps `Deliver`'s
result onto it: nil acks, `sdk.Permanent` acks and drops (logged, never
dead-lettered), anything else is returned and so NAK'd
(`pkg/sdk/lifecycle.go`).

### AckWait, in-progress heartbeats & redelivery (#139)

The durable consumer's **AckWait** is the crash-recovery window — how long
JetStream waits for an ack before assuming the worker died and redelivering. It
is **not** the per-message time budget: while a handler runs, the subscriber
sends periodic `InProgress()` heartbeats (every `AckWait/2`) that reset the
server-side timer, so a handler can legitimately run far longer than AckWait
(slow producer target, large payload, bulk API) **without** triggering a
duplicate delivery (`pkg/messaging/subscriber.go`).

- JetStream stores `Backoff[0]` as the effective AckWait when a backoff schedule
  is set, so the two are always kept aligned to avoid the re-bind crash-loop in
  [#99]. AckWait is tunable per worker via the `WORKER_ACK_WAIT` env (a Go
  duration, e.g. `2m`); raising it on an existing durable reconciles the
  consumer in place (`UpdateConsumer`) rather than failing to re-bind.
- **Idempotency / dedup:** delivery is at-least-once. Producers publish with a
  stable `Msg-Id` (the envelope ID), and the main stream's **5-minute duplicate
  window** (`Duplicates: 5*time.Minute`) collapses re-published duplicates. The
  full redelivery-to-DLQ backoff schedule (≈156s) fits well inside that window,
  so a message that is NAK'd and retried across the whole schedule is still
  deduplicated. Downstream sinks should additionally treat writes as idempotent
  where the target allows it (e.g. upsert by key).

### Dead Letter Queue

A message that fails five deliveries (`MaxDeliveryAttempts`; redelivered after
1 s, 5 s, 30 s and 2 min) is dead-lettered:

1. The message is copied to the `VRSKY_DLQ` stream with the last error and
   the attempt count, and acked on the data stream.
2. `vrsky_dlq_messages_total` goes up, and the `DLQGrowing` alert fires
   when a pipeline dead-letters messages for 10 minutes. The metric has no
   tenant label, so the alert goes to the platform's notification targets,
   not the tenant's. Nothing is sent per message.
3. The message is kept for 7 days.
4. The connection's DLQ view in the UI lists the messages and can retry one
   (republished to the data stream, so every worker sees it again) or
   discard it.

A destination that returns `sdk.Permanent` is not retried at all: the message
is acked and dropped, and never reaches the DLQ. Which errors each destination
marks permanent is in the
[operator troubleshooting guide](operator/troubleshooting.md#which-failed-messages-reach-the-dlq).

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
          image: nats:2.12-alpine
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
          image: nats:2.12-alpine
          args:
            - "-m"
            - "8222" # Monitoring port
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

  # Built as DLQGrowing in infrastructure/prometheus-rules.yml; it goes to
  # the platform's targets, not the tenant's (see "Dead Letter Queue").
  - name: DLQGrowing
    condition: increase(vrsky_dlq_messages_total[10m]) > 0 for 10min
    action: Alert platform + investigate
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

---

## Decision History

| Date       | Version | Changes                                   |
| ---------- | ------- | ----------------------------------------- |
| 2026-01-27 | 1.0     | Initial hybrid NATS architecture approved |

---

**Status**: ✅ Approved - Ready for implementation

**Next Steps**:

1. Create GitHub issues for implementation tasks
2. Set up NATS Helm chart repository
3. Build control plane NATS provisioning API
4. Implement worker discovery service
5. Create monitoring dashboards
