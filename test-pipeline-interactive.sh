#!/bin/bash

################################################################################
# VRSky Interactive Pipeline Test
# 
# Simple utility to test Consumer → NATS → Producer flow manually
# Start Consumer and Producer, then send webhooks interactively
################################################################################

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

export PATH=$PATH:~/go/bin

echo -e "${BLUE}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  VRSky Interactive Pipeline Test - Manual Mode              ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Cleanup function
cleanup() {
    echo ""
    echo -e "${YELLOW}Cleaning up...${NC}"
    pkill -f "bin/consumer" 2>/dev/null || true
    pkill -f "bin/producer" 2>/dev/null || true
    sleep 1
    echo -e "${GREEN}✓ Done${NC}"
}

trap cleanup EXIT

# Ask user for ports
read -p "Consumer port (default 9000): " CONSUMER_PORT
CONSUMER_PORT=${CONSUMER_PORT:-9000}

read -p "NATS subject (default test.manual): " NATS_SUBJECT
NATS_SUBJECT=${NATS_SUBJECT:-test.manual}

echo ""
echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}Starting services...${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
echo ""

cd /home/ludvik/vrsky/src

# Start Consumer
echo -e "${YELLOW}→ Starting Consumer on port $CONSUMER_PORT...${NC}"
INPUT_TYPE=http \
INPUT_CONFIG="{\"port\":\"$CONSUMER_PORT\"}" \
OUTPUT_TYPE=nats \
OUTPUT_CONFIG="{\"url\":\"nats://localhost:4222\",\"subject\":\"$NATS_SUBJECT\"}" \
timeout 600 ./bin/consumer > /tmp/consumer-manual.log 2>&1 &

CONSUMER_PID=$!
sleep 2

if ! kill -0 $CONSUMER_PID 2>/dev/null; then
    echo -e "${RED}✗ Consumer failed to start${NC}"
    cat /tmp/consumer-manual.log
    exit 1
fi

echo -e "${GREEN}✓ Consumer started (PID: $CONSUMER_PID)${NC}"

# Start Producer
echo -e "${YELLOW}→ Starting Producer...${NC}"
INPUT_TYPE=nats \
INPUT_CONFIG="{\"url\":\"nats://localhost:4222\",\"topic\":\"$NATS_SUBJECT\"}" \
OUTPUT_TYPE=http \
OUTPUT_CONFIG="{\"url\":\"http://localhost:9999/webhook\",\"method\":\"POST\"}" \
timeout 600 ./bin/producer > /tmp/producer-manual.log 2>&1 &

PRODUCER_PID=$!
sleep 2

if ! kill -0 $PRODUCER_PID 2>/dev/null; then
    echo -e "${RED}✗ Producer failed to start${NC}"
    cat /tmp/producer-manual.log
    exit 1
fi

echo -e "${GREEN}✓ Producer started (PID: $PRODUCER_PID)${NC}"
echo ""

# Main loop for sending messages
while true; do
    echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}Options:${NC}"
    echo "  1) Send test message (with timestamp)"
    echo "  2) Send custom JSON message"
    echo "  3) Send order-like message"
    echo "  4) View Consumer logs"
    echo "  5) View Producer logs"
    echo "  6) Exit and cleanup"
    echo ""
    read -p "Choose option: " choice

    case $choice in
        1)
            echo ""
            TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
            PAYLOAD="{\"test_id\":\"test-$(date +%s)\",\"message\":\"Hello VRSky!\",\"timestamp\":\"$TIMESTAMP\"}"
            
            echo -e "${YELLOW}Sending:${NC}"
            echo "  URL: http://localhost:$CONSUMER_PORT/webhook"
            echo "  Payload: $PAYLOAD"
            echo ""
            
            RESPONSE=$(curl -s -w "\nHTTP_%{http_code}" -X POST "http://localhost:$CONSUMER_PORT/webhook" \
                -H "Content-Type: application/json" \
                -d "$PAYLOAD")
            
            HTTP_CODE=$(echo "$RESPONSE" | grep "HTTP_" | sed 's/HTTP_//')
            BODY=$(echo "$RESPONSE" | grep -v "HTTP_")
            
            if [ "$HTTP_CODE" == "202" ]; then
                echo -e "${GREEN}✓ HTTP $HTTP_CODE - Message accepted${NC}"
            else
                echo -e "${RED}✗ HTTP $HTTP_CODE - Error${NC}"
                echo "$BODY"
            fi
            
            sleep 1
            CONSUMER_LOG=$(tail -1 /tmp/consumer-manual.log)
            if echo "$CONSUMER_LOG" | grep -q "Webhook queued"; then
                MSG_ID=$(echo "$CONSUMER_LOG" | grep -oP '"id":"[^"]*"' | cut -d'"' -f4)
                echo -e "${GREEN}✓ Message queued to NATS with ID: $MSG_ID${NC}"
            fi
            echo ""
            ;;
            
        2)
            echo ""
            read -p "Enter JSON payload: " CUSTOM_PAYLOAD
            
            echo -e "${YELLOW}Sending:${NC}"
            echo "  URL: http://localhost:$CONSUMER_PORT/webhook"
            echo "  Payload: $CUSTOM_PAYLOAD"
            echo ""
            
            RESPONSE=$(curl -s -w "\nHTTP_%{http_code}" -X POST "http://localhost:$CONSUMER_PORT/webhook" \
                -H "Content-Type: application/json" \
                -d "$CUSTOM_PAYLOAD")
            
            HTTP_CODE=$(echo "$RESPONSE" | grep "HTTP_" | sed 's/HTTP_//')
            
            if [ "$HTTP_CODE" == "202" ]; then
                echo -e "${GREEN}✓ HTTP $HTTP_CODE - Message accepted${NC}"
            else
                echo -e "${RED}✗ HTTP $HTTP_CODE - Error${NC}"
            fi
            echo ""
            ;;
            
        3)
            echo ""
            TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
            PAYLOAD="{\"order_id\":\"ORD-$(date +%s)\",\"customer\":\"Test Customer\",\"amount\":99.99,\"status\":\"pending\",\"timestamp\":\"$TIMESTAMP\"}"
            
            echo -e "${YELLOW}Sending order message:${NC}"
            echo "  Payload: $PAYLOAD"
            echo ""
            
            RESPONSE=$(curl -s -w "\nHTTP_%{http_code}" -X POST "http://localhost:$CONSUMER_PORT/webhook" \
                -H "Content-Type: application/json" \
                -d "$PAYLOAD")
            
            HTTP_CODE=$(echo "$RESPONSE" | grep "HTTP_" | sed 's/HTTP_//')
            
            if [ "$HTTP_CODE" == "202" ]; then
                echo -e "${GREEN}✓ HTTP $HTTP_CODE - Order accepted${NC}"
            else
                echo -e "${RED}✗ HTTP $HTTP_CODE - Error${NC}"
            fi
            echo ""
            ;;
            
        4)
            echo ""
            echo -e "${BLUE}Consumer logs (last 10 lines):${NC}"
            echo "─────────────────────────────────────────"
            tail -10 /tmp/consumer-manual.log | sed 's/^/  /'
            echo ""
            ;;
            
        5)
            echo ""
            echo -e "${BLUE}Producer logs (last 10 lines):${NC}"
            echo "─────────────────────────────────────────"
            tail -10 /tmp/producer-manual.log | sed 's/^/  /'
            echo ""
            ;;
            
        6)
            echo ""
            echo -e "${YELLOW}Exiting and cleaning up...${NC}"
            break
            ;;
            
        *)
            echo -e "${RED}Invalid option${NC}"
            echo ""
            ;;
    esac
done

echo -e "${GREEN}✓ Test completed${NC}"
