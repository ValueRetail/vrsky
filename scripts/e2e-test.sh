#!/bin/bash

# E2E Test: HTTP → Consumer → NATS → Producer → HTTP
# This script validates the full pipeline without manual intervention

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SRC_DIR="$PROJECT_ROOT/src"
BIN_DIR="$SRC_DIR/bin"

# Colors
BLUE='\033[0;34m'
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Helper functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[✓]${NC} $1"
}

log_error() {
    echo -e "${RED}[✗]${NC} $1"
}

cleanup() {
    log_info "Cleaning up..."
    
    # Kill all background processes
    if [ ! -z "$NATS_PID" ]; then
        docker stop vrsky-e2e-nats 2>/dev/null || true
        docker rm vrsky-e2e-nats 2>/dev/null || true
    fi
    
    if [ ! -z "$MOCK_HTTP_PID" ]; then
        kill $MOCK_HTTP_PID 2>/dev/null || true
    fi
    
    if [ ! -z "$CONSUMER_PID" ]; then
        kill $CONSUMER_PID 2>/dev/null || true
    fi
    
    if [ ! -z "$PRODUCER_PID" ]; then
        kill $PRODUCER_PID 2>/dev/null || true
    fi
    
    # Clean up message file
    rm -f /tmp/received-messages.txt
    
    log_success "Cleanup complete"
}

trap cleanup EXIT

# Main test flow

log_info "Starting E2E Test: HTTP → Consumer → NATS → Producer → HTTP"
log_info "=============================================================="

# 1. Verify binaries exist
log_info "Checking binaries..."
if [ ! -f "$BIN_DIR/consumer" ]; then
    log_error "Consumer binary not found at $BIN_DIR/consumer"
    exit 1
fi
log_success "Consumer binary found"

if [ ! -f "$BIN_DIR/producer" ]; then
    log_error "Producer binary not found at $BIN_DIR/producer"
    exit 1
fi
log_success "Producer binary found"

# 2. Clean up any previous runs
rm -f /tmp/received-messages.txt

# 3. Start NATS
log_info "Starting NATS server..."
if ! docker run -d --name vrsky-e2e-nats -p 4222:4222 nats:latest > /dev/null 2>&1; then
    log_error "Failed to start NATS container"
    exit 1
fi
NATS_PID=1  # Just a marker
log_success "NATS started on port 4222"

# Wait for NATS to be ready
sleep 2

# 4. Start mock HTTP server
log_info "Starting mock HTTP server..."
cd "$PROJECT_ROOT/test/mock-http-server"
go run main.go &
MOCK_HTTP_PID=$!
sleep 1
log_success "Mock HTTP server started on port 9001 (PID: $MOCK_HTTP_PID)"

# 5. Start Consumer (HTTP Input :8000 → NATS Output)
log_info "Starting Consumer..."
cd "$SRC_DIR"
INPUT_TYPE=http \
INPUT_CONFIG='{"port":"8000"}' \
OUTPUT_TYPE=nats \
OUTPUT_CONFIG='{"url":"nats://localhost:4222","subject":"test.messages"}' \
"$BIN_DIR/consumer" &
CONSUMER_PID=$!
log_success "Consumer started on port 8000 (PID: $CONSUMER_PID)"

# 6. Start Producer (NATS Input → HTTP Output :9001)
log_info "Starting Producer..."
INPUT_TYPE=nats \
INPUT_CONFIG='{"url":"nats://localhost:4222","topic":"test.messages"}' \
OUTPUT_TYPE=http \
OUTPUT_CONFIG='{"url":"http://localhost:9001/webhook","method":"POST"}' \
"$BIN_DIR/producer" &
PRODUCER_PID=$!
log_success "Producer started (PID: $PRODUCER_PID)"

# Wait for services to initialize
sleep 2

# 7. Send webhook to Consumer
log_info "Sending webhook to Consumer..."
WEBHOOK_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST http://localhost:8000/webhook \
    -H "Content-Type: application/json" \
    -d '{"order_id":"e2e-test-001","status":"completed","items":["widget"]}')

HTTP_CODE=$(echo "$WEBHOOK_RESPONSE" | tail -n 1)
if [ "$HTTP_CODE" = "202" ]; then
    log_success "Consumer accepted webhook (HTTP 202)"
else
    log_error "Consumer rejected webhook (HTTP $HTTP_CODE)"
    exit 1
fi

# 8. Wait for message to propagate through pipeline
log_info "Waiting for message to propagate..."
sleep 3

# 9. Verify message reached HTTP endpoint
log_info "Verifying message received..."
if [ ! -f "/tmp/received-messages.txt" ]; then
    log_error "No messages file created - message did not reach HTTP endpoint"
    exit 1
fi

if grep -q "e2e-test-001" /tmp/received-messages.txt; then
    log_success "Message reached HTTP endpoint!"
    log_success "Message content:"
    cat /tmp/received-messages.txt | sed 's/^/    /'
else
    log_error "Expected message with 'e2e-test-001' not found"
    log_info "Messages received:"
    cat /tmp/received-messages.txt | sed 's/^/    /'
    exit 1
fi

# 10. Success!
echo ""
log_success "=========================================================="
log_success "E2E TEST PASSED! Full pipeline works:"
log_success "  HTTP Webhook → Consumer → NATS → Producer → HTTP"
log_success "=========================================================="
exit 0
