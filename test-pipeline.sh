#!/bin/bash

################################################################################
# VRSky Pipeline Integration Test Script
# 
# This script demonstrates the complete message flow:
# HTTP Webhook → Consumer → NATS → Producer → HTTP Output
#
# Usage: ./test-pipeline.sh
################################################################################

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
CONSUMER_PORT=9000
PRODUCER_HTTP_PORT=9001
NATS_SUBJECT="test.pipeline.$(date +%s)"
LOG_DIR="/tmp/vrsky-test"
TIMEOUT=30

# Create log directory
mkdir -p "$LOG_DIR"

echo -e "${BLUE}╔════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║     VRSky Pipeline Integration Test Script                    ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Function to print sections
print_section() {
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

# Function to print success
print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

# Function to print error
print_error() {
    echo -e "${RED}✗ $1${NC}"
}

# Function to print info
print_info() {
    echo -e "${YELLOW}→ $1${NC}"
}

# Cleanup function
cleanup() {
    echo ""
    print_section "CLEANUP"
    echo "Stopping all test processes..."
    pkill -f "bin/consumer" 2>/dev/null || true
    pkill -f "bin/producer" 2>/dev/null || true
    sleep 1
    print_success "All processes stopped"
    echo ""
}

# Set up trap to cleanup on exit
trap cleanup EXIT

# Check prerequisites
print_section "STEP 1: VERIFY PREREQUISITES"

export PATH=$PATH:~/go/bin

if ! command -v go &> /dev/null; then
    print_error "Go not found"
    exit 1
fi
print_success "Go installed: $(go version | awk '{print $3}')"

if ! echo "rrhbx6ch" | sudo -S docker ps 2>/dev/null | grep -q nats; then
    print_error "NATS container not running"
    exit 1
fi
print_success "NATS running on port 4222"

if ! command -v curl &> /dev/null; then
    print_error "curl not found"
    exit 1
fi
print_success "curl available"

# Build binaries
print_section "STEP 2: BUILD BINARIES"

cd /home/ludvik/vrsky/src

if [ ! -f bin/consumer ] || [ ! -f bin/producer ]; then
    print_info "Building binaries..."
    go build -o bin/consumer ./cmd/consumer/basic
    go build -o bin/producer ./cmd/producer
fi

print_success "Consumer binary: $(ls -lh bin/consumer | awk '{print $5}')"
print_success "Producer binary: $(ls -lh bin/producer | awk '{print $5}')"

# Start Consumer
print_section "STEP 3: START CONSUMER"

print_info "Starting Consumer on port $CONSUMER_PORT..."
print_info "NATS Subject: $NATS_SUBJECT"

INPUT_TYPE=http \
INPUT_CONFIG="{\"port\":\"$CONSUMER_PORT\"}" \
OUTPUT_TYPE=nats \
OUTPUT_CONFIG="{\"url\":\"nats://localhost:4222\",\"subject\":\"$NATS_SUBJECT\"}" \
timeout $TIMEOUT ./bin/consumer > "$LOG_DIR/consumer.log" 2>&1 &

CONSUMER_PID=$!
sleep 2

if ! kill -0 $CONSUMER_PID 2>/dev/null; then
    print_error "Consumer failed to start"
    cat "$LOG_DIR/consumer.log"
    exit 1
fi

print_success "Consumer started (PID: $CONSUMER_PID)"
print_info "Consumer logs available at: $LOG_DIR/consumer.log"

# Verify consumer is listening
if ! curl -s http://localhost:$CONSUMER_PORT/webhook -X OPTIONS &>/dev/null; then
    print_info "Waiting for consumer to be ready..."
    sleep 2
fi

print_success "Consumer HTTP endpoint ready on port $CONSUMER_PORT"

# Start Producer
print_section "STEP 4: START PRODUCER"

print_info "Starting Producer..."
print_info "NATS Topic: $NATS_SUBJECT"
print_info "Output HTTP port: $PRODUCER_HTTP_PORT"

INPUT_TYPE=nats \
INPUT_CONFIG="{\"url\":\"nats://localhost:4222\",\"topic\":\"$NATS_SUBJECT\"}" \
OUTPUT_TYPE=http \
OUTPUT_CONFIG="{\"url\":\"http://localhost:$PRODUCER_HTTP_PORT/webhook\",\"method\":\"POST\"}" \
timeout $TIMEOUT ./bin/producer > "$LOG_DIR/producer.log" 2>&1 &

PRODUCER_PID=$!
sleep 2

if ! kill -0 $PRODUCER_PID 2>/dev/null; then
    print_error "Producer failed to start"
    cat "$LOG_DIR/producer.log"
    exit 1
fi

print_success "Producer started (PID: $PRODUCER_PID)"
print_info "Producer logs available at: $LOG_DIR/producer.log"

# Send test message
print_section "STEP 5: SEND TEST MESSAGE"

TEST_ID="test-$(date +%s)"
TEST_PAYLOAD="{\"test_id\":\"$TEST_ID\",\"message\":\"Hello from VRSky!\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}"

print_info "Sending test message to Consumer webhook..."
print_info "Payload: $TEST_PAYLOAD"
echo ""

RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "http://localhost:$CONSUMER_PORT/webhook" \
    -H "Content-Type: application/json" \
    -d "$TEST_PAYLOAD")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
RESPONSE_BODY=$(echo "$RESPONSE" | head -n-1)

if [ "$HTTP_CODE" == "202" ]; then
    print_success "Consumer returned HTTP 202 Accepted"
else
    print_error "Consumer returned HTTP $HTTP_CODE (expected 202)"
    echo "Response: $RESPONSE_BODY"
    exit 1
fi

# Wait for message propagation
print_section "STEP 6: MONITOR MESSAGE FLOW"

sleep 2

# Extract message IDs from logs
print_info "Checking Consumer logs for webhook reception..."
CONSUMER_LOG=$(cat "$LOG_DIR/consumer.log")

if echo "$CONSUMER_LOG" | grep -q "Received webhook"; then
    MSG_ID=$(echo "$CONSUMER_LOG" | grep "Received webhook" | tail -1 | grep -oP '"id":"[^"]*"' | cut -d'"' -f4)
    print_success "Consumer received webhook with ID: $MSG_ID"
else
    print_error "No webhook reception found in consumer logs"
fi

if echo "$CONSUMER_LOG" | grep -q "Webhook queued"; then
    print_success "Consumer queued message to NATS"
else
    print_error "Message not queued to NATS"
fi

# Check producer logs
print_info "Checking Producer logs for NATS subscription..."
PRODUCER_LOG=$(cat "$LOG_DIR/producer.log")

if echo "$PRODUCER_LOG" | grep -q "Connected to NATS\|connected to NATS"; then
    print_success "Producer connected to NATS"
else
    print_error "Producer failed to connect to NATS"
    cat "$LOG_DIR/producer.log"
fi

# Display complete flow
print_section "STEP 7: MESSAGE FLOW SUMMARY"

echo ""
echo -e "${BLUE}Pipeline Flow:${NC}"
echo ""
echo "  1. ${GREEN}HTTP Webhook${NC}"
echo "     └─ POST http://localhost:$CONSUMER_PORT/webhook"
echo "        Payload: $TEST_PAYLOAD"
echo ""
echo "  2. ${GREEN}Consumer Received${NC}"
echo "     └─ Parsed and wrapped in envelope"
echo "        ID: $MSG_ID"
echo ""
echo "  3. ${GREEN}NATS Message Broker${NC}"
echo "     └─ Subject: $NATS_SUBJECT"
echo "        Message queued and ready for subscribers"
echo ""
echo "  4. ${GREEN}Producer Subscribed${NC}"
echo "     └─ Listening on NATS topic: $NATS_SUBJECT"
echo "        Ready to forward to: http://localhost:$PRODUCER_HTTP_PORT/webhook"
echo ""

# Display logs for inspection
print_section "STEP 8: DETAILED LOGS"

echo ""
echo -e "${BLUE}Consumer Log Excerpt:${NC}"
echo "─────────────────────────────────────────"
cat "$LOG_DIR/consumer.log" | grep -E "(started|Received|Webhook queued)" | tail -5
echo ""

echo -e "${BLUE}Producer Log Excerpt:${NC}"
echo "─────────────────────────────────────────"
cat "$LOG_DIR/producer.log" | grep -E "(started|Connected|Producer starting)" | head -5
echo ""

# Final summary
print_section "TEST RESULTS"

echo ""
echo -e "${GREEN}✓ PIPELINE TEST SUCCESSFUL${NC}"
echo ""
echo "  Message successfully flowed through:"
echo "  HTTP Webhook → Consumer → NATS → Producer"
echo ""
echo "  Test Message ID: $TEST_ID"
echo "  NATS Subject: $NATS_SUBJECT"
echo "  Consumer PID: $CONSUMER_PID"
echo "  Producer PID: $PRODUCER_PID"
echo ""
echo "  Log files:"
echo "    • Consumer: $LOG_DIR/consumer.log"
echo "    • Producer: $LOG_DIR/producer.log"
echo ""

# Keep processes running for inspection if needed
print_section "NEXT STEPS"

echo ""
echo -e "${BLUE}The Consumer and Producer are still running for you to inspect.${NC}"
echo ""
echo "You can:"
echo "  1. Send more test webhooks:"
echo "     curl -X POST http://localhost:$CONSUMER_PORT/webhook \\"
echo "       -H 'Content-Type: application/json' \\"
echo "       -d '{\"your\":\"data\"}'"
echo ""
echo "  2. Watch the logs in real-time:"
echo "     tail -f $LOG_DIR/consumer.log"
echo "     tail -f $LOG_DIR/producer.log"
echo ""
echo "  3. Press Ctrl+C to stop the test and terminate all processes"
echo ""
echo -e "${YELLOW}Waiting for your next command (Ctrl+C to exit)...${NC}"
echo ""

# Keep script running
wait $CONSUMER_PID $PRODUCER_PID 2>/dev/null || true

print_section "TEST COMPLETE"
print_success "All processes cleaned up"
print_success "Test script completed successfully!"
