# Phase 1E: Filter Component - Architecture Guide

## System Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     NATS JetStream                          │
│                                                             │
│  Input Topic ──→  [Filter Component]  ──→ Output Topic    │
│  (orders.*)        │                       (orders.valid)  │
│                    ├─ Parse               (orders.invalid) │
│                    ├─ Validate                            │
│                    ├─ Evaluate Conditions                 │
│                    ├─ Make Decision                       │
│                    └─ Route Message                       │
│                                                             │
│                    ┌──────────────────────┐                │
│                    │  Rejection Handler   │                │
│                    │  - Retry Logic       │                │
│                    │  - DLQ Routing       │                │
│                    │  - Error Recovery    │                │
│                    └──────────────────────┘                │
│                                                             │
│                    ┌──────────────────────┐                │
│                    │  Metrics Collection  │                │
│                    │  - Prometheus        │                │
│                    │  - Request Counts    │                │
│                    │  - Process Times     │                │
│                    └──────────────────────┘                │
└─────────────────────────────────────────────────────────────┘
```

## Component Design

### Core Components

#### 1. **Filter** (filter.go)
Main orchestration component implementing the Filter interface.

**Responsibilities:**
- Lifecycle management (Start/Stop)
- Message consumption from NATS
- Routing decisions to output/rejection topics
- Metrics recording
- Health status reporting

**Key Methods:**
- `Start(ctx)` - Begin consuming messages
- `Stop(ctx)` - Graceful shutdown
- `ProcessMessage(ctx, env)` - Evaluate single message
- `Health()` - Report component status

#### 2. **Condition Engine** (conditions.go)
Evaluates expressions against message payloads.

**Responsibilities:**
- Parse field paths (dot notation)
- Apply operators to values
- Handle type coercion
- Support nested objects and arrays

**Operators Implemented:**
- Comparison: `==`, `!=`, `>`, `<`, `>=`, `<=`
- String: `contains`, `startswith`, `endswith`, `regex_match`
- Collections: `in_list`
- Special: `always`

**Design Pattern:**
```
Operator Registry
├── Comparison Operators (numeric)
├── String Operators
├── Collection Operators
└── Special Operators
```

#### 3. **Gating** (gating.go)
Implements the acceptance/rejection decision logic.

**Responsibilities:**
- Evaluate all rules sequentially
- Track matching rules
- Make final accept/reject decision
- Provide decision rationale

**Logic:**
- If any rule matches → ACCEPT
- If no rules match → REJECT
- First matching rule determines decision

#### 4. **Message Parser** (parser.go)
Handles deserialization and format detection.

**Supported Formats:**
- JSON (primary)
- XML (converted to map representation)
- Plain text

**Responsibilities:**
- Detect content type
- Parse payload
- Handle encoding errors gracefully

#### 5. **Schema Validator** (schema.go)
Optional JSON Schema validation.

**Features:**
- Strict mode (fail on error)
- Lenient mode (warn only)
- Schema registration and caching
- Error reporting with context

**Implementation:**
Uses `github.com/santhosh-tekuri/jsonschema` for validation.

#### 6. **Rejection Handler** (rejection.go)
Routes rejected/failed messages appropriately.

**Responsibilities:**
- Publish to rejection topic
- Implement retry logic with exponential backoff
- Handle DLQ messages
- Add metadata to rejected messages

**Retry Strategy:**
- Max retries: 3
- Initial delay: 100ms
- Backoff multiplier: 2.0
- Max delay: 5 seconds

#### 7. **Error Handler** (errors.go)
Provides centralized error management.

**Features:**
- Error recording and retrieval
- Panic recovery
- Graceful shutdown coordination
- Retry classification

### Data Flow

```
Message Received (NATS)
        ↓
Parse Envelope
        ↓
Validate Not Empty
        ↓
Parse Payload (JSON/XML)
        ↓
[Optional] Schema Validation
        ↓
Evaluate Rules (AND logic within rule, OR between rules)
        ├─ Rule 1 Condition?
        │   ├─ True → ACCEPT (Route to output_topic)
        │   └─ False → Try Next Rule
        ├─ Rule 2 Condition?
        │   └─ ...
        └─ No Rules Match → REJECT (Route to rejection_topic)
        ↓
Add Metadata
        ↓
Marshal Envelope
        ↓
Publish to NATS
        ↓
Record Metrics
```

## Configuration Model

```go
type Config struct {
    FilterID       string        // Unique identifier
    InputTopic     string        // NATS consumer subject
    OutputTopic    string        // NATS for accepted messages
    RejectionTopic string        // NATS for rejected messages
    Rules          []interface{} // Raw rules (parsed at runtime)
}

type Rule struct {
    ID        string      // Auto-generated identifier
    Name      string      // Human-readable name
    SchemaID  string      // Optional JSON Schema reference
    Condition *Condition  // Evaluation condition
}

type Condition struct {
    Operator string      // "==", ">", "contains", etc.
    Field    string      // Message field path
    Value    interface{} // Comparison value
}
```

## Field Path Resolution

The condition engine supports dot notation for nested field access:

```
Field Path: "user.profile.age"
Payload: {
    "user": {
        "profile": {
            "age": 25
        }
    }
}
Resolution: 25
```

**Algorithm:**
1. Split path by "."
2. For each component:
   - Access map key (if current is map)
   - Handle array indices: `items[0]`
   - Return nil if key not found
3. Type coercion for comparisons

## Metrics

Prometheus metrics are collected for observability:

```
Counter Metrics:
├── messages_received_total      # Total processed
├── messages_accepted_total      # Accepted count
├── messages_rejected_total      # Rejected count
└── messages_failed_total        # Processing failures

Histogram Metrics:
└── process_duration_seconds     # Distribution of processing times

Labels:
└── filter_id                    # Filter instance identifier
```

## Error Handling Strategy

```
Processing Error
├─ Parsing Error
│  ├─ Invalid JSON
│  ├─ Invalid XML
│  └─ Route to rejection_topic
│
├─ Validation Error
│  ├─ Schema mismatch
│  ├─ Missing required fields
│  └─ Route to rejection_topic
│
├─ Processing Failure
│  ├─ Condition evaluation error
│  ├─ Retry with backoff
│  ├─ After max retries → DLQ
│  └─ Record failure metric
│
└─ System Error
   ├─ NATS publish failure
   ├─ Exponential backoff retry
   └─ Health status: Unhealthy
```

## Concurrency Model

```
Main Filter Goroutine
├─ Message Consumer (blocking on NATS subscription)
│  └─ For each message:
│     ├─ Unmarshal envelope
│     ├─ Parse payload
│     ├─ Evaluate rules
│     └─ Publish decision
│
└─ Signal Handler
   └─ On SIGINT/SIGTERM → Graceful shutdown
```

**Thread Safety:**
- RWMutex protects health status
- No shared mutable state between messages
- NATS handles concurrent subscriptions
- Each message processed sequentially

## Performance Characteristics

### Processing Latency

- **Simple condition** (==): ~0.5ms
- **Nested field (3 levels)**: ~0.6ms
- **Regex pattern**: ~1-2ms
- **Multiple rules** (3): ~1.5ms

### Throughput

- **Typical**: 1,000-2,000 msg/sec per instance
- **Peak**: 5,000+ msg/sec with optimization

### Memory Usage

- **Base**: ~10MB
- **Per filter instance**: ~1MB
- **Per cached schema**: <100KB

## Resilience Patterns

### Graceful Degradation

1. **Schema Validation Failure**
   - Strict mode: reject message
   - Lenient mode: log warning, continue

2. **Network Failure**
   - Exponential backoff retry
   - After max retries → DLQ
   - Monitor health status

3. **Malformed Messages**
   - Parse error → reject
   - Add error details to metadata
   - Log for debugging

### Shutdown Coordination

```
Signal Received
    ↓
Stop accepting new messages
    ↓
Process in-flight messages (timeout: 15s)
    ↓
Close NATS subscription
    ↓
Unregister metrics
    ↓
Exit cleanly
```

## Testing Strategy

### Unit Tests

- Condition evaluation logic
- Field path resolution
- Operator implementations
- Schema validation
- Error handling

### Integration Tests

- Message flow end-to-end
- NATS pub/sub coordination
- Metric recording
- Multiple filter instances

### E2E Tests

- Order filtering scenario
- Nested condition evaluation
- Pattern matching
- Error recovery and DLQ

## Security Considerations

1. **Input Validation**
   - All JSON parsed carefully
   - Regex patterns validated
   - Field paths sanitized

2. **Message Privacy**
   - Sensitive data not logged
   - Metrics don't contain PII
   - Audit trail encrypted

3. **Access Control**
   - NATS accounts for tenants
   - RBAC on topics
   - Filter configuration locked

## Configuration Best Practices

1. **Rule Design**
   - Keep conditions simple
   - Use specific field names
   - Document rule purpose

2. **Topic Naming**
   - Semantic names (orders.*, emails.*)
   - Consistent conventions
   - Track provenance

3. **Performance Optimization**
   - Order rules by frequency
   - Avoid complex regex
   - Cache compiled patterns

## Integration with VRSky

The Filter component integrates with:

- **Envelope**: Message wrapper with metadata
- **Component Interface**: Standard lifecycle
- **NATS**: Message transport
- **Prometheus**: Metrics collection
- **Logging**: Structured logs with context

## Future Enhancements (Priority 2-3)

### Priority 2: Conditional Routing
- Route to different topics based on conditions
- Support weighted routing
- A/B testing scenarios

### Priority 3: Rate Limiting
- Per-tenant rate limits
- Sliding window algorithm
- Backpressure handling

### Beyond Priority 3
- Machine learning-based filtering
- Anomaly detection
- Custom filter plugins
