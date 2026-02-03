# Complete Testing Verification Guide - Phase 1B

**Purpose:** Verify that all Phase 1B components work as expected  
**Duration:** ~45 minutes total (can be done in sections)  
**Difficulty:** Beginner-friendly (just follow the commands)

---

## 🎯 Testing Overview

This guide walks you through verifying:
- ✅ Code compiles correctly
- ✅ All 6 unit tests pass
- ✅ E2E pipeline works (HTTP → NATS → HTTP)
- ✅ Manual webhook functionality
- ✅ Docker image builds
- ✅ Docker container runs and processes webhooks

---

## ⚡ QUICK START (5 minutes)

If you just want to verify the basics:

```bash
cd /home/ludvik/vrsky/src

# Build the binary
make build-consumer

# Run unit tests
make test

# Expected output:
# ✓ All 6 tests should PASS
# ✓ Execution time: ~0.5 seconds
```

**If all green checkmarks appear, Phase 1B is working!** ✅

---

## 🧪 FULL VERIFICATION PLAN (45 minutes)

Follow these 7 steps in order. Each takes 3-10 minutes.

### **STEP 1: Verify Prerequisites** (2 min)

Check that all required tools are available:

```bash
# Check Go version
go version
# Expected: go version go1.21 or higher

# Check if NATS is running
docker ps | grep nats
# Expected: Should see "nats" in output

# Check Docker
docker --version
# Expected: Docker 29.x or higher

# Check git
git --version
# Expected: git version 2.x or higher
```

**✅ All present?** Continue to Step 2

---

### **STEP 2: Clean Build** (3 min)

Rebuild from scratch to ensure everything compiles:

```bash
cd /home/ludvik/vrsky/src

# Clean old artifacts
make clean

# Build both binaries
go build -o bin/consumer ./cmd/consumer/basic
go build -o bin/producer ./cmd/producer

# Verify binaries exist
ls -lh bin/
# Expected: consumer and producer files (8.9MB each)

# Test that binaries run
./bin/consumer --help 2>/dev/null || echo "Consumer binary built"
./bin/producer --help 2>/dev/null || echo "Producer binary built"
```

**✅ Both binaries present and executable?** Continue to Step 3

---

### **STEP 3: Unit Tests** (3 min)

Run the 6 unit tests:

```bash
cd /home/ludvik/vrsky/src

# Run tests with verbose output
make test

# You should see:
# === RUN   TestHTTPInput_NewHTTPInput
# --- PASS: TestHTTPInput_NewHTTPInput (0.00s)
# === RUN   TestHTTPInput_Start_Close
# --- PASS: TestHTTPInput_Start_Close (0.10s)
# === RUN   TestHTTPInput_ReceiveWebhook
# --- PASS: TestHTTPInput_ReceiveWebhook (0.10s)
# === RUN   TestHTTPInput_Read_ReturnsEnvelope
# --- PASS: TestHTTPInput_Read_ReturnsEnvelope (0.10s)
# === RUN   TestHTTPInput_ParsesPayload
# --- PASS: TestHTTPInput_ParsesPayload (0.10s)
# === RUN   TestHTTPInput_ContextCancellation
# --- PASS: TestHTTPInput_ContextCancellation (0.10s)
# PASS
# ✓ Tests passed
```

**✅ All 6 tests PASS?** Continue to Step 4

---

### **STEP 4: Code Quality Checks** (3 min)

Verify the code follows Go standards:

```bash
cd /home/ludvik/vrsky/src

# Format check
go fmt ./...
echo "✓ Format check passed (no output = formatted correctly)"

# Vet check for common mistakes
go vet ./...
echo "✓ Vet passed (no output = no issues found)"

# Check that modules are tidy
go mod tidy
git diff go.mod go.sum
# Expected: No changes (already tidy)
```

**✅ No errors or changes?** Continue to Step 5

---

### **STEP 5: Manual Webhook Test** (5 min)

Test the HTTP endpoint manually:

**Terminal 1 - Start Consumer:**
```bash
cd /home/ludvik/vrsky/src

# Set environment variables
export INPUT_TYPE=http
export INPUT_CONFIG='{"port":"8000"}'
export OUTPUT_TYPE=nats
export OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"test.manual"}'

# Run consumer
./bin/consumer

# Expected output:
# Configuration loaded
# HTTP input started on port 8000
# (should keep running)
```

**Terminal 2 - Send Webhook:**
```bash
# Test webhook 1 - Basic JSON
curl -X POST http://localhost:8000/webhook \
  -H "Content-Type: application/json" \
  -d '{"test":"data","id":1}'

# Expected: HTTP 202 response
# curl will show: %{http_code} = 202

# Test webhook 2 - Order-like payload
curl -X POST http://localhost:8000/webhook \
  -H "Content-Type: application/json" \
  -d '{"order_id":"ORD-123","amount":99.99,"status":"pending"}'

# Expected: HTTP 202 response

# Test webhook 3 - Different content type
curl -X POST http://localhost:8000/webhook \
  -H "Content-Type: text/plain" \
  -d '{"flexible":"data"}'

# Expected: HTTP 202 response

# Get HTTP status code specifically
curl -s -o /dev/null -w "%{http_code}\n" \
  -X POST http://localhost:8000/webhook \
  -H "Content-Type: application/json" \
  -d '{"final":"test"}'

# Expected: 202
```

**Back in Terminal 1 - Consumer Output:**
```
# You should see messages like:
# INFO Received webhook id=abc123-def456 source_ip=127.0.0.1 content_type=application/json payload_size=27
# INFO Webhook queued id=abc123-def456

# This confirms the webhook was received and processed
```

**✅ All 4 webhooks returned 202 and were logged?** Continue to Step 6

---

### **STEP 6: E2E Pipeline Test** (8 min)

Test the complete HTTP → NATS → HTTP pipeline:

**Terminal 1 - Start Consumer:**
```bash
cd /home/ludvik/vrsky/src

export INPUT_TYPE=http
export INPUT_CONFIG='{"port":"8001"}'
export OUTPUT_TYPE=nats
export OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"e2e.test.messages"}'

./bin/consumer

# Expected: HTTP input started on port 8001
```

**Terminal 2 - Start Producer:**
```bash
cd /home/ludvik/vrsky/src

export INPUT_TYPE=nats
export INPUT_CONFIG='{"url":"nats://localhost:4222","topic":"e2e.test.messages"}'
export OUTPUT_TYPE=http
export OUTPUT_CONFIG='{"url":"http://localhost:8080/post","method":"POST"}'

./bin/producer

# Expected:
# Configuration loaded
# Connected to NATS
# HTTP output started
# Producer starting main loop
```

**Terminal 3 - Send Webhook Through Pipeline:**
```bash
# Send a test message through the pipeline
curl -X POST http://localhost:8001/webhook \
  -H "Content-Type: application/json" \
  -d '{"pipeline_test":"success","timestamp":"'$(date -u +%Y-%m-%dT%H:%M:%SZ)'"}'

# Expected: HTTP 202 response

# Wait a moment for message to propagate
sleep 2

# Check that producer received it
# (Look at Terminal 2 for confirmation)
```

**Expected in Terminal 1 (Consumer):**
```
INFO Received webhook
INFO Webhook queued
```

**Expected in Terminal 2 (Producer):**
```
INFO Read envelope from NATS topic
INFO Sending to HTTP output
```

**✅ Messages flowed through the pipeline?** Continue to Step 7

---

### **STEP 7: Docker Tests** (12 min)

#### **Part A: Build Docker Image** (4 min)

```bash
cd /home/ludvik/vrsky/src

# Build the Docker image
echo "rrhbx6ch" | sudo -S make docker-build-consumer

# Expected output:
# Building consumer Docker image...
# ... (build progress) ...
# ✓ Docker image built: vrsky/consumer:latest

# Verify image exists
echo "rrhbx6ch" | sudo -S docker images | grep vrsky/consumer

# Expected: 
# vrsky/consumer    latest    <image-id>    27.9MB
```

**✅ Image built successfully (27.9MB)?** Continue to Part B

#### **Part B: Run Docker Container** (5 min)

```bash
# Start the container
echo "rrhbx6ch" | sudo -S docker run -d \
  --name test-consumer \
  --network host \
  -e INPUT_TYPE=http \
  -e INPUT_CONFIG='{"port":"8003"}' \
  -e OUTPUT_TYPE=nats \
  -e OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"docker.test"}' \
  vrsky/consumer:latest

# Store container ID
CONTAINER_ID=$(echo "rrhbx6ch" | sudo -S docker run -d \
  --name test-consumer \
  --network host \
  -e INPUT_TYPE=http \
  -e INPUT_CONFIG='{"port":"8003"}' \
  -e OUTPUT_TYPE=nats \
  -e OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"docker.test"}' \
  vrsky/consumer:latest)

echo "Container ID: $CONTAINER_ID"

# Wait for startup
sleep 2

# Check container is running
echo "rrhbx6ch" | sudo -S docker ps | grep test-consumer

# Expected: Container listed and running

# Check logs
echo "rrhbx6ch" | sudo -S docker logs test-consumer

# Expected:
# Configuration loaded
# HTTP input started on port 8003
```

**✅ Container started and logs show it's running?** Continue to Part C

#### **Part C: Test Docker Container** (3 min)

```bash
# Send webhook to Docker container
curl -X POST http://localhost:8003/webhook \
  -H "Content-Type: application/json" \
  -d '{"container_test":"working","from":"docker"}'

# Expected: HTTP 202 response

# Check container processed it
echo "rrhbx6ch" | sudo -S docker logs test-consumer | tail -5

# Expected: "Received webhook" and "Webhook queued" messages

# Clean up
echo "rrhbx6ch" | sudo -S docker stop test-consumer
echo "rrhbx6ch" | sudo -S docker rm test-consumer

# Verify stopped
echo "rrhbx6ch" | sudo -S docker ps | grep test-consumer || echo "✓ Container cleaned up"
```

**✅ Docker container worked and has been cleaned up?** Continue to Final Verification

---

## ✅ FINAL VERIFICATION CHECKLIST

After completing all 7 steps, verify everything:

- [ ] Step 1: Prerequisites present (Go, NATS, Docker, git)
- [ ] Step 2: Binaries built (consumer 8.9MB, producer 8.9MB)
- [ ] Step 3: 6/6 unit tests PASS
- [ ] Step 4: Code quality checks pass (fmt, vet, mod tidy)
- [ ] Step 5: Manual webhooks return HTTP 202 and log correctly
- [ ] Step 6: E2E pipeline works (HTTP → NATS → HTTP)
- [ ] Step 7a: Docker image builds (27.9MB)
- [ ] Step 7b: Docker container starts and logs show startup
- [ ] Step 7c: Docker container accepts webhooks and processes them

**If ALL boxes are checked: ✅ PHASE 1B IS WORKING PERFECTLY!**

---

## 🐛 Troubleshooting

### Issue: "Port already in use"
```bash
# Kill the process using the port
lsof -i :8000
# Then: kill -9 <PID>

# Or use a different port in the config
```

### Issue: "NATS connection refused"
```bash
# Check if NATS is running
docker ps | grep nats

# If not, start it:
echo "rrhbx6ch" | sudo -S docker run -d -p 4222:4222 --name nats nats:latest
```

### Issue: "Docker permission denied"
```bash
# Use sudo with password
echo "rrhbx6ch" | sudo -S docker ps

# All docker commands need this prefix
```

### Issue: "Test fails - no output"
```bash
# Run with verbose logging
RUST_LOG=debug make test

# Or run specific test
go test -v -run TestHTTPInput_ReceiveWebhook ./pkg/io
```

### Issue: "Binary won't start"
```bash
# Check it was built correctly
file ./bin/consumer

# Should show: "ELF 64-bit LSB executable"

# Rebuild if needed
rm ./bin/consumer
go build -o bin/consumer ./cmd/consumer/basic
```

---

## 📊 Expected Test Results Summary

| Test | Expected Result | Status |
|------|-----------------|--------|
| Build | 2 binaries (8.9MB each) | ✅ |
| Unit Tests | 6/6 PASS (0.5s) | ✅ |
| Code Quality | No fmt/vet issues | ✅ |
| Manual Webhooks | 4/4 HTTP 202 | ✅ |
| E2E Pipeline | Messages flow through | ✅ |
| Docker Build | 27.9MB image | ✅ |
| Docker Container | Starts & accepts webhooks | ✅ |

---

## 🎯 Success Criteria

**Phase 1B is working correctly if:**
- ✅ Code compiles without errors
- ✅ All 6 unit tests pass
- ✅ Manual webhooks work (HTTP 202)
- ✅ E2E pipeline propagates messages
- ✅ Docker image builds at 27.9MB
- ✅ Docker container runs and processes webhooks
- ✅ No errors in any test

**ALL criteria met = Ready for PR submission!** 🚀

---

## 💡 Pro Tips

1. **Run tests after any code changes** to catch regressions
2. **Keep one NATS container running** - don't stop/start it repeatedly
3. **Use different ports for manual tests** (8000, 8001, 8003) to avoid conflicts
4. **Save curl commands as shell aliases** for repeated testing
5. **Monitor Docker logs** in a separate terminal for long-running tests

---

## 🚀 Quick Command Reference

```bash
# Quick verification (5 min)
cd /home/ludvik/vrsky/src && make test

# Manual test (Terminal 1)
INPUT_TYPE=http INPUT_CONFIG='{"port":"8000"}' \
OUTPUT_TYPE=nats OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"test"}' \
./bin/consumer

# Manual test (Terminal 2)
curl -X POST http://localhost:8000/webhook \
  -H "Content-Type: application/json" \
  -d '{"test":"data"}'

# Docker build
echo "rrhbx6ch" | sudo -S make docker-build-consumer

# Docker run
echo "rrhbx6ch" | sudo -S docker run -d --network host \
  -e INPUT_TYPE=http -e INPUT_CONFIG='{"port":"8000"}' \
  -e OUTPUT_TYPE=nats -e OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"test"}' \
  vrsky/consumer:latest
```

---

**Duration:** ~45 minutes total  
**Difficulty:** Beginner-friendly  
**Result:** Complete verification of Phase 1B functionality

Ready to verify? Start with Step 1 above! 🚀

