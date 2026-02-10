#!/bin/bash
# Full end-to-end test: Source DB → Consumer → NATS → Producer → Target DB
# Shows complete data flow through the pipeline

set -e

export PATH="/home/ludvik/go/bin:$PATH"
cd /home/ludvik/vrsky/src

echo "=== VRSky Full End-to-End Pipeline Test ==="
echo ""
echo "Flow: Source DB → Consumer → NATS → Producer → Target DB"
echo ""

# Setup both databases
echo "Step 1: Setting up source and target databases..."

PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db <<EOF > /dev/null 2>&1
DROP TABLE IF EXISTS e2e_pipeline CASCADE;

CREATE TABLE e2e_pipeline (
    id SERIAL PRIMARY KEY,
    title VARCHAR(100),
    description TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO e2e_pipeline (title, description) VALUES
    ('Seed 1', 'Initial data point 1'),
    ('Seed 2', 'Initial data point 2');
EOF

PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db <<EOF > /dev/null 2>&1
DROP TABLE IF EXISTS e2e_pipeline CASCADE;

CREATE TABLE e2e_pipeline (
    id SERIAL PRIMARY KEY,
    title VARCHAR(100),
    description TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
EOF

echo "✓ Tables created in both databases"

# Build components
echo ""
echo "Step 2: Building Consumer and Producer..."
go build -o /tmp/postgres-consumer ./cmd/postgres-consumer
go build -o /tmp/postgres-producer ./cmd/postgres-producer
echo "✓ Binaries built"

# Start Consumer
echo ""
echo "Step 3: Starting Consumer (Source DB → NATS)..."
export POSTGRES_INPUT_PASSWORD=source_password
export POSTGRES_INPUT_DATABASE=source_db
export NATS_URL=nats://localhost:4222
/tmp/postgres-consumer > /tmp/e2e-consumer.log 2>&1 &
CONSUMER_PID=$!
sleep 3

if ! ps -p $CONSUMER_PID > /dev/null; then
    echo "✗ Consumer failed to start"
    cat /tmp/e2e-consumer.log
    exit 1
fi
echo "✓ Consumer started (PID: $CONSUMER_PID)"

# Start Producer
echo ""
echo "Step 4: Starting Producer (NATS → Target DB)..."
export POSTGRES_OUTPUT_PASSWORD=target_password
export POSTGRES_OUTPUT_DATABASE=target_db
export POSTGRES_OUTPUT_PORT=5433
/tmp/postgres-producer > /tmp/e2e-producer.log 2>&1 &
PRODUCER_PID=$!
sleep 3

if ! ps -p $PRODUCER_PID > /dev/null; then
    echo "✗ Producer failed to start"
    cat /tmp/e2e-producer.log
    exit 1
fi
echo "✓ Producer started (PID: $PRODUCER_PID)"

# Start NATS subscriber to monitor messages
echo ""
echo "Step 5: Monitoring NATS messages (5 second window)..."
nats sub postgres.changes --timeout=5s > /tmp/e2e-messages.txt 2>&1 &
NATS_PID=$!
sleep 1

# Insert data into source
echo ""
echo "Step 6: Inserting test data into source database..."
PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db <<EOF > /dev/null 2>&1
INSERT INTO e2e_pipeline (title, description) VALUES
    ('E2E Test 1', 'This should flow through the entire pipeline'),
    ('E2E Test 2', 'Another data point for the test');
EOF
echo "✓ Data inserted"

# Wait for processing
echo ""
echo "Waiting 6 seconds for data to flow through pipeline..."
sleep 6

# Check source
echo ""
echo "Step 7: Verifying data in databases..."
SOURCE_COUNT=$(PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db -tA -c "SELECT COUNT(*) FROM e2e_pipeline;")
echo "✓ Source database: $SOURCE_COUNT records"

TARGET_COUNT=$(PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db -tA -c "SELECT COUNT(*) FROM e2e_pipeline;")
echo "✓ Target database: $TARGET_COUNT records"

# Show actual data
echo ""
echo "=== Source Database Content ==="
PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db -c "SELECT * FROM e2e_pipeline ORDER BY id;" | head -10

echo ""
echo "=== Target Database Content ==="
PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db -c "SELECT * FROM e2e_pipeline ORDER BY id;" | head -10

# Check NATS messages
echo ""
echo "Step 8: Analyzing NATS message flow..."
if [ -f /tmp/e2e-messages.txt ]; then
    MESSAGE_COUNT=$(grep -c "\"id\":" /tmp/e2e-messages.txt 2>/dev/null || echo 0)
    if [ $MESSAGE_COUNT -gt 0 ]; then
        echo "✓ Received $MESSAGE_COUNT message(s) on postgres.changes"
    else
        echo "⚠ No messages captured (Consumer may be using polling instead of streaming)"
    fi
fi

# Status check
echo ""
echo "Step 9: Component Status..."
if ps -p $CONSUMER_PID > /dev/null; then
    echo "✓ Consumer is running"
else
    echo "✗ Consumer stopped"
fi

if ps -p $PRODUCER_PID > /dev/null; then
    echo "✓ Producer is running"
else
    echo "✗ Producer stopped"
fi

# Cleanup
echo ""
echo "Step 10: Cleanup..."
kill $CONSUMER_PID 2>/dev/null
wait $CONSUMER_PID 2>/dev/null
kill $PRODUCER_PID 2>/dev/null
wait $PRODUCER_PID 2>/dev/null
kill $NATS_PID 2>/dev/null 2>&1
wait $NATS_PID 2>/dev/null 2>&1

echo "✓ Services stopped"

echo ""
echo "=== Test Complete ==="
echo ""
echo "Results Summary:"
echo "  Source records: $SOURCE_COUNT"
echo "  Target records: $TARGET_COUNT"
echo "  NATS messages: ${MESSAGE_COUNT:-0}"
echo ""
echo "Logs:"
echo "  Consumer: tail -30 /tmp/e2e-consumer.log"
echo "  Producer: tail -30 /tmp/e2e-producer.log"
echo "  Messages: cat /tmp/e2e-messages.txt"
echo ""

if [ "$SOURCE_COUNT" -gt 2 ] && [ "$TARGET_COUNT" -gt 0 ]; then
    echo "✓ Data flowed from source to target successfully!"
else
    echo "⚠ Data flow incomplete. Check logs for details."
fi
