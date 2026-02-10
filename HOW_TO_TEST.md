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

## Testing with 3 Terminals

Open 3 terminals in `/home/ludvik/vrsky/src`

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

### Terminal 3: Test It

**Step 1: Check source database has data**
```bash
PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db \
  -c "SELECT COUNT(*) FROM test_cdc_table;"
```

**Step 2: Insert test data into source**
```bash
PGPASSWORD=source_password psql -h localhost -p 5432 -U postgres -d source_db \
  -c "INSERT INTO test_cdc_table (name, email) VALUES ('TestUser', 'test@example.com');"
```

**Step 3: Wait 2 seconds**
```bash
sleep 2
```

**Step 4: Check target database received it**
```bash
PGPASSWORD=target_password psql -h localhost -p 5433 -U postgres -d target_db \
  -c "SELECT * FROM test_cdc_table WHERE name = 'TestUser';"
```

**Step 5: Watch messages on NATS (optional, open Terminal 4)**
```bash
nats sub postgres.changes
```

---

## Expected Results

After inserting data in Terminal 3, you should see:

**In Terminal 3:**
- Source database shows the new record
- Target database shows the same record (eventual consistency)

**In Terminal 4 (optional NATS monitor):**
- Messages appear on `postgres.changes` subject
- Each message contains the CDC change data

**In Terminal 1 & 2 logs:**
- Consumer and Producer show processing activity
- No errors

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

1. **Insert multiple records** in source and watch them appear in target
2. **Update a record** in source and see it change in target
3. **Delete a record** in source and see it disappear from target
4. **Watch NATS** in Terminal 4 to see messages flowing between Consumer and Producer

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

Consumer listens to source database → publishes changes to NATS → Producer receives and writes to target database.

That's it. Test by inserting data in Terminal 3 and watching it appear in the target database.
