# VRSky Producer - Testing & Deployment Guide

## Quick Start

### 1. Build Producer Binary Locally

```bash
# Build the producer binary to ./bin/producer
make build

# Or clean build
make clean && make build
```

### 2. Build Docker Image

```bash
# Build Docker image: vrsky/producer:latest
make docker-build

# List built images
docker images | grep vrsky/producer
```

### 3. Start Local Environment

```bash
# Start NATS, httpbin, and producer services
docker-compose up -d

# View logs
docker-compose logs -f

# View producer logs only
docker-compose logs -f producer

# Stop services
docker-compose down
```

---

## Configuration

### Producer Configuration via Environment Variables

The Producer uses JSON environment variables for configuration:

| Variable | Type | Example | Description |
|----------|------|---------|-------------|
| `INPUT_TYPE` | string | `"nats"` | Input type: `nats`, `http`, `file` |
| `INPUT_CONFIG` | JSON | `{"url":"...","topic":"..."}` | Input configuration (type-specific) |
| `OUTPUT_TYPE` | string | `"http"` | Output type: `http`, `nats`, `file` |
| `OUTPUT_CONFIG` | JSON | `{"url":"...","method":"POST"}` | Output configuration (type-specific) |

### NATS Input Configuration

```json
{
  "url": "nats://localhost:4222",
  "topic": "test.>",
  "queue_group": "producer-workers"
}
```

**Parameters**:
- `url` (required): NATS server URL
- `topic` (required): Topic pattern to subscribe to (wildcards: `*` = single level, `>` = multiple levels)
- `queue_group` (optional): Queue group for load balancing

### HTTP Output Configuration

```json
{
  "url": "http://localhost:8080/post",
  "method": "POST",
  "retries": 1,
  "timeout_ms": 5000
}
```

**Parameters**:
- `url` (required): HTTP endpoint URL
- `method` (required): HTTP method (e.g., `POST`, `PUT`)
- `retries` (optional): Number of retries on failure (default: 1)
- `timeout_ms` (optional): Request timeout in milliseconds (default: 5000)

---

## Testing Scenarios

### Scenario 1: NATS → HTTP (Docker Compose)

**Setup**: All services running in docker-compose

```bash
# Start all services
docker-compose up -d

# Wait for services to be healthy
docker-compose ps

# Expected output:
# STATUS: healthy for all services
```

**Test**: Publish message to NATS and observe HTTP POST

```bash
# In a new terminal, publish a message to NATS
docker exec vrsky-nats nats pub test.1.orders "Order #12345: iPhone 15"

# View producer logs
docker-compose logs producer

# Expected: Message received from NATS and POSTed to httpbin
```

**Verification**: Check httpbin logs

```bash
# View httpbin logs to see the POST request
docker-compose logs httpbin

# Expected output includes:
# POST /post HTTP/1.1
# Body: Order #12345: iPhone 15
```

### Scenario 2: Local NATS → Local HTTP (Manual Testing)

**Setup**: NATS and httpbin in docker, producer running locally

```bash
# 1. Start NATS and httpbin only
docker-compose up -d nats httpbin

# 2. Wait for health checks
docker-compose ps

# 3. Build and run producer locally
make build
export INPUT_TYPE="nats"
export INPUT_CONFIG='{"url":"nats://localhost:4222","topic":"test.>"}'
export OUTPUT_TYPE="http"
export OUTPUT_CONFIG='{"url":"http://localhost:8080/post","method":"POST","retries":1}'
./bin/producer
```

**Test**: Publish and verify

```bash
# Publish message (in another terminal)
nats pub test.orders "Test message"

# View producer output (should see structured JSON logs)
# Expected:
# {"level":"info","msg":"Received message","topic":"test.orders","message_id":"..."}
# {"level":"info","msg":"HTTP POST successful","url":"http://localhost:8080/post","status":200}
```

### Scenario 3: Multiple Topics with Wildcards

**Test pattern matching**:

```bash
# Producer topic: "events.>"

# Publish to different subtopics
nats pub events.orders "order payload"
nats pub events.users "user payload"
nats pub events.notifications.email "email payload"

# All should be received and forwarded by producer
```

### Scenario 4: Error Handling - HTTP Failure with Retry

**Setup**: Create failing endpoint (returns 500)

```bash
# Use netcat to simulate a failing endpoint
nc -l localhost 9999 &

# Configure producer to POST to failing endpoint
export OUTPUT_CONFIG='{"url":"http://localhost:9999/fail","method":"POST","retries":1}'

# Publish message
nats pub test.error "test error handling"

# Producer should:
# 1. POST to endpoint (fails with timeout/connection error)
# 2. Retry once
# 3. Log error and continue
# 4. Continue listening for new messages
```

### Scenario 5: NATS Reconnection

**Test auto-reconnect behavior**:

```bash
# 1. Start all services
docker-compose up -d

# 2. Publish a message (should work)
nats pub test.reconnect "message 1"

# 3. Stop NATS
docker-compose stop nats

# 4. Observe producer logs
docker-compose logs producer

# Expected: Error logs about disconnection, auto-reconnect attempts

# 5. Restart NATS
docker-compose start nats

# 6. Publish new message
nats pub test.reconnect "message 2"

# Expected: Producer reconnects and receives message
```

---

## Makefile Commands Reference

```bash
# Show all available commands
make help

# Build binary to ./bin/producer
make build

# Build and run locally
make run

# Build Docker image
make docker-build

# Format code with gofmt
make fmt

# Run Go vet
make vet

# Run linter (if golangci-lint installed)
make lint

# Tidy Go modules
make mod-tidy

# Show build information
make info

# Clean build artifacts
make clean
```

---

## Docker Compose Commands

```bash
# Start all services
docker-compose up -d

# Start specific service
docker-compose up -d nats

# View logs (follow mode)
docker-compose logs -f

# View specific service logs
docker-compose logs -f producer

# Show service status
docker-compose ps

# Stop all services (keep volumes)
docker-compose stop

# Stop and remove services
docker-compose down

# Remove volumes too
docker-compose down -v

# Rebuild images
docker-compose build --no-cache

# Execute command in running container
docker-compose exec producer ./producer

# View environment variables in service
docker-compose config | grep -A 10 "producer:"
```

---

## Debugging

### View Structured Logs

Producer logs are output as JSON (one object per line):

```json
{"level":"info","msg":"Producer started","component":"producer"}
{"level":"info","msg":"Received message","topic":"test.1","message_id":"abc-123"}
{"level":"info","msg":"HTTP POST successful","url":"http://localhost:8080/post","status":200}
```

**Parse logs with jq**:

```bash
# View all error logs
docker-compose logs producer | jq 'select(.level=="error")'

# View specific field
docker-compose logs producer | jq '.msg'

# Count log levels
docker-compose logs producer | jq '.level' | sort | uniq -c
```

### Check Service Connectivity

```bash
# Check NATS connectivity from producer
docker-compose exec producer nats -s nats://nats:4222 status

# Check httpbin connectivity
docker-compose exec producer curl -v http://httpbin:80/status/200

# View network information
docker network inspect vrsky-network
```

### Check Environment Variables in Container

```bash
# View environment variables
docker-compose exec producer env | sort

# Verify INPUT/OUTPUT configuration
docker-compose exec producer env | grep INPUT
docker-compose exec producer env | grep OUTPUT
```

---

## Deployment Checklist

Before deploying Producer to production:

- [ ] Code review completed
- [ ] Unit tests pass: `make test`
- [ ] Linter passes: `golangci-lint run`
- [ ] Code formatted: `make fmt`
- [ ] Docker image builds successfully: `make docker-build`
- [ ] docker-compose local test passes
- [ ] Error handling verified (retries, fallback)
- [ ] NATS reconnection tested
- [ ] Logs verified (structured JSON format)
- [ ] Configuration validated (JSON syntax, required fields)
- [ ] Documentation updated
- [ ] Git tag created: `git tag v0.1.0`

---

## File Summary

| File | Purpose |
|------|---------|
| `Makefile` | Build automation, Docker commands |
| `cmd/producer/Dockerfile` | Multi-stage Docker image build |
| `docker-compose.yml` | Local development environment (NATS + httpbin + producer) |

---

## Quick Reference: Producer Test Flow

```
┌──────────────────┐
│   User publishes │
│  message to NATS │
└────────┬─────────┘
         │
         ↓
┌──────────────────┐
│ NATS receives &  │
│  buffers message │
└────────┬─────────┘
         │
         ↓
┌──────────────────┐
│ Producer reads   │
│ from NATS topic  │
└────────┬─────────┘
         │
         ↓
┌──────────────────┐
│ Wraps in Envelope│
│ (adds ID, meta)  │
└────────┬─────────┘
         │
         ↓
┌──────────────────┐
│ POSTs to HTTP    │
│ endpoint (httpbin)
└────────┬─────────┘
         │
         ↓
┌──────────────────┐
│  HTTP response   │
│   (200, 201)     │
└────────┬─────────┘
         │
         ↓
┌──────────────────┐
│ Producer logs:   │
│ "HTTP POST OK"   │
└──────────────────┘
```

---

## Example: End-to-End Test Script

```bash
#!/bin/bash
# test-producer.sh - Complete Producer test

set -e

echo "Starting Producer end-to-end test..."

# Start services
echo "1. Starting docker-compose..."
docker-compose up -d

# Wait for health checks
echo "2. Waiting for services to be healthy..."
sleep 15

# Verify services
echo "3. Verifying services..."
docker-compose ps

# Publish test message
echo "4. Publishing test message..."
docker exec vrsky-nats nats pub test.e2e.orders "Test Order #12345"

# Wait for processing
sleep 2

# Check logs
echo "5. Checking producer logs..."
docker-compose logs producer | tail -5

echo "✓ Producer test complete!"
```

Save as `test-producer.sh`, then run:
```bash
chmod +x test-producer.sh
./test-producer.sh
```
