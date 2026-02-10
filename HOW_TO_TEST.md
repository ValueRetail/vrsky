# How to Test VRSky

## What is VRSky?

VRSky is a **pub/sub message pipeline platform** for data integration. It reads data changes from a source system, publishes them to a message broker (NATS), and applies them to a target system.

Think of it like a **mail routing system**: 
- Consumer = mailbox at source (collects mail)
- NATS = postal service (delivers mail)
- Producer = mailbox at target (delivers mail)

The key insight: Consumer and Producer are **completely decoupled** by NATS, which means you can easily add data transformation, filtering, routing, or any other components in between.

---

## How Things Work - Architecture

### System Components

```
┌────────────┐      ┌──────────┐      ┌──────────┐      ┌──────────┐      ┌────────────┐
│   Source   │      │ Consumer │      │   NATS   │      │ Producer │      │   Target   │
│  Database  │─────▶│          │─────▶│  Broker  │─────▶│          │─────▶│  Database  │
│ (5432)     │      │          │      │ (4222)   │      │          │      │ (5433)     │
└────────────┘      └──────────┘      └──────────┘      └──────────┘      └────────────┘
```

### 1. **Consumer** (PostgreSQL Input)
- **What it does**: Listens to the source database for data changes (inserts, updates, deletes)
- **How it works**: 
  - Polls the source database regularly
  - Detects changes to the `test_cdc_table`
  - Converts each change into a message (the envelope)
  - Publishes the message to NATS on the `postgres.changes` subject
- **Current implementation**: `src/pkg/io/postgres_input.go`
- **Start command**: `postgres-consumer` binary

### 2. **NATS Broker** (Message Hub)
- **What it does**: Central message broker that decouples Consumer from Producer
- **Why it matters**: 
  - Consumer doesn't need to know where messages go
  - Producer doesn't need to know where messages come from
  - Easy to add other components (filters, converters) later
  - Messages persist briefly (can have multiple subscribers)
- **How it works**: 
  - Consumer publishes to `postgres.changes` subject
  - Producer subscribes to `postgres.changes` subject
  - Any message published appears for all subscribers
- **Configuration**: Running on `localhost:4222`

### 3. **Producer** (PostgreSQL Output)
- **What it does**: Listens to NATS for messages and applies them to the target database
- **How it works**:
  - Subscribes to `postgres.changes` subject on NATS
  - Receives each message published by Consumer
  - Extracts the data from the envelope (message format)
  - Applies the operation to the target database (insert/update/delete)
- **Current implementation**: `src/pkg/io/postgres_output.go`
- **Start command**: `postgres-producer` binary

### 4. **Message Format (Envelope)**
- **What it is**: The standardized structure messages use when traveling through NATS
- **Contains**: 
  - What operation happened (INSERT, UPDATE, DELETE)
  - Which table was affected
  - The actual data (old values, new values)
  - Metadata (timestamp, change ID)
- **Implementation**: `src/pkg/envelope/envelope.go`
- **Why it matters**: Allows Consumer and Producer to agree on message format without direct coupling

### Data Flow Explained

```
1. INSERT into source_db.test_cdc_table
        ↓
2. Consumer detects change (polls every ~100ms)
        ↓
3. Consumer creates envelope with:
   {
     "operation": "INSERT",
     "table": "test_cdc_table",
     "data": { "id": 123, "name": "TestUser", "email": "test@example.com" },
     "timestamp": "2024-02-10T10:30:00Z"
   }
        ↓
4. Consumer publishes to NATS:
   Subject: postgres.changes
   Payload: [the envelope above]
        ↓
5. Producer receives message from NATS
        ↓
6. Producer reads the envelope
        ↓
7. Producer executes: 
   INSERT INTO target_db.test_cdc_table (id, name, email) 
   VALUES (123, 'TestUser', 'test@example.com')
        ↓
8. Done! Data is now in target database
```

---

## What Has Been Built

### Currently Working

✅ **PostgreSQL Consumer** - Reads from PostgreSQL, publishes to NATS  
✅ **PostgreSQL Producer** - Consumes from NATS, writes to PostgreSQL  
✅ **NATS Integration** - Message broker fully functional  
✅ **HTTP Consumer/Producer** - Basic HTTP webhook support  
✅ **File Consumer/Producer** - File read/write support  
✅ **Message Envelope** - Standardized message format  
✅ **All Unit Tests** - 99 tests passing, no race conditions

### Currently NOT Built

❌ **Converters** - Transform messages between different formats/schemas  
❌ **Filters** - Route or filter messages based on criteria  
❌ **UI Dashboard** - Visual management of integrations  
❌ **Multi-tenant** - Support for multiple isolated tenants  
❌ **JavaScript/TypeScript Scripting** - Custom transformation logic  
❌ **Streaming Replication** - True CDC streaming (vs polling)

### Key Design Decisions Made

**Why Consumer and Producer are separate:**
- Allows independent scaling (run multiple Consumers or Producers)
- Easy to add filters/converters between them
- Consumer can fail without breaking Producer (NATS buffers messages)

**Why NATS is in the middle:**
- Decouples source from target
- Can publish to multiple targets from one source
- Easy to add processing steps later
- Native support for message routing

**Why polling instead of streaming CDC:**
- Avoids protocol conflicts in connection pools
- Simpler to implement and debug
- Good enough for this phase
- Can upgrade to streaming later

**Why envelope format:**
- Consumer and Producer don't need to know each other's internals
- Easy to swap producers (e.g., MySQL instead of PostgreSQL)
- Room for metadata, error handling, retry info

---

## Architecture

### Quick System Reference

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

---

## What's Next

### Phase 1 - Foundation (Current - Weeks 1-2)
✅ Consumer reads from source database  
✅ Producer writes to target database  
✅ NATS integration working  
✅ Testing framework in place

### Phase 2 - Data Transformation (Weeks 3-4)
- **Converters**: Transform message format between schemas
  - Example: PostgreSQL column → different name/type in target
  - Example: Normalize data before writing
- **Filters**: Route or filter messages based on criteria
  - Example: Only sync certain tables
  - Example: Only process records where field = value

### Phase 3 - Enterprise Features (Weeks 5+)
- **UI Dashboard**: Visual integration management
- **Multi-tenant isolation**: Separate customers' data
- **JavaScript/TypeScript scripting**: Custom transformations
- **Stream replication**: True CDC instead of polling
- **Error handling**: Dead letter queues, retries, backoff
- **Monitoring**: Metrics, dashboards, alerting

### How New Components Fit In

```
Source DB
   │
   ▼
Consumer (reads from source)
   │
   ├─▶ Filter₁ (filter records)
   │     │
   │     ├─▶ Converter₁ (transform format)
   │     │     │
   │     │     └─▶ Filter₂ (route by rules)
   │     │          │
   │     │          └─▶ NATS (publish)
   │     │
   │     └─▶ [skip record]
   │
   └─▶ [error handling]
         │
         └─▶ Dead Letter Queue
              │
              └─▶ NATS (error messages)

NATS
   │
   ▼
Producer (receives from NATS)
   │
   └─▶ Target DB (writes)
```

The beauty: All these components are **pluggable**. Consumer publishes the same messages regardless of how many filters/converters subscribe to them.

---

## For Your Partner

### Reading Order

1. **Start here**: This section explains how the system works
2. **Run the tests**: Follow "Testing with 4 Terminals" section
3. **Explore the code**:
   - `src/pkg/io/postgres_input.go` - Consumer (reads from DB)
   - `src/pkg/io/postgres_output.go` - Producer (writes to DB)
   - `src/pkg/envelope/envelope.go` - Message format
4. **Check the docs**: See `docs/` folder for detailed architecture

### Key Concepts to Understand

- **Pub/Sub**: Publisher (Consumer) and Subscriber (Producer) are decoupled
- **Message Envelope**: Standardized format for all messages
- **NATS Subject**: Like a "channel" - publishers send to it, subscribers listen to it
- **Decoupling**: Consumer doesn't know about Producer, Producer doesn't know about Consumer
- **Extensibility**: Easy to add filters, converters, or other processors in between

### Questions to Ask Yourself While Testing

1. **When I insert data in Terminal 4, why do I see a message in Terminal 3?**
   - Because Consumer detected the change and published it to NATS

2. **Why does the same data appear in the target database?**
   - Because Producer received the message and applied it

3. **Could I insert a filter between Consumer and Producer?**
   - Yes! It would subscribe to `postgres.changes`, filter messages, then publish to a different subject

4. **Could I have multiple Producers?**
   - Yes! Multiple Producers can subscribe to the same subject and all receive messages

5. **Could the source and target be different database types?**
   - Yes! We could have MySQL Consumer and PostgreSQL Producer - NATS and the envelope format bridge them
