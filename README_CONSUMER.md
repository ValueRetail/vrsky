# VRSky Consumer (Phase 1B) - Basic Webhook Receiver

## 🎯 Overview

The **HTTP Consumer** is a basic webhook receiver that:
- Listens for incoming HTTP POST requests on a configurable port
- Wraps payloads in VRSky Envelope format (with metadata)
- Publishes messages to NATS topics for downstream processing
- Complements Phase 1A Producer to create a bidirectional pipeline

This is **Phase 1B** of the VRSky platform foundation, focused on establishing the input side of the integration pipeline.

## 🏗️ Architecture

```
HTTP Client
    │
    ├─ POST /webhook (JSON payload)
    │
    ▼
┌─────────────────────────────────┐
│  HTTP Consumer (Port 8000)      │
│  ┌───────────────────────────┐  │
│  │ HTTP Input Server         │  │
│  │ - Accept POST requests    │  │
│  │ - Return 202 Accepted     │  │
│  │ - Parse JSON              │  │
│  │ - Extract metadata        │  │
│  └───────────────┬───────────┘  │
│                  │               │
│                  ▼               │
│  ┌───────────────────────────┐  │
│  │ Envelope Creation         │  │
│  │ - UUID: message ID        │  │
│  │ - Timestamp: created_at   │  │
│  │ - Payload: raw JSON       │  │
│  │ - Metadata: source IP,    │  │
│  │   headers, etc            │  │
│  └───────────────┬───────────┘  │
│                  │               │
│                  ▼               │
│  ┌───────────────────────────┐  │
│  │ NATS Output Publisher     │  │
│  │ - Connect to NATS         │  │
│  │ - Publish to subject      │  │
│  │ - Serialize as JSON       │  │
│  └───────────────┬───────────┘  │
└────────────────────────────────┘
                  │
                  ▼
            ┌──────────────┐
            │ NATS Broker  │
            │ (topic:      │
            │ "messages")  │
            └────────┬─────┘
                     │
                     ▼ (subscribed by Producer)
            ┌──────────────────┐
            │ VRSky Producer   │
            │ (downstream)     │
            └──────────────────┘
```

## 🚀 Quick Start

### Prerequisites
- Go 1.21+
- NATS server running locally (`nats://localhost:4222`)
- Docker (optional, for running with container)

### Local Development

```bash
# Navigate to project
cd /home/ludvik/vrsky

# Build consumer
make build-consumer

# Start NATS (in another terminal)
docker run -d -p 4222:4222 nats:latest

# Run consumer with default config
make run-consumer

# In another terminal, send webhook
curl -X POST http://localhost:8000/webhook \
  -H "Content-Type: application/json" \
  -d '{"order_id":"12345","status":"completed"}'

# Verify with NATS CLI
nats sub test.messages
```

## 📋 Configuration

### Environment Variables

Consumer uses environment variables for configuration:

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `INPUT_TYPE` | string | (required) | Input type: `"http"` |
| `INPUT_CONFIG` | JSON | (required) | `{"port":"8000"}` |
| `OUTPUT_TYPE` | string | (required) | Output type: `"nats"` |
| `OUTPUT_CONFIG` | JSON | (required) | `{"url":"nats://localhost:4222","subject":"test.messages"}` |

### Example Configurations

**Development (local NATS on port 4222):**
```bash
INPUT_TYPE=http \
INPUT_CONFIG='{"port":"8000"}' \
OUTPUT_TYPE=nats \
OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"test.messages"}' \
./bin/consumer
```

**Production (remote NATS):**
```bash
INPUT_TYPE=http \
INPUT_CONFIG='{"port":"8000"}' \
OUTPUT_TYPE=nats \
OUTPUT_CONFIG='{"url":"nats://nats-cluster:4222","subject":"orders.received"}' \
./bin/consumer
```

## 🧪 Testing

### Unit Tests
```bash
# Run all unit tests (no external dependencies)
make test

# Run only HTTP input tests
cd src && go test -v ./pkg/io -run TestHTTPInput
```

### Integration Tests
```bash
# Requires local NATS running
docker run -d -p 4222:4222 nats:latest

# Run integration tests
cd src && go test -v -tags=integration ./pkg/io
```

### End-to-End Test
```bash
# Full pipeline test: HTTP → Consumer → NATS → Producer → HTTP
make e2e-test

# Or manually:
cd /home/ludvik/vrsky
./scripts/e2e-test.sh
```

**E2E Test Flow:**
1. Starts NATS server
2. Starts mock HTTP server (to receive output)
3. Starts Consumer (HTTP input on :8000 → NATS output)
4. Starts Producer (NATS input → HTTP output to mock server)
5. Sends webhook to Consumer
6. Verifies message reaches mock HTTP server
7. Cleans up all services

## 🐳 Docker

### Build Docker Image
```bash
make docker-build-consumer
```

### Run with Docker
```bash
# Start NATS first
docker run -d --name nats -p 4222:4222 nats:latest

# Run consumer
docker run -d \
  --name consumer \
  --link nats \
  -p 8000:8000 \
  -e INPUT_TYPE=http \
  -e INPUT_CONFIG='{"port":"8000"}' \
  -e OUTPUT_TYPE=nats \
  -e OUTPUT_CONFIG='{"url":"nats://nats:4222","subject":"test.messages"}' \
  vrsky/consumer:latest

# Test
curl -X POST http://localhost:8000/webhook \
  -H "Content-Type: application/json" \
  -d '{"test":"data"}'
```

### Push to Registry
```bash
make docker-push-consumer
```

## 📊 Message Format

### HTTP Request → Envelope

**Input (HTTP POST):**
```json
POST /webhook
Content-Type: application/json

{
  "order_id": "12345",
  "status": "completed",
  "items": ["widget", "gadget"]
}
```

**Internal Envelope:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "tenant_id": "",
  "integration_id": "",
  "payload": "{\"order_id\":\"12345\",\"status\":\"completed\",\"items\":[\"widget\",\"gadget\"]}",
  "payload_ref": "",
  "payload_size": 72,
  "content_type": "application/json",
  "source": "http",
  "current_step": 0,
  "step_history": ["http-input:127.0.0.1"],
  "created_at": "2026-02-03T10:30:45.123456Z",
  "expires_at": "2026-02-03T10:45:45.123456Z",
  "retry_count": 0,
  "last_error": ""
}
```

**NATS Message:**
- **Subject:** `test.messages` (configurable)
- **Body:** Complete envelope as JSON

## 🔄 Message Flow Example

### Scenario: Order Webhook → HTTP Consumer → NATS → Producer → HTTP API

```
1. Client sends order webhook:
   curl -X POST http://consumer:8000/webhook \
     -d '{"order_id":"ORD-001","amount":99.99}'
   ↓ (returns 202 Accepted immediately)

2. Consumer wraps in Envelope:
   {
     "id": "uuid-123",
     "payload": "{...order data...}",
     "source": "http",
     "created_at": "2026-02-03T10:30:45Z"
   }
   ↓

3. Consumer publishes to NATS "orders.received":
   NATS [orders.received] ← envelope (JSON)
   ↓

4. Producer subscribes to "orders.received":
   Receives envelope from NATS
   ↓

5. Producer sends to downstream HTTP API:
   POST https://api.example.com/orders
   Body: {complete envelope with original order data}
   ↓

6. API Responds:
   201 Created
```

## 🛠️ Build & Deployment

### Build Commands
```bash
# Build consumer binary
make build-consumer

# Build Docker image
make docker-build-consumer

# Run consumer locally
make run-consumer
```

### Project Structure
```
vrsky/
├── src/
│   ├── cmd/consumer/
│   │   ├── basic/main.go      ← Entry point
│   │   └── Dockerfile         ← Docker build
│   ├── pkg/
│   │   ├── io/
│   │   │   ├── http_input.go  ← HTTP server
│   │   │   ├── nats_output.go ← NATS publisher
│   │   │   ├── http_input_test.go
│   │   │   ├── nats_output_test.go
│   │   │   └── e2e_integration_test.go
│   │   └── envelope/
│   │       └── envelope.go    ← Message format
│   └── Makefile
├── scripts/
│   └── e2e-test.sh            ← Full pipeline test
└── test/
    └── mock-http-server/
        └── main.go            ← Mock endpoint for testing
```

## 📝 Logging

Consumer uses structured JSON logging with `slog`:

```json
{"time":"2026-02-03T10:30:45Z","level":"INFO","msg":"HTTP input started","port":8000,"endpoint":"/webhook"}
{"time":"2026-02-03T10:30:47Z","level":"INFO","msg":"Received webhook","id":"uuid-123","source_ip":"127.0.0.1","size":72,"content_type":"application/json"}
{"time":"2026-02-03T10:30:47Z","level":"INFO","msg":"Connected to NATS for output","url":"nats://localhost:4222","subject":"test.messages"}
{"time":"2026-02-03T10:30:47Z","level":"INFO","msg":"Message published to NATS","subject":"test.messages","message_id":"uuid-123"}
```

## ⚠️ Error Handling

### Fire-and-Forget Philosophy
- Consumer returns **202 Accepted** to HTTP client **immediately**
- Processing happens asynchronously in background
- If NATS publish fails, message is logged but doesn't block webhook response

### Connection Resilience
- HTTP server graceful shutdown (30-second timeout)
- NATS auto-reconnect on network failure
- Connection timeouts: 30 seconds (configurable)

## 🔗 Related Issues & Components

- **Phase 1A (Producer):** `#23` - Receives messages from NATS
- **Phase 1C (Converter):** Next phase - Transform messages
- **Phase 1D (Filter):** Next phase - Conditional routing
- **Parent Issue:** `#1` - Build Core Platform Foundation

## 📖 Comparison: Phase 1B vs Phase 2

| Aspect | Phase 1B (Basic) | Phase 2 (Full) |
|--------|------------------|----------------|
| HTTP Input | ✓ Basic webhook receiver | ✓ Advanced with auth |
| NATS Output | ✓ Simple publisher | ✓ With JetStream support |
| Error Handling | Basic fire-and-forget | Advanced with retries & DLQ |
| State Tracking | None | Full KV tracking |
| Consumer Interface | Embedded in main | Abstracted interface |
| Testing | Unit + E2E | Unit + Integration + E2E |
| **Use Case** | **Rapid MVP** | **Production-ready** |

## 🚦 Health Checks

### Kubernetes Liveness Probe
```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 8000
  initialDelaySeconds: 10
  periodSeconds: 30
```

### Docker Health Check
```dockerfile
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD test -f /app/consumer
```

## 🐛 Troubleshooting

### Consumer won't start
```
ERROR: Failed to start input: HTTP Input already running
```
**Cause:** Port 8000 already in use  
**Solution:** Change port in `INPUT_CONFIG` or kill existing process

### No messages in NATS
```
ERROR: Failed to publish to NATS subject test.messages
```
**Cause:** NATS server unreachable  
**Solution:** Verify NATS is running (`docker ps | grep nats`)

### Webhook returns 400 instead of 202
```
ERROR: Invalid JSON in webhook
```
**Cause:** Invalid JSON sent to POST /webhook  
**Solution:** Verify JSON is valid: `curl -X POST ... -d '{valid JSON}'`

## 📚 References

- NATS Documentation: https://docs.nats.io/
- Go Documentation: https://golang.org/doc/
- VRSky Architecture: `../docs/NATS_ARCHITECTURE.md`
- Phase 1A Producer: `../README.md`

## 🎓 Learning Resources

**New to VRSky?** Start here:
1. Read `../README.md` (project overview)
2. Study `../docs/PROJECT_INCEPTION.md` (architecture)
3. Review Phase 1A Producer code (similar pattern)
4. Try the quick start above
5. Run `make e2e-test` to see full pipeline

---

**Last Updated:** February 3, 2026  
**Version:** 1.0 (Phase 1B)  
**Status:** ✅ Production-Ready
