# VRSky Converter Configuration Reference

## Overview

The Converter component uses a hierarchical configuration system that supports multiple sources with automatic hot-reload. This guide covers all available configuration options.

## Configuration Sources (Priority Order)

1. **ConfigService API** (Highest Priority) - Per-tenant configuration, hot-reload every 60s
2. **Environment Variables** - Process environment
3. **Local Config Files** - YAML/JSON files in /etc/vrsky/
4. **Defaults** - Built-in defaults (Lowest Priority)

### Example Configuration Resolution

```
ConfigService has max_retries = 5
Environment has MAX_RETRIES = 3
Defaults have max_retries = 3

Result: max_retries = 5 (ConfigService takes priority)
```

## Configuration Sections

### Server Configuration

#### CONVERTER_PORT
- **Type**: Integer
- **Default**: 8080
- **Description**: HTTP server port for health checks and metrics
- **Example**: `export CONVERTER_PORT=8080`

#### GRACEFUL_SHUTDOWN_TIMEOUT
- **Type**: Integer (seconds)
- **Default**: 15
- **Description**: Time to wait for in-flight requests before force shutdown
- **Example**: `export GRACEFUL_SHUTDOWN_TIMEOUT=30`

#### HEALTH_CHECK_INTERVAL
- **Type**: Integer (seconds)
- **Default**: 30
- **Description**: Interval for health check probes
- **Example**: `export HEALTH_CHECK_INTERVAL=60`

---

### Logging Configuration

#### LOG_LEVEL
- **Type**: String (debug/info/warn/error)
- **Default**: info
- **Description**: Logging level for the converter
- **Values**:
  - `debug` - Verbose logging (development only)
  - `info` - Information level (default production)
  - `warn` - Warning and error only
  - `error` - Error only
- **Example**: `export LOG_LEVEL=debug`

#### LOG_FORMAT
- **Type**: String (json/text)
- **Default**: json
- **Description**: Log output format
- **Values**:
  - `json` - Structured JSON logging (recommended for production)
  - `text` - Human-readable text format (development only)
- **Example**: `export LOG_FORMAT=json`

---

### Performance Configuration

#### MAX_CONCURRENT_REQUESTS
- **Type**: Integer
- **Default**: 1000
- **Range**: 1-10000
- **Description**: Maximum concurrent message transformations
- **Example**: `export MAX_CONCURRENT_REQUESTS=2000`
- **Notes**:
  - Higher values increase throughput but require more memory
  - Monitor memory usage when increasing
  - Should match Kubernetes resource limits

#### REQUEST_TIMEOUT
- **Type**: Integer (seconds)
- **Default**: 30
- **Range**: 1-300
- **Description**: Timeout for individual transformation requests
- **Example**: `export REQUEST_TIMEOUT=60`

#### CACHE_TTL
- **Type**: Integer (seconds)
- **Default**: 300
- **Range**: 0-3600
- **Description**: Time-to-live for cached function results
- **Example**: `export CACHE_TTL=600`
- **Notes**:
  - 0 = Disable caching
  - Affects lookup performance
  - Per-function TTL can override this

---

### Validation Configuration

#### VALIDATION_MODE
- **Type**: String (strict/lenient)
- **Default**: strict
- **Description**: JSON Schema validation mode
- **Values**:
  - `strict` - All validation errors are failures
  - `lenient` - Attempts type coercion before failing
- **Example**: `export VALIDATION_MODE=lenient`

#### VALIDATION_ERROR_STRATEGY
- **Type**: String (skip/coerce/fail)
- **Default**: fail
- **Description**: How to handle validation errors
- **Values**:
  - `skip` - Ignore validation errors, proceed
  - `coerce` - Attempt to fix data (type conversion, etc.)
  - `fail` - Reject messages that fail validation
- **Example**: `export VALIDATION_ERROR_STRATEGY=coerce`

#### MISSING_FIELDS_STRATEGY
- **Type**: String (skip/coerce/fail)
- **Default**: fail
- **Description**: How to handle missing required fields
- **Example**: `export MISSING_FIELDS_STRATEGY=skip`

#### TYPE_MISMATCH_STRATEGY
- **Type**: String (skip/coerce/fail)
- **Default**: fail
- **Description**: How to handle type conversion failures
- **Example**: `export TYPE_MISMATCH_STRATEGY=coerce`

---

### Database Configuration

#### POSTGRES_URL
- **Type**: String
- **Default**: empty (required if using PostgreSQL lookups)
- **Description**: PostgreSQL connection string
- **Format**: `postgres://user:password@host:port/database?options`
- **Example**: `export POSTGRES_URL=postgres://converter:secret@db.local:5432/vrsky`
- **Supported Options**:
  - `sslmode=disable` - No SSL
  - `sslmode=require` - Require SSL
  - `sslmode=verify-full` - Verify certificate

#### POSTGRES_MAX_CONNECTIONS
- **Type**: Integer
- **Default**: 25
- **Range**: 1-100
- **Description**: Maximum database connections in pool
- **Example**: `export POSTGRES_MAX_CONNECTIONS=50`

#### POSTGRES_CONNECTION_TIMEOUT
- **Type**: Integer (seconds)
- **Default**: 5
- **Range**: 1-30
- **Description**: Timeout for establishing database connection
- **Example**: `export POSTGRES_CONNECTION_TIMEOUT=10`

#### POSTGRES_IDLE_TIMEOUT
- **Type**: Integer (seconds)
- **Default**: 300
- **Description**: Time before idle connections are closed
- **Example**: `export POSTGRES_IDLE_TIMEOUT=600`

---

### Storage Configuration

#### MINIO_ENDPOINT
- **Type**: String
- **Default**: empty (required for large payload handling)
- **Description**: MinIO server endpoint
- **Format**: `host:port`
- **Example**: `export MINIO_ENDPOINT=minio.local:9000`

#### MINIO_REGION
- **Type**: String
- **Default**: us-east-1
- **Description**: MinIO region name
- **Example**: `export MINIO_REGION=eu-west-1`

#### MINIO_BUCKET
- **Type**: String
- **Default**: vrsky-converter
- **Description**: MinIO bucket for temporary payload storage
- **Example**: `export MINIO_BUCKET=converter-payloads`

#### MINIO_USE_SSL
- **Type**: Boolean
- **Default**: false
- **Description**: Use SSL for MinIO connection
- **Example**: `export MINIO_USE_SSL=true`

#### MINIO_PAYLOAD_THRESHOLD
- **Type**: Integer (bytes)
- **Default**: 262144 (256KB)
- **Description**: Size threshold for storing payloads in MinIO
- **Example**: `export MINIO_PAYLOAD_THRESHOLD=1048576` (1MB)

---

### NATS Configuration

#### NATS_URL
- **Type**: String
- **Default**: nats://localhost:4222
- **Description**: NATS server URL
- **Format**: `nats://host:port` or `nats://host1:port1,host2:port2` for cluster
- **Example**: `export NATS_URL=nats://nats-cluster:4222`

#### NATS_REQUEST_TIMEOUT
- **Type**: Integer (seconds)
- **Default**: 30
- **Range**: 1-300
- **Description**: Timeout for NATS request-reply operations
- **Example**: `export NATS_REQUEST_TIMEOUT=60`

#### NATS_TOPICS_PREFIX
- **Type**: String
- **Default**: vrsky
- **Description**: Prefix for NATS topics
- **Example**: `export NATS_TOPICS_PREFIX=myapp`

#### NATS_CONSUMER_GROUP
- **Type**: String
- **Default**: converter
- **Description**: Consumer group name for NATS JetStream
- **Example**: `export NATS_CONSUMER_GROUP=conv-group-1`

---

### Metrics Configuration

#### PROMETHEUS_PORT
- **Type**: Integer
- **Default**: 8080
- **Description**: Port for Prometheus metrics endpoint
- **Example**: `export PROMETHEUS_PORT=9090`

#### METRICS_ENABLED
- **Type**: Boolean
- **Default**: true
- **Description**: Enable/disable Prometheus metrics
- **Example**: `export METRICS_ENABLED=false`

#### METRICS_PATH
- **Type**: String
- **Default**: /metrics
- **Description**: HTTP path for metrics endpoint
- **Example**: `export METRICS_PATH=/prometheus-metrics`

---

### Retry Configuration

#### MAX_RETRIES
- **Type**: Integer
- **Default**: 3
- **Range**: 1-10
- **Description**: Maximum retry attempts for failed transformations
- **Example**: `export MAX_RETRIES=5`

#### RETRY_BACKOFF
- **Type**: String
- **Default**: exponential
- **Description**: Retry backoff strategy
- **Values**:
  - `exponential` - Exponential backoff (1s, 10s, 100s, ...)
  - `linear` - Linear backoff (1s, 2s, 3s, ...)
  - `fixed` - Fixed interval
- **Example**: `export RETRY_BACKOFF=exponential`

#### RETRY_BACKOFF_BASE
- **Type**: Integer (seconds)
- **Default**: 1
- **Description**: Base interval for retry backoff
- **Example**: `export RETRY_BACKOFF_BASE=2`

---

### WASM Configuration

#### WASM_MODULE_DIR
- **Type**: String (path)
- **Default**: /app/wasm-modules
- **Description**: Directory containing WASM plugin modules
- **Example**: `export WASM_MODULE_DIR=/etc/vrsky/wasm-modules`

#### WASM_MEMORY_PAGES
- **Type**: Integer
- **Default**: 256
- **Range**: 1-65536
- **Description**: Memory pages allocated per WASM module (64KB per page)
- **Example**: `export WASM_MEMORY_PAGES=512`
- **Notes**: 256 pages = 16MB

#### WASM_SANDBOX_ENABLED
- **Type**: Boolean
- **Default**: true
- **Description**: Enable sandboxed execution for WASM modules
- **Example**: `export WASM_SANDBOX_ENABLED=false`

#### WASM_TIMEOUT
- **Type**: Integer (seconds)
- **Default**: 10
- **Description**: Execution timeout for WASM function calls
- **Example**: `export WASM_TIMEOUT=30`

---

### Advanced Configuration

#### CONVERTER_ID
- **Type**: String
- **Default**: Auto-generated from hostname
- **Description**: Unique identifier for this converter instance
- **Example**: `export CONVERTER_ID=converter-001`

#### TENANT_ID
- **Type**: String
- **Default**: empty (required)
- **Description**: Tenant that this converter instance serves
- **Example**: `export TENANT_ID=acme-corp`

#### CONFIG_REFRESH_INTERVAL
- **Type**: Integer (seconds)
- **Default**: 60
- **Description**: Interval to refresh configuration from ConfigService
- **Example**: `export CONFIG_REFRESH_INTERVAL=30`

#### CONFIG_CACHE_FALLBACK_ENABLED
- **Type**: Boolean
- **Default**: true
- **Description**: Use cached config if ConfigService is unavailable
- **Example**: `export CONFIG_CACHE_FALLBACK_ENABLED=false`

#### INPUT_TOPIC
- **Type**: String
- **Default**: empty (from config)
- **Description**: NATS topic to subscribe to (per-tenant)
- **Example**: `export INPUT_TOPIC=acme-corp.webhook.received`

#### OUTPUT_TOPIC
- **Type**: String
- **Default**: Auto-generated as `{INPUT_TOPIC}.converted`
- **Description**: NATS topic to publish successful transformations
- **Example**: `export OUTPUT_TOPIC=acme-corp.webhook.converted`

#### ERROR_TOPIC
- **Type**: String
- **Default**: empty (from config)
- **Description**: NATS topic to publish failed transformations
- **Example**: `export ERROR_TOPIC=acme-corp.webhook.errors`

---

## ConfigService API Format

Configuration can be loaded from ConfigService API in the following format:

```json
{
  "converter_id": "converter-001",
  "tenant_id": "tenant-123",
  "input_topic": "webhook.received",
  "output_topic": "webhook.converted",
  "error_topic": "webhook.errors",
  "transformations": [
    {
      "source": "customer.email",
      "target": "email",
      "type": "string"
    },
    {
      "source": "order.total",
      "target": "amount",
      "type": "float",
      "condition": "order.total > 100"
    },
    {
      "function": "lookup_customer(customer.id)",
      "target": "customer_data",
      "type": "object"
    }
  ],
  "input_schema": {
    "required_fields": ["order.id", "customer.email"]
  },
  "output_schema": {
    "required_fields": ["order_id", "customer_email"]
  },
  "error_handling": {
    "missing_fields": "fail",
    "type_mismatch": "coerce",
    "validation_error": "fail"
  },
  "max_retries": 3,
  "retry_backoff": "exponential"
}
```

---

## Example Configuration Scenarios

### Development Environment

```bash
export LOG_LEVEL=debug
export LOG_FORMAT=text
export MAX_CONCURRENT_REQUESTS=100
export VALIDATION_MODE=strict
export CACHE_TTL=60
export NATS_URL=nats://localhost:4222
```

### Production - High Throughput

```bash
export LOG_LEVEL=info
export LOG_FORMAT=json
export MAX_CONCURRENT_REQUESTS=2000
export VALIDATION_MODE=lenient
export CACHE_TTL=600
export MAX_RETRIES=5
export POSTGRES_MAX_CONNECTIONS=50
```

### Production - Low Latency

```bash
export LOG_LEVEL=info
export LOG_FORMAT=json
export MAX_CONCURRENT_REQUESTS=500
export VALIDATION_MODE=strict
export CACHE_TTL=300
export REQUEST_TIMEOUT=5
export WASM_MEMORY_PAGES=512
```

### Multi-Tenant Setup

```bash
export CONVERTER_ID=converter-tenant1
export TENANT_ID=tenant-123
export INPUT_TOPIC=tenant-123.webhook.received
export OUTPUT_TOPIC=tenant-123.webhook.converted
export ERROR_TOPIC=tenant-123.webhook.errors
export CONFIG_SERVICE_URL=http://config-service:8080
export CONFIG_REFRESH_INTERVAL=60
```

---

## Configuration Validation

The converter validates configuration on startup and logs warnings for:

- Invalid log levels
- Out-of-range values (e.g., MAX_CONCURRENT_REQUESTS > 10000)
- Missing required fields (TENANT_ID, INPUT_TOPIC)
- Invalid error handling strategies
- Unreachable ConfigService URL

---

## Hot-Reload

Configuration from ConfigService is automatically refreshed every 60 seconds (configurable via CONFIG_REFRESH_INTERVAL):

1. New configuration fetched from ConfigService API
2. Validated for correctness
3. Applied to converter without restart
4. Metrics recorded for successful reload
5. Previous configuration kept as fallback on error

In-flight transformations continue with old configuration and complete normally.

---

## Debugging Configuration

### Check Active Configuration

```bash
# Print all environment variables
env | grep -i converter

# View loaded configuration
curl http://localhost:8080/config
```

### Enable Debug Logging

```bash
export LOG_LEVEL=debug

# Check configuration loading in logs
kubectl logs deployment/vrsky-converter -f | grep -i config
```

### Configuration Priority Test

```bash
# Set in multiple places, observe which takes priority

# ConfigService API
curl -X POST http://config-service:8080/v1/config \
  -d '{"max_retries": 5}'

# Environment variable
export MAX_RETRIES=3

# ConfigService takes priority → max_retries = 5
```

---

## Environment Variable Naming Convention

All configuration options can be specified as environment variables using SCREAMING_SNAKE_CASE:

- `converter_id` → `CONVERTER_ID`
- `max_retries` → `MAX_RETRIES`
- `log_level` → `LOG_LEVEL`
- `postgres_max_connections` → `POSTGRES_MAX_CONNECTIONS`

---

## References

- Implementation Guide: `CONVERTER_IMPLEMENTATION_GUIDE.md`
- Functions Reference: `CONVERTER_FUNCTIONS_REFERENCE.md`
- Deployment Guide: `../infrastructure/kubernetes/converter/README.md`
