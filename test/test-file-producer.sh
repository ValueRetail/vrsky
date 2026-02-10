#!/bin/bash
# Quick test for File Producer with NATS
# Run this to test file writing to disk

set -e

export PATH="/home/ludvik/go/bin:$PATH"
cd /home/ludvik/vrsky/src

echo "=== VRSky File Producer Test ==="
echo ""

# Setup
OUTPUT_DIR="/tmp/vrsky-file-test"
mkdir -p "$OUTPUT_DIR"
rm -f "$OUTPUT_DIR"/*

echo "Step 1: Building File Producer..."
go build -o /tmp/file-producer ./cmd/file-producer

echo "Step 2: Starting File Producer..."
export FILE_OUTPUT_PATH="$OUTPUT_DIR"
export NATS_URL=nats://localhost:4222
/tmp/file-producer > /tmp/file-producer-test.log 2>&1 &
PROD_PID=$!

sleep 2

if ! ps -p $PROD_PID > /dev/null; then
    echo "✗ File Producer failed to start"
    cat /tmp/file-producer-test.log
    exit 1
fi
echo "✓ File Producer started (PID: $PROD_PID)"

echo ""
echo "Step 3: Publishing test messages to NATS..."
nats pub vrsky.files '{"id":"test-1","name":"file1.txt","size":100}'
nats pub vrsky.files '{"id":"test-2","name":"file2.txt","size":200}'
echo "✓ Messages published"

sleep 1

echo ""
echo "Step 4: Checking output files..."
FILE_COUNT=$(ls -1 "$OUTPUT_DIR" 2>/dev/null | wc -l)
echo "Files created: $FILE_COUNT"

if [ $FILE_COUNT -gt 0 ]; then
    echo "✓ Files were written to disk"
    ls -lh "$OUTPUT_DIR"
else
    echo "✗ No files were created"
fi

echo ""
echo "Step 5: Testing large payload (should be rejected)..."
# Try to send a payload that's too large (test size validation)
LARGE_PAYLOAD=$(python3 -c "import sys; sys.stdout.write('x' * 300000)" 2>/dev/null || echo "x")

if [ ${#LARGE_PAYLOAD} -gt 100000 ]; then
    nats pub vrsky.files "{\"id\":\"large\",\"data\":\"$LARGE_PAYLOAD\"}" 2>/dev/null || true
    sleep 1
    
    if grep -q "payload size\|exceeds" /tmp/file-producer-test.log; then
        echo "✓ Large payload rejected as expected"
    fi
fi

echo ""
echo "Cleanup: Stopping File Producer..."
kill $PROD_PID 2>/dev/null
wait $PROD_PID 2>/dev/null

echo ""
echo "=== Test Complete ==="
echo "Output files location: $OUTPUT_DIR"
echo "Logs: /tmp/file-producer-test.log"
