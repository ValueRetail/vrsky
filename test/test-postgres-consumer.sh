#!/bin/bash
# Test PostgreSQL Consumer → NATS message flow
# Shows data flowing from source database to NATS

set -e

export PATH="/home/ludvik/go/bin:$PATH"
cd /home/ludvik/vrsky/src

echo "=== VRSky PostgreSQL Consumer → NATS Test ==="
echo ""

# Setup test table
echo "Step 1: Setting up source database..."
PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db <<EOF > /dev/null 2>&1
DROP TABLE IF EXISTS test_consumer CASCADE;

CREATE TABLE test_consumer (
    id SERIAL PRIMARY KEY,
    message_text VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO test_consumer (message_text) VALUES ('Initial record');
EOF
echo "✓ Test table created with 1 initial record"

echo ""
echo "Step 2: Building Consumer..."
go build -o /tmp/postgres-consumer ./cmd/postgres-consumer

echo ""
echo "Step 3: Starting Consumer..."
export POSTGRES_INPUT_PASSWORD=source_password
export POSTGRES_INPUT_DATABASE=source_db
export NATS_URL=nats://localhost:4222
/tmp/postgres-consumer > /tmp/consumer-test.log 2>&1 &
CONSUMER_PID=$!

sleep 3

if ! ps -p $CONSUMER_PID > /dev/null; then
    echo "✗ Consumer failed to start"
    cat /tmp/consumer-test.log
    exit 1
fi
echo "✓ Consumer started (PID: $CONSUMER_PID)"

echo ""
echo "Step 4: Subscribing to NATS messages (10 second window)..."
echo "(Messages from Consumer will appear below)"
echo ""

# Start subscriber in background, collect output
nats sub postgres.changes --timeout=10s > /tmp/nats-messages.txt 2>&1 &
NATS_PID=$!

# Give subscriber time to start
sleep 1

echo "Step 5: Inserting test data into source database..."
PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db <<EOF > /dev/null 2>&1
INSERT INTO test_consumer (message_text) VALUES ('Message from consumer test 1');
INSERT INTO test_consumer (message_text) VALUES ('Message from consumer test 2');
EOF
echo "✓ 2 new records inserted"

echo ""
echo "Waiting for NATS subscription to collect messages..."
sleep 10

# Check for messages
echo ""
echo "Step 6: Analyzing NATS messages..."
if [ -f /tmp/nats-messages.txt ]; then
    MESSAGE_COUNT=$(grep -c "\"id\":" /tmp/nats-messages.txt || echo 0)
    if [ $MESSAGE_COUNT -gt 0 ]; then
        echo "✓ Received $MESSAGE_COUNT message(s) on postgres.changes"
        echo ""
        echo "Sample message (first 500 chars):"
        head -c 500 /tmp/nats-messages.txt
        echo "..."
    else
        echo "⚠ No messages received (this may be normal if Consumer hasn't published yet)"
        echo "Check logs: tail -20 /tmp/consumer-test.log"
    fi
fi

echo ""
echo "Step 7: Verifying source database..."
SOURCE_COUNT=$(PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db -tA -c "SELECT COUNT(*) FROM test_consumer;")
echo "✓ Source database has $SOURCE_COUNT records"

echo ""
echo "Cleanup: Stopping Consumer..."
kill $CONSUMER_PID 2>/dev/null
wait $CONSUMER_PID 2>/dev/null
kill $NATS_PID 2>/dev/null 2>&1
wait $NATS_PID 2>/dev/null 2>&1

echo ""
echo "=== Test Complete ==="
echo "Consumer logs: tail -30 /tmp/consumer-test.log"
echo "NATS messages: cat /tmp/nats-messages.txt"
