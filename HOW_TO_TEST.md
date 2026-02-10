# How to Test VRSky

## Architecture

```
┌────────────┐      ┌──────────┐      ┌──────────┐      ┌──────────┐      ┌────────────┐
│   Source   │      │ Consumer │      │   NATS   │      │ Producer │      │   Target   │
│  Database  │─────▶│          │─────▶│  Broker  │─────▶│          │─────▶│  Database  │
│ (5432)     │      │          │      │ (4222)   │      │          │      │ (5433)     │
└────────────┘      └──────────┘      └──────────┘      └──────────┘      └────────────┘
```

**Flow:**
- Consumer reads changes from source database
- Sends them to NATS broker  
- Producer receives messages from NATS
- Writes data to target database

---

## Testing with 4 Terminals

This tests the complete pub/sub pipeline: Consumer reads from source DB → publishes messages to NATS → Producer consumes messages and writes to target DB.

Open 4 terminals in `/home/ludvik/vrsky/src`

### Terminal 1: Start Consumer

```bash
export PATH="/home/ludvik/go/bin:$PATH"
export POSTGRES_INPUT_PASSWORD=source_password
export POSTGRES_INPUT_DATABASE=source_db
export NATS_URL=nats://localhost:4222

go build -o /tmp/postgres-consumer ./cmd/postgres-consumer
/tmp/postgres-consumer
```

You should see:
```
PostgreSQL Input initialized
Connected to PostgreSQL
Connected to NATS
PostgreSQL Input started
PostgreSQL consumer started successfully
```

The Consumer is now listening to the source database for changes and ready to publish messages to NATS.

### Terminal 2: Start Producer

```bash
export PATH="/home/ludvik/go/bin:$PATH"
export POSTGRES_OUTPUT_PASSWORD=target_password
export POSTGRES_OUTPUT_DATABASE=target_db
export POSTGRES_OUTPUT_PORT=5433
export NATS_URL=nats://localhost:4222

go build -o /tmp/postgres-producer ./cmd/postgres-producer
/tmp/postgres-producer
```

You should see:
```
PostgreSQL Output initialized
Connected to PostgreSQL
Connected to NATS
PostgreSQL Output started
PostgreSQL producer started successfully
```

The Producer is now listening to NATS for messages and ready to write to the target database.

### Terminal 3: Watch NATS Messages

Open a new terminal and subscribe to the message stream to see what's being published:

```bash
nats sub postgres.changes
```

This shows you the actual messages flowing through the system. You'll see messages appear here when the Consumer publishes changes.

### Terminal 4: Insert Test Data and Verify

**Step 1: Insert data into source database**
```bash
PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db \
  -c "INSERT INTO test_cdc_table (name, email) VALUES ('TestUser', 'test@example.com');"
```

**Step 2: Watch Terminal 3**
You should see a message appear on `postgres.changes` subject. This is the Consumer publishing the change.

**Step 3: Check target database received it**
```bash
PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db \
  -c "SELECT * FROM test_cdc_table WHERE name = 'TestUser';"
```

The Producer should have received the message from NATS and written it to the target database.

---

## Expected Results

**In Terminal 3 (NATS messages):**
```
[#1] Received on "postgres.changes":
{message with database change}
```

**In Terminal 4:**
- Source database insert succeeds
- Target database shows the new record
- No errors

**In Terminal 1 & 2 logs:**
- Consumer shows it processed the change
- Producer shows it received and processed the message
- Both stay running, ready for more messages

---

## Databases

```
Source Database
  Host: localhost
  Port: 5432
  Database: source_db
  User: postgres
  Password: source_password
  Test Table: test_cdc_table

Target Database
  Host: localhost
  Port: 5433
  Database: target_db
  User: postgres
  Password: target_password
  Test Table: test_cdc_table
```

---

## What to Try

1. **Watch the message flow** - Insert data in source, see message on NATS (Terminal 3), then see result in target
2. **Insert multiple records** - Watch multiple messages appear on NATS as you insert multiple rows
3. **Update a record** - See how the system handles UPDATE messages
4. **Delete a record** - See how the system handles DELETE messages
5. **Check message format** - Look at the actual message structure on NATS to understand what data is being sent

The key thing to understand: **Every database change becomes a message on NATS that the Producer consumes and applies to the target database.**

This is the foundation for adding Converters, Filters, and other components that can process these messages between Consumer and Producer.

---

## Troubleshooting

**Services won't start?**
```bash
# Kill any existing processes
pkill -f postgres-consumer
pkill -f postgres-producer

# Check if ports are open
nc -zv localhost 5432
nc -zv localhost 5433
nc -zv localhost 4222
```

**No data appearing in target?**
- Check Consumer and Producer logs (Terminal 1 & 2) for errors
- Verify test_cdc_table exists in both databases
- Confirm NATS is running: `nats server info`

**Connection failed error?**
- Check port numbers (target is 5433, not 5432!)
- Verify passwords in environment variables

---

## Summary

This is a **pub/sub message pipeline** for database changes:

1. Consumer reads database changes → publishes to NATS (publisher)
2. Producer subscribes to NATS → applies changes to target database (subscriber)
3. NATS is the message broker connecting them

Watch Terminal 3 to see the actual messages flowing through the system. Later, you can add Converters, Filters, and other components to process these messages in between.
