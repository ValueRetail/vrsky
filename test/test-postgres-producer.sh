#!/bin/bash
# Test PostgreSQL Producer - NATS → Target Database
# Shows data flowing from NATS to target database

set -e

export PATH="/home/ludvik/go/bin:$PATH"
cd /home/ludvik/vrsky/src

echo "=== VRSky PostgreSQL Producer → Database Test ==="
echo ""

# Setup test table
echo "Step 1: Setting up target database..."
PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db <<EOF > /dev/null 2>&1
DROP TABLE IF EXISTS test_producer CASCADE;

CREATE TABLE test_producer (
    id SERIAL PRIMARY KEY,
    message_text VARCHAR(255),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
EOF
echo "✓ Test table created in target database"

echo ""
echo "Step 2: Building Producer..."
go build -o /tmp/postgres-producer ./cmd/postgres-producer

echo ""
echo "Step 3: Starting Producer..."
export POSTGRES_OUTPUT_PASSWORD=target_password
export POSTGRES_OUTPUT_DATABASE=target_db
export POSTGRES_OUTPUT_PORT=5433
export NATS_URL=nats://localhost:4222
/tmp/postgres-producer > /tmp/producer-test.log 2>&1 &
PRODUCER_PID=$!

sleep 3

if ! ps -p $PRODUCER_PID > /dev/null; then
    echo "✗ Producer failed to start"
    cat /tmp/producer-test.log
    exit 1
fi
echo "✓ Producer started (PID: $PRODUCER_PID)"

echo ""
echo "Step 4: Publishing test messages to NATS..."

# Create a proper CDC message format
cat > /tmp/cdc-message.json <<'EOF'
{
  "id": "test-insert-001",
  "source": "test",
  "content_type": "application/cdc+json",
  "payload": {
    "operation": "INSERT",
    "table": "test_producer",
    "record": {
      "id": 1,
      "message_text": "Test message 1 from NATS"
    }
  }
}
EOF

# Send message
nats pub postgres.changes @/tmp/cdc-message.json
echo "✓ Message published to postgres.changes"

sleep 2

echo ""
echo "Step 5: Checking target database for inserted data..."
TARGET_COUNT=$(PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db -tA -c "SELECT COUNT(*) FROM test_producer;")
echo "✓ Target database has $TARGET_COUNT record(s)"

if [ $TARGET_COUNT -gt 0 ]; then
    echo ""
    echo "Data in target database:"
    PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db -c "SELECT * FROM test_producer;"
fi

echo ""
echo "Step 6: Testing UPDATE operation..."

cat > /tmp/cdc-update.json <<'EOF'
{
  "id": "test-update-001",
  "source": "test",
  "content_type": "application/cdc+json",
  "payload": {
    "operation": "UPDATE",
    "table": "test_producer",
    "record": {
      "id": 1,
      "message_text": "Updated message from NATS"
    }
  }
}
EOF

nats pub postgres.changes @/tmp/cdc-update.json
echo "✓ UPDATE message published"

sleep 2

echo ""
echo "Verifying UPDATE..."
PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db -c "SELECT message_text FROM test_producer WHERE id = 1;"

echo ""
echo "Step 7: Testing DELETE operation..."

cat > /tmp/cdc-delete.json <<'EOF'
{
  "id": "test-delete-001",
  "source": "test",
  "content_type": "application/cdc+json",
  "payload": {
    "operation": "DELETE",
    "table": "test_producer",
    "record": {
      "id": 1
    }
  }
}
EOF

nats pub postgres.changes @/tmp/cdc-delete.json
echo "✓ DELETE message published"

sleep 2

echo ""
echo "Verifying DELETE..."
FINAL_COUNT=$(PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db -tA -c "SELECT COUNT(*) FROM test_producer;")
echo "✓ Final record count: $FINAL_COUNT"

echo ""
echo "Cleanup: Stopping Producer..."
kill $PRODUCER_PID 2>/dev/null
wait $PRODUCER_PID 2>/dev/null

echo ""
echo "=== Test Complete ==="
echo "Producer logs: tail -30 /tmp/producer-test.log"
echo "Target database: SELECT * FROM test_producer;"
