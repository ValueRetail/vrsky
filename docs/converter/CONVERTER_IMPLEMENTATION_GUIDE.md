# VRSky Converter Implementation Guide

## Overview

The Converter is a core component of the VRSky integration platform responsible for transforming data from any source format to any target format. It's designed to be highly scalable, multi-tenant capable, and extensible through pluggable functions.

## Architecture

### High-Level Data Flow

```
Input Message (NATS)
        ↓
   Converter
        ├→ Parse Input Schema (validation)
        ├→ Extract Fields (FieldMapper)
        ├→ Evaluate Rules (RuleEngine)
        ├→ Apply Transformations
        ├→ Execute Functions (FunctionRegistry)
        ├→ Validate Output Schema
        ├→ Cache Results
        ↓
Output Message (NATS) / Error Topic (NATS)
```

### Core Components

#### 1. Converter (converter.go)
The main orchestrator that coordinates the transformation pipeline.

```go
type Converter struct {
    config       *ConverterConfig
    nats         *nats.Conn
    logger       Logger
    metrics      *Metrics
    funcRegistry *FunctionRegistry
    ruleEngine   *RuleEngine
    fieldMapper  *FieldMapper
    validator    *SchemaValidator
    cache        *FunctionCache
    closedOnce   sync.Once
    closed       bool
}
```

**Key Methods**:
- `New()` - Create a new converter instance
- `Start()` - Initialize and start listening to input topic
- `Stop()` - Gracefully shutdown the converter
- `ProcessMessage()` - Transform a single message
- `Health()` - Check component health

#### 2. Configuration (config.go)
Manages converter configuration from multiple sources with automatic hot-reload.

```go
type ConverterConfig struct {
    ConverterID string
    TenantID string
    InputTopic string
    OutputTopic string
    ErrorTopic string
    Transformations []Transformation
    InputSchema *ValidationSchema
    OutputSchema *ValidationSchema
    ErrorHandling ErrorHandlingConfig
    MaxRetries int
    RetryBackoff string
}
```

**Configuration Sources** (priority order):
1. ConfigService API (highest priority, hot-reload every 60s)
2. Environment variables
3. Local config files
4. Defaults

#### 3. Schema Validator (validator.go)
Provides thread-safe JSON Schema validation for input/output data.

```go
type SchemaValidator struct {
    schemas  map[string]*jsonschema.Schema
    compiler *jsonschema.Compiler
}
```

**Features**:
- Multi-tenant schema isolation
- JSON Schema v5 support
- Thread-safe concurrent validation
- Schema caching for performance
- Required field validation

#### 4. Rule Engine (rule_engine.go)
Evaluates conditional rules to determine if transformations apply.

```go
type RuleEngine struct {
    evaluator *ExpressionEvaluator
}
```

**Supported Operations**:
- Comparison: `==`, `!=`, `>`, `<`, `>=`, `<=`
- String: `contains`, `startswith`, `endswith`, `regex_match`
- Collection: `in_list`
- Logical: `&&`, `||`, `!`

**Example Rules**:
```
order.total > 5000
customer.status == "gold"
product.category in_list ["electronics", "furniture"]
```

#### 5. Expression Evaluator (expression_evaluator.go)
Evaluates mathematical and logical expressions for calculated fields.

```go
type ExpressionEvaluator struct{}
```

**Supported Operations**:
- Arithmetic: `+`, `-`, `*`, `/`, `%`
- Comparison: `==`, `!=`, `>`, `<`, `>=`, `<=`
- Logical: `&&`, `||`, `!`
- Variables: `${variable_name}` notation

**Example Expressions**:
```
sum(order.line_items[].price)
${discount_rate} * ${order_total}
```

#### 6. Field Mapper (field_mapper.go)
Extracts fields from input using JSONPath notation.

```go
type FieldMapper struct{}
```

**JSONPath Support**:
- Simple paths: `order.customer.email`
- Array access: `order.items[0].price`
- Wildcards: `order.items[*].price`
- Filters: `order.items[@.qty > 5]`

**Example Mappings**:
```yaml
- source: order.customer.email
  target: customer_email
  
- source: order.items[*].price
  target: item_prices
```

#### 7. Function Registry (function_registry.go)
Manages built-in and plugin functions for data transformation.

```go
type FunctionRegistry struct {
    functions map[string]TransformFunction
}
```

**Built-in Functions** (15+):
- `uuid()` - Generate UUID v4
- `now()` - Current timestamp
- `upper()`, `lower()` - String case
- `substring()`, `concat()` - String manipulation
- `split()`, `join()` - String splitting/joining
- `length()` - Get length
- `contains()` - String contains check
- `trim()` - Remove whitespace
- `replace()` - String replacement
- `toNumber()`, `toString()` - Type conversion
- `parseJSON()`, `stringifyJSON()` - JSON handling
- `lookup_postgres()` - Database queries
- `lookup_http()` - HTTP requests
- `cache_get()`, `cache_set()` - Caching

#### 8. Function Cache (function_cache.go)
Caches function results to improve performance.

```go
type FunctionCache struct {
    cache  map[string]CacheEntry
    ttlMap map[string]time.Time
}
```

**Features**:
- In-memory caching with TTL
- Automatic expiration
- Per-function TTL configuration
- Thread-safe concurrent access

#### 9. Lookup Backends
Support for external data sources:

**PostgreSQL Backend** (lookup_postgres.go):
```go
type PostgresLookup struct {
    connPool *pgxpool.Pool
    timeout  time.Duration
}
```

**HTTP Backend** (lookup_http.go):
```go
type HTTPLookup struct {
    client  *http.Client
    timeout time.Duration
}
```

**Composite Backend** (lookup_composite.go):
- Fallback strategy (try primary, then fallback)
- Retry logic with exponential backoff
- Error handling per backend

#### 10. WASM Plugin Framework (wasm_functions.go)
Execute WebAssembly modules as transformation functions.

```go
type WASMFunction struct {
    module  *wasm.Module
    runtime wasm.Runtime
    memory  *wasm.Memory
}
```

**Features**:
- Dynamic module loading
- Type marshaling (JSON ↔ WASM types)
- Memory management
- Error propagation
- Sandboxed execution

## Processing Pipeline

### Step-by-Step Transformation Process

```
1. Receive Message from NATS
   ├─ Increment messages_received counter
   └─ Start transformation timer

2. Validate Input (if InputSchema defined)
   ├─ Load schema from validator
   ├─ Validate message against schema
   └─ Handle validation errors per strategy

3. Extract Fields (FieldMapper)
   ├─ Parse JSONPath expressions
   ├─ Extract values from input
   └─ Build context for transformations

4. For Each Transformation:
   ├─ Evaluate condition (if present)
   │  ├─ If condition false, skip transformation
   │  └─ If condition true, continue
   │
   ├─ Resolve source value
   │  ├─ From Source field (simple mapping)
   │  ├─ From Function call (function.go)
   │  ├─ From Expression (expression_evaluator.go)
   │  └─ From Value (static value)
   │
   ├─ Apply transformations
   │  ├─ Type conversion
   │  ├─ Caching lookup (function_cache.go)
   │  └─ Error handling per strategy
   │
   └─ Add to output

5. Validate Output (if OutputSchema defined)
   ├─ Load schema from validator
   ├─ Validate transformed data
   └─ Handle validation errors per strategy

6. Record Metrics
   ├─ transformation_duration_seconds
   ├─ messages_succeeded_total (or messages_failed_total)
   └─ retry_attempts_total

7. Publish Results
   ├─ Success → Output Topic
   ├─ Failure (after retries) → Error Topic
   └─ Retry eligible → Queue for retry
```

### Error Handling Strategies

The converter supports three error handling strategies for different error types:

#### MissingFields Strategy
Applied when required input fields are missing.

- `skip` - Skip the field, proceed with transformation
- `coerce` - Use default/null value
- `fail` - Reject the message, send to error topic

#### TypeMismatch Strategy
Applied when type conversion fails.

- `skip` - Skip the field
- `coerce` - Use original value or convert forcefully
- `fail` - Reject the message

#### ValidationError Strategy
Applied when schema validation fails.

- `skip` - Skip validation, proceed anyway
- `coerce` - Attempt to fix data (type conversion, etc.)
- `fail` - Reject the message

### Retry Logic

Failed messages are retried with exponential backoff:

```
Attempt 1: Immediate
Attempt 2: Wait 1 second
Attempt 3: Wait 10 seconds
Attempt 4: Wait 100 seconds
Max retries: 3 (default)
```

After max retries, the message is published to the error topic with full context.

## Multi-Tenancy

The converter is designed for strong multi-tenant isolation:

### Tenant Isolation
- Each converter instance is bound to a single tenant via `TENANT_ID` config
- NATS topics include tenant prefix: `{TENANT_ID}.{topic_name}`
- Schemas are stored per-tenant in the validator
- Configuration is loaded per-tenant from ConfigService

### Example Multi-Tenant Deployment
```
Converter Instance 1:
  TENANT_ID: acme-corp
  INPUT_TOPIC: acme-corp.webhook.received
  OUTPUT_TOPIC: acme-corp.webhook.converted
  Schemas: Stored per ACME Corp

Converter Instance 2:
  TENANT_ID: example-inc
  INPUT_TOPIC: example-inc.webhook.received
  OUTPUT_TOPIC: example-inc.webhook.converted
  Schemas: Stored per Example Inc
```

## Performance Optimizations

### 1. Function Result Caching
Functions like `lookup_postgres()` and `lookup_http()` have results cached:

```go
// First call: Database query executed
result = lookup_customer(customer_id) // 50ms

// Subsequent calls within TTL: Cache hit
result = lookup_customer(customer_id) // <1ms
```

### 2. Schema Caching
Compiled JSON schemas are cached:

```go
// First validation: Schema compiled
ValidateInput(tenant, schema, data) // 5ms

// Subsequent validations: Cached schema
ValidateInput(tenant, schema, data) // 1ms
```

### 3. Connection Pooling
PostgreSQL connections are pooled for reuse:

```go
PostgresLookup:
  MaxConnections: 25
  Connection Timeout: 5s
  Idle Timeout: Auto-close after 5 minutes
```

### 4. Concurrency
The converter processes messages concurrently:

```go
MAX_CONCURRENT_REQUESTS: 1000 (configurable)
Each message processed in separate goroutine
Automatic backpressure if queue fills up
```

## Extension Points

### Custom Functions

Register custom functions via FunctionRegistry:

```go
funcRegistry := NewFunctionRegistry()

// Add custom function
funcRegistry.Register("my_function", &CustomFunction{
    Execute: func(ctx context.Context, args ...interface{}) (interface{}, error) {
        // Implementation
        return result, nil
    },
})
```

### WASM Plugins

Deploy WASM modules as transformation functions:

```bash
# Build WASM module
cargo build --target wasm32-wasi

# Deploy module
kubectl cp target/wasm32-wasi/release/my_plugin.wasm \
  pod:/app/wasm-modules/my_plugin.wasm

# Use in transformation
"function": "wasm:my_plugin:transform"
```

### Lookup Backends

Implement custom lookup backends:

```go
type CustomLookup struct{}

func (c *CustomLookup) Lookup(ctx context.Context, key string) (interface{}, error) {
    // Your implementation
    return value, nil
}

// Register in composite backend
compositeBackend.AddBackend("custom", customLookup)
```

## Health Checks

### Liveness Probe
```
GET /health
Returns 200 OK if converter is running
Used by Kubernetes to restart unhealthy pods
```

### Readiness Probe
```
GET /ready
Returns 200 OK if converter is:
  - Connected to NATS
  - Connected to ConfigService
  - Ready to accept messages
Used by Kubernetes to route traffic
```

## Metrics

### Prometheus Metrics

The converter exports metrics on port 8080 at `/metrics`:

- `vrsky_converter_messages_received_total{converter_id, tenant_id}` - Counter
- `vrsky_converter_messages_succeeded_total{converter_id, tenant_id}` - Counter
- `vrsky_converter_messages_failed_total{converter_id, tenant_id}` - Counter
- `vrsky_converter_transformation_duration_seconds{converter_id, tenant_id}` - Histogram
- `vrsky_converter_retry_attempts_total{converter_id, tenant_id}` - Counter

### Example Prometheus Queries

```promql
# Messages per second
rate(vrsky_converter_messages_received_total[5m])

# Success rate
rate(vrsky_converter_messages_succeeded_total[5m]) / 
rate(vrsky_converter_messages_received_total[5m]) * 100

# P95 latency
histogram_quantile(0.95, vrsky_converter_transformation_duration_seconds)

# Retry rate
rate(vrsky_converter_retry_attempts_total[5m]) /
rate(vrsky_converter_messages_received_total[5m]) * 100
```

## Development Workflow

### Building from Source

```bash
cd src
go build -o bin/converter ./cmd/converter
```

### Running Tests

```bash
cd src
go test ./pkg/converter -v
go test ./pkg/converter -race  # With race detector
go test -cover ./pkg/converter # With coverage
```

### Running Locally

```bash
# Start dependencies
docker-compose up -d

# Set environment variables
export NATS_URL=nats://localhost:4222
export CONFIG_SERVICE_URL=http://localhost:8080
export POSTGRES_URL=postgres://localhost:5432

# Run converter
go run ./cmd/converter
```

### Performance Profiling

```bash
# CPU profile
go test -cpuprofile=cpu.prof ./pkg/converter
go tool pprof cpu.prof

# Memory profile
go test -memprofile=mem.prof ./pkg/converter
go tool pprof mem.prof

# Benchmark specific function
go test -bench=BenchmarkTransform -benchmem ./pkg/converter
```

## Best Practices

### 1. Configuration
- Keep sensitive data (DB passwords) in environment variables or secrets
- Use ConfigService API for per-tenant configuration
- Enable configuration hot-reload for zero-downtime updates

### 2. Performance
- Use `lookup_postgres()` with connection pooling
- Enable function result caching for expensive operations
- Monitor transformation duration and adjust concurrency

### 3. Reliability
- Set appropriate MaxRetries and RetryBackoff
- Implement proper error handling strategies
- Monitor error rate and investigate spikes

### 4. Security
- Use HTTPS for HTTP lookups
- Validate all input data with schemas
- Use read-only filesystem in Kubernetes
- Run as non-root user (UID 1000)

### 5. Observability
- Export Prometheus metrics
- Use structured JSON logging
- Include context in error messages
- Set up alerting for error spikes

## Troubleshooting

### Common Issues

#### High Memory Usage
- Reduce `CACHE_TTL` if caching too much
- Lower `MAX_CONCURRENT_REQUESTS`
- Check for memory leaks in custom functions

#### Slow Transformations
- Check expression_evaluator complexity
- Review PostgreSQL query performance
- Enable function caching for lookups
- Profile with pprof

#### Message Loss
- Verify NATS cluster health
- Check error topic for failed messages
- Review error handling strategies
- Check logs for transformation errors

## References

- Configuration Reference: `CONVERTER_CONFIGURATION_REFERENCE.md`
- Functions Reference: `CONVERTER_FUNCTIONS_REFERENCE.md`
- Deployment Guide: `../infrastructure/kubernetes/converter/README.md`
