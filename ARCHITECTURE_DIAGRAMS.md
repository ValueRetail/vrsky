# VRSky Architecture Diagrams & Visual References

## 1. Dual-NATS Topology

```
┌──────────────────────────────────────────────────────────────────┐
│                    PLATFORM NATS CLUSTER (HA)                   │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐              │
│  │   Node 1    │  │   Node 2    │  │   Node 3    │              │
│  └─────────────┘  └─────────────┘  └─────────────┘              │
│                                                                  │
│  Streams:                                                        │
│  ├─ message_state (TTL: 15min)                                  │
│  ├─ retry_queue (TTL: 1hr)                                      │
│  ├─ dead_letter_queue (TTL: 7 days)                            │
│  └─ integration_locks (TTL: 5min)                              │
└──────────────────────────────────────────────────────────────────┘
                             ▲
                             │ (State Tracking)
                             │
        ┌────────────────────┼────────────────────┐
        │                    │                    │
        ▼                    ▼                    ▼
   ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
   │  Tenant A   │    │  Tenant B   │    │  Tenant C   │
   ├─────────────┤    ├─────────────┤    ├─────────────┤
   │ NATS Core-1 │    │ NATS Core-1 │    │ NATS Core-1 │
   │             │    │             │    │             │
   │ 50 integ.   │    │ 30 integ.   │    │ NATS Core-2 │
   └─────────────┘    └─────────────┘    │             │
        ▲                    ▲            │ 75 integ.   │
        │                    │            └─────────────┘
     Workers              Workers              ▲
                                            Workers
```

## 2. Message Flow Through Filter

```
┌─────────────────────────────────────────────────────────────────┐
│                    FILTER MESSAGE PROCESSING                    │
│                                                                 │
│  Input Message (NATS)                                          │
│         │                                                       │
│         ▼                                                       │
│  ┌─────────────────┐                                           │
│  │ Unmarshal       │ Parse Envelope                            │
│  │ Envelope        │                                           │
│  └─────────────────┘                                           │
│         │                                                       │
│         ▼                                                       │
│  ┌─────────────────┐                                           │
│  │ Extract &       │ JSON/XML Payload                          │
│  │ Parse Payload   │                                           │
│  └─────────────────┘                                           │
│         │                                                       │
│         ▼                                                       │
│  ╔═════════════════════════════════════════════════════════╗   │
│  ║ PRIORITY 1: GATING & VALIDATION (Always On)           ║   │
│  ║                                                         ║   │
│  ║ For each rule:                                         ║   │
│  ║   1. Evaluate condition (op: ==, >, contains, etc.)   ║   │
│  ║   2. If matches → ACCEPT (first match wins)           ║   │
│  ║   3. If no matches → REJECT                           ║   │
│  ║                                                         ║   │
│  ║ Decision: ACCEPT or REJECT                            ║   │
│  ╚═════════════════════════════════════════════════════════╝   │
│         │                                                       │
│         ├─ ACCEPT → continue to Priority 2                    │
│         └─ REJECT → route to rejection_topic                  │
│         │                                                       │
│         ▼ (if accepted)                                        │
│  ╔═════════════════════════════════════════════════════════╗   │
│  ║ PRIORITY 2: CONDITIONAL ROUTING (Optional)             ║   │
│  ║                                                         ║   │
│  ║ If routing rules configured:                           ║   │
│  ║   1. Evaluate routing conditions                       ║   │
│  ║   2. Determine output_topic                            ║   │
│  ║   3. Apply transformations (add/rename fields)        ║   │
│  ║                                                         ║   │
│  ║ Decision: Override output_topic if matches             ║   │
│  ╚═════════════════════════════════════════════════════════╝   │
│         │                                                       │
│         ▼ (if accepted)                                        │
│  ╔═════════════════════════════════════════════════════════╗   │
│  ║ PRIORITY 3: RATE LIMITING (Optional)                   ║   │
│  ║                                                         ║   │
│  ║ If rate limit rules configured:                        ║   │
│  ║   1. Check current count/tokens                        ║   │
│  ║   2. Compare against limit                             ║   │
│  ║   3. Decide: queue/drop/reject if exceeded             ║   │
│  ║                                                         ║   │
│  ║ Decision: Allow or block                               ║   │
│  ╚═════════════════════════════════════════════════════════╝   │
│         │                                                       │
│         ▼                                                       │
│  ┌─────────────────┐                                           │
│  │ Update          │ Add to StepHistory                        │
│  │ Envelope        │ Add Metadata                              │
│  └─────────────────┘                                           │
│         │                                                       │
│         ▼                                                       │
│  ┌─────────────────┐                                           │
│  │ Marshal         │ Serialize to JSON                         │
│  │ Envelope        │                                           │
│  └─────────────────┘                                           │
│         │                                                       │
│         ▼                                                       │
│  ┌─────────────────────────────────────┐                       │
│  │ Publish to NATS                     │                       │
│  │ • output_topic (accepted)           │                       │
│  │ • rejection_topic (rejected)        │                       │
│  │ • custom topic (routed)             │                       │
│  └─────────────────────────────────────┘                       │
│         │                                                       │
│         ▼                                                       │
│  ┌─────────────────┐                                           │
│  │ Record Metrics  │ Prometheus counters                       │
│  │ Log Event       │ Structured logging                        │
│  └─────────────────┘                                           │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## 3. Complete Message Pipeline (End-to-End)

```
┌────────────────┐
│ External       │
│ System         │
│ (Webhook/API)  │
└────────┬───────┘
         │ HTTP POST
         ▼
┌────────────────────────────────┐
│ HTTP CONSUMER                  │
│ • Receives webhook             │
│ • Creates Envelope             │
│ • Sets TenantID, Metadata      │
│ • Type: Component.TypeConsumer │
└────────┬───────────────────────┘
         │ Publish to NATS
         │ Topic: tenant-a.webhook.received
         ▼
      [NATS]
         │
         ▼
┌────────────────────────────────┐
│ FILTER COMPONENT               │
│ • Validates message            │
│ • Evaluates gating rules       │
│ • Applies routing rules        │
│ • Enforces rate limits         │
│ • Type: Component.TypeFilter   │
└────────┬──────────────────┬────┘
         │                  │
    ACCEPT                REJECT
    (80%)                (20%)
         │                  │
         ▼                  ▼
    Output Topic    Rejection Topic
    Filter.Output   Filter.Rejection
         │                  │
         │                  ▼
         │           ┌──────────────────┐
         │           │ DLQ HANDLER      │
         │           │ • Retry logic    │
         │           │ • Platform NATS  │
         │           └──────────────────┘
         │
    [NATS]
         │
         ▼
┌────────────────────────────────┐
│ PRODUCER                       │
│ • Receives from filter         │
│ • Extracts payload             │
│ • Sends to destination         │
│ • Type: Component.TypeProducer │
└────────┬───────────────────────┘
         │ HTTP/DB/File
         ▼
┌────────────────────────────┐
│ External System            │
│ (Target API/DB)            │
└────────────────────────────┘
```

## 4. Component Interaction Network

```
                        ┌─────────────────────┐
                        │  PROMETHEUS         │
                        │  (Metrics)          │
                        └──────────┬──────────┘
                                   ▲
                                   │
                    ┌──────────────┼──────────────┐
                    │              │              │
                    ▼              ▼              ▼
            ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
            │   CONSUMER   │ │    FILTER    │ │   PRODUCER   │
            │              │ │              │ │              │
            │ • HTTP       │ │ • Gating     │ │ • HTTP       │
            │ • File       │ │ • Routing    │ │ • PostgreSQL │
            │ • Postgres   │ │ • Rate Limit │ │ • File       │
            └──────┬───────┘ └──────┬───────┘ └──────┬───────┘
                   │                │                │
                   └────────┬───────┼────────┬──────┘
                            │       │        │
                        [NATS]      │    [NATS]
                            │       │        │
                            ▼       ▼        ▼
                    ┌─────────────────────────────┐
                    │  TENANT NATS (Per-tenant)   │
                    │                             │
                    │ Topics:                     │
                    │ • webhook.received          │
                    │ • filter.input              │
                    │ • filter.output             │
                    │ • producer.send             │
                    └─────────────────────────────┘
                            ▲
                            │
                            │ (State & DLQ)
                            │
                    ┌─────────────────────────────┐
                    │  PLATFORM NATS (Shared HA)  │
                    │                             │
                    │ Streams:                    │
                    │ • message_state             │
                    │ • dead_letter_queue         │
                    │ • retry_queue               │
                    └─────────────────────────────┘
```

## 5. Filter Three-Tier Priority System

```
┌─────────────────────────────────────────────────────────────────┐
│                    FILTER PRIORITY HIERARCHY                    │
│                                                                 │
│ ╔════════════════════════════════════════════════════════════╗ │
│ ║ PRIORITY 1: GATING & VALIDATION                          ║ │
│ ║ Status: ✅ IMPLEMENTED                                    ║ │
│ ║                                                            ║ │
│ ║ Rule: IF (condition) THEN ACCEPT ELSE REJECT              ║ │
│ ║                                                            ║ │
│ ║ Operators (12):                                           ║ │
│ ║  • Comparison: ==, !=, >, <, >=, <=                      ║ │
│ ║  • String: contains, startswith, endswith, regex_match   ║ │
│ ║  • Collection: in_list                                   ║ │
│ ║  • Special: always                                       ║ │
│ ║                                                            ║ │
│ ║ Example: amount >= 50 → ACCEPT                           ║ │
│ ╚════════════════════════════════════════════════════════════╝ │
│                          ↓                                      │
│ ╔════════════════════════════════════════════════════════════╗ │
│ ║ PRIORITY 2: CONDITIONAL ROUTING                          ║ │
│ ║ Status: ✅ CODE READY (not deployed)                      ║ │
│ ║                                                            ║ │
│ ║ Rule: IF (condition) THEN route to {topic}               ║ │
│ ║       AND apply {transformations}                        ║ │
│ ║                                                            ║ │
│ ║ Features:                                                 ║ │
│ ║  • Multiple output topics                                ║ │
│ ║  • Message transformations                               ║ │
│ ║  • Weighted routing (A/B testing)                        ║ │
│ ║                                                            ║ │
│ ║ Example: amount >= 10000 → route to "premium" topic      ║ │
│ ║          add field: tier = "premium"                     ║ │
│ ╚════════════════════════════════════════════════════════════╝ │
│                          ↓                                      │
│ ╔════════════════════════════════════════════════════════════╗ │
│ ║ PRIORITY 3: RATE LIMITING                                ║ │
│ ║ Status: ✅ CODE READY (not deployed)                      ║ │
│ ║                                                            ║ │
│ ║ Rule: Track count/tokens, enforce limits                 ║ │
│ ║                                                            ║ │
│ ║ Strategies:                                               ║ │
│ ║  • time_window: max messages per time period             ║ │
│ ║  • concurrent: max simultaneous messages                 ║ │
│ ║  • token_bucket: smooth throttling                       ║ │
│ ║                                                            ║ │
│ ║ Exceed Actions:                                           ║ │
│ ║  • queue: defer to later                                 ║ │
│ ║  • drop: silently discard                                ║ │
│ ║  • reject: send to rejection topic                       ║ │
│ ║                                                            ║ │
│ ║ Example: max 100 messages/minute                         ║ │
│ ║          exceed action: queue                            ║ │
│ ╚════════════════════════════════════════════════════════════╝ │
│                          ↓                                      │
│              ┌──────────────────┐                              │
│              │ OUTPUT           │                              │
│              │ • Accepted       │                              │
│              │ • Routed         │                              │
│              │ • Rate-Limited   │                              │
│              └──────────────────┘                              │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## 6. Envelope Data Structure Flow

```
External System
    │
    │ (raw data)
    ▼
┌──────────────────────┐
│ Envelope Creation    │
│                      │
│ ID (UUID)            │
│ TenantID             │
│ IntegrationID        │
│ Payload (JSON)       │
│ ContentType          │
│ CreatedAt            │
│ ExpiresAt (TTL)      │
│ StepHistory: []      │
│ Metadata: {}         │
└──────────┬───────────┘
           │ (published to NATS)
           ▼
        [NATS]
           │
           ▼
┌──────────────────────┐
│ Filter Processing    │
│                      │
│ Reads:               │
│ • Payload            │
│ • TenantID           │
│ • Metadata           │
│                      │
│ Modifies:            │
│ • StepHistory        │
│ • Metadata           │
│ • (potentially)      │
│   Payload (Priority 2)
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ Updated Envelope     │
│                      │
│ ID (UUID)            │
│ TenantID             │
│ IntegrationID        │
│ Payload (modified?)  │
│ ContentType          │
│ CreatedAt            │
│ ExpiresAt            │
│ StepHistory:         │
│ [filter:ACCEPT]      │
│ Metadata:            │
│ {                    │
│   routing_rule_id:   │
│   routed_to: topic   │
│ }                    │
└──────────┬───────────┘
           │ (published to output topic)
           ▼
        [NATS]
           │
           ▼
      Producer
           │
           ▼
   External System
```

## 7. Multi-Tenant Isolation Model

```
┌────────────────────────────────────────────────────────────────┐
│              TENANT ISOLATION ARCHITECTURE                     │
│                                                                │
│  ┌──────────────────────────────────────────────────────┐    │
│  │ TENANT A                                             │    │
│  │                                                      │    │
│  │ Account: tenant-a                                   │    │
│  │ NATS Instance: nats-tenant-a-1:4222                │    │
│  │                                                      │    │
│  │ Topics (isolated):                                  │    │
│  │ • tenant-a.webhook.received                         │    │
│  │ • tenant-a.filter.input                            │    │
│  │ • tenant-a.filter.output                           │    │
│  │ • tenant-a.postgres.changes                        │    │
│  │ • tenant-a.producer.send                           │    │
│  │                                                      │    │
│  │ Credentials: JWT token (auto-rotated monthly)      │    │
│  │ Network Policy: Only tenant-a workers can access   │    │
│  └──────────────────────────────────────────────────────┘    │
│                                                                │
│  ┌──────────────────────────────────────────────────────┐    │
│  │ TENANT B                                             │    │
│  │                                                      │    │
│  │ Account: tenant-b                                   │    │
│  │ NATS Instance: nats-tenant-b-1:4222                │    │
│  │                                                      │    │
│  │ Topics (isolated):                                  │    │
│  │ • tenant-b.webhook.received                         │    │
│  │ • tenant-b.filter.input                            │    │
│  │ • tenant-b.filter.output                           │    │
│  │ • (etc - completely separate)                      │    │
│  │                                                      │    │
│  │ Credentials: JWT token (auto-rotated monthly)      │    │
│  │ Network Policy: Only tenant-b workers can access   │    │
│  └──────────────────────────────────────────────────────┘    │
│                                                                │
│  ┌──────────────────────────────────────────────────────┐    │
│  │ PLATFORM NATS (Shared)                              │    │
│  │                                                      │    │
│  │ Streams (all tenants write here):                   │    │
│  │ • message_state (cross-tenant safe)                 │    │
│  │ • dead_letter_queue (cross-tenant safe)             │    │
│  │ • retry_queue (cross-tenant safe)                   │    │
│  │                                                      │    │
│  │ Access Control:                                      │    │
│  │ • Credentials: Service account (control plane)      │    │
│  │ • TLS encryption: In-cluster only                   │    │
│  │ • No direct tenant access                           │    │
│  └──────────────────────────────────────────────────────┘    │
│                                                                │
└────────────────────────────────────────────────────────────────┘

Isolation Mechanisms:
1. Topic Namespacing: {tenant-id}.{component}.{stream}
2. Account Isolation: NATS auth prevents cross-tenant subscriptions
3. Network Policies: Kubernetes enforces pod-level access
4. Credentials Rotation: Monthly JWT token rotation
5. Metrics Isolation: Prometheus labels per tenant
```

## 8. Filter Configuration Example

```yaml
# /home/ludvik/vrsky/filter-config.yml
filter_id: orders-validator
input_topic: tenant-a.orders.incoming
output_topic: tenant-a.orders.valid
rejection_topic: tenant-a.orders.invalid

# PRIORITY 1: Gating Rules
rules:
  - name: minimum_amount
    condition:
      operator: ">="
      field: amount
      value: 50
  
  - name: valid_country
    condition:
      operator: "in_list"
      field: country
      value: ["US", "CA", "MX"]
  
  - name: recent_orders
    condition:
      operator: "contains"
      field: tags
      value: "express"

# PRIORITY 2: Routing Rules (Optional)
routing_rules:
  - id: route_high_value
    priority: 10
    condition:
      operator: ">="
      field: amount
      value: 10000
    output_topic: tenant-a.orders.premium
    transformations:
      - action: add_field
        field: tier
        value: "premium"
      - action: add_field
        field: priority
        value: "high"
  
  - id: route_standard
    priority: 100
    condition:
      operator: "always"
    output_topic: tenant-a.orders.standard

# PRIORITY 3: Rate Limiting Rules (Optional)
rate_limit_rules:
  - id: per_customer_limit
    strategy: time_window
    max_messages_per_window: 100
    window_duration_seconds: 60
    exceed_action: queue
    queue_size: 1000
    condition:
      operator: "always"
  
  - id: high_volume_throttle
    strategy: token_bucket
    token_bucket_rate: 1000
    token_bucket_capacity: 5000
    exceed_action: drop
    condition:
      operator: "always"
```

---

## Summary

These diagrams show:
1. **Dual-NATS topology** - Platform cluster + tenant instances
2. **Message flow** - Complete processing from input to output
3. **End-to-end pipeline** - Consumer → Filter → Producer
4. **Component network** - How all components interact
5. **Filter priorities** - Three-tier decision making
6. **Envelope structure** - Message container evolution
7. **Tenant isolation** - Multi-tenant separation
8. **Configuration** - YAML example with all options

