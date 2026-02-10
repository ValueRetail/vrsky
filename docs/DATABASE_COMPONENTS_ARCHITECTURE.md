# VRSky Database Components Architecture

## Overview

The VRSky Database Components provide Change Data Capture (CDC) functionality for PostgreSQL, enabling real-time data replication and synchronization across systems. This document describes the architecture, design patterns, and operational characteristics of these components.

## System Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         VRSky Platform                              │
└─────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────┐
│                    PostgreSQL CDC Pipeline                           │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │              PostgreSQL Source Database                     │   │
│  │  ┌────────────────────────────────────────────────────┐    │   │
│  │  │ Tables:                                            │    │   │
│  │  │ - customers                                        │    │   │
│  │  │ - orders                                           │    │   │
│  │  │ - products                                         │    │   │
│  │  │                                                    │    │   │
│  │  │ Configuration:                                     │    │   │
│  │  │ - wal_level=logical                               │    │   │
│  │  │ - max_replication_slots=10                         │    │   │
│  │  └────────────────────────────────────────────────────┘    │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│                ↓ (Logical Replication Stream)                      │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │         PostgreSQL Consumer (PostgresInput)                 │   │
│  │  ┌────────────────────────────────────────────────────┐    │   │
│  │  │ Functions:                                         │    │   │
│  │  │ - Subscribe to logical replication stream         │    │   │
│  │  │ - Parse WAL records                               │    │   │
│  │  │ - Filter by table (optional)                      │    │   │
│  │  │ - Create VRSky envelopes                          │    │   │
│  │  │ - Batch changes by size/timeout                   │    │   │
│  │  │ - Publish to NATS                                 │    │   │
│  │  └────────────────────────────────────────────────────┘    │   │
│  │  Configuration:                                             │   │
│  │  - POSTGRES_INPUT_HOST                                      │   │
│  │  - POSTGRES_INPUT_DATABASE                                  │   │
│  │  - POSTGRES_INPUT_TABLES (optional filter)                  │   │
│  │  - POSTGRES_INPUT_BATCH_SIZE                                │   │
│  │  - NATS_URL                                                 │   │
│  │  - POSTGRES_INPUT_SUBJECT (default: postgres.changes)       │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│                ↓ (NATS Messaging)                                  │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                  NATS Broker                                │   │
│  │  ┌────────────────────────────────────────────────────┐    │   │
│  │  │ Subject: postgres.changes                          │    │   │
│  │  │ Message Format: JSON-serialized VRSky Envelope     │    │   │
│  │  │ Message Size: < 256KB (inline) or reference        │    │   │
│  │  │                                                    │    │   │
│  │  │ Subscribers:                                       │    │   │
│  │  │ - PostgreSQL Producer                             │    │   │
│  │  │ - (Other CDC consumers)                            │    │   │
│  │  └────────────────────────────────────────────────────┘    │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│                ↓ (NATS Subscription)                               │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │         PostgreSQL Producer (PostgresOutput)                │   │
│  │  ┌────────────────────────────────────────────────────┐    │   │
│  │  │ Functions:                                         │    │   │
│  │  │ - Subscribe to NATS messages                       │    │   │
│  │  │ - Deserialize VRSky envelopes                      │    │   │
│  │  │ - Parse CDC operations (INSERT/UPDATE/DELETE)     │    │   │
│  │  │ - Accumulate changes in batch                      │    │   │
│  │  │ - Execute batch in transaction                     │    │   │
│  │  │ - Resolve conflicts (UPSERT/REPLACE/SKIP)         │    │   │
│  │  └────────────────────────────────────────────────────┘    │   │
│  │  Configuration:                                             │   │
│  │  - POSTGRES_OUTPUT_HOST                                     │   │
│  │  - POSTGRES_OUTPUT_DATABASE                                 │   │
│  │  - POSTGRES_OUTPUT_BATCH_SIZE                               │   │
│  │  - POSTGRES_CONFLICT_RESOLUTION (UPSERT/REPLACE/SKIP)       │   │
│  │  - NATS_URL                                                 │   │
│  │  - POSTGRES_OUTPUT_SUBJECT (default: postgres.changes)      │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│                ↓ (SQL Transactions)                                │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │         PostgreSQL Target Database                          │   │
│  │  ┌────────────────────────────────────────────────────┐    │   │
│  │  │ Synchronized Tables:                               │    │   │
│  │  │ - customers (replicated)                           │    │   │
│  │  │ - orders (replicated)                              │    │   │
│  │  │ - products (replicated)                            │    │   │
│  │  │                                                    │    │   │
│  │  │ Guarantees:                                        │    │   │
│  │  │ - Consistency (via transactions)                   │    │   │
│  │  │ - Atomicity (all-or-nothing updates)              │    │   │
│  │  │ - Durability (persisted to disk)                   │    │   │
│  │  │ - Eventual consistency (async replication)         │    │   │
│  │  └────────────────────────────────────────────────────┘    │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
└──────────────────────────────────────────────────────────────────────┘
```

## Component Details

### PostgreSQL Consumer (Source → NATS)

**Responsibility:** Capture changes from PostgreSQL source database and publish to NATS

**Key Features:**
- Logical replication subscription via pgx
- WAL (Write-Ahead Log) stream parsing
- Change filtering by table (optional)
- Batch accumulation (configurable size/timeout)
- VRSky Envelope creation with CDC metadata
- Graceful shutdown with pending batch flush

**Design Pattern: Change Capture**
```
Source DB → Replication Slot → WAL Stream → Consumer → NATS
```

**Data Flow:**
1. PostgreSQL generates changes in WAL
2. Consumer connects to replication slot
3. Consumer receives WAL stream events
4. Events parsed into CDC changes
5. Changes filtered by table (if configured)
6. Changes accumulated into batch
7. Batch published to NATS when:
   - Batch reaches configured size, OR
   - Batch timeout expires
8. Process repeats

**Failure Modes & Recovery:**
- Connection lost → exponential backoff, auto-reconnect
- WAL parsing error → log, skip message, continue
- NATS publish failure → log, wait, retry
- Graceful shutdown → flush pending batch, cleanup slot

### PostgreSQL Producer (NATS → Target)

**Responsibility:** Consume changes from NATS and apply to PostgreSQL target database

**Key Features:**
- NATS subscription to CDC messages
- Envelope deserialization
- Operation routing (INSERT/UPDATE/DELETE)
- Batch accumulation (configurable size/timeout)
- Transaction-based execution
- Conflict resolution strategies (UPSERT/REPLACE/SKIP)
- Parameterized queries (SQL injection prevention)
- Write counter tracking

**Design Pattern: Change Application**
```
NATS → Producer → Parse → Batch → Transaction → Target DB
```

**Data Flow:**
1. Producer subscribes to NATS subject
2. Messages received from NATS
3. VRSky Envelope deserialized
4. Operation type extracted (INSERT/UPDATE/DELETE)
5. Table name and values parsed from metadata/payload
6. Change queued in pending batch
7. When batch full or timeout:
   - BEGIN TRANSACTION
   - For each change in batch:
     - Route to INSERT/UPDATE/DELETE handler
     - Build parameterized SQL query
     - Apply conflict resolution strategy
     - Execute query
   - COMMIT TRANSACTION
8. Process repeats

**Conflict Resolution:**
- **UPSERT**: INSERT ... ON CONFLICT ... DO UPDATE
  - Idempotent, handles duplicates, preserves source truth
- **REPLACE**: DELETE then INSERT
  - Simple, but risky (data loss)
- **SKIP**: INSERT ... ON CONFLICT ... DO NOTHING
  - Preserves target changes, risks divergence

**Security:**
- Parameterized queries prevent SQL injection
- Safe identifier quoting for table/column names
- No string concatenation in SQL generation

**Failure Modes & Recovery:**
- NATS connection lost → auto-reconnect
- Database connection lost → auto-reconnect
- Transaction conflict → rollback, log, skip batch
- Constraint violation → rollback, log, skip batch
- Graceful shutdown → flush pending batch, close connections

## Message Format

### VRSky Envelope (CDC Message)

```json
{
  "id": "cdc-12345-1048576",
  "source": "PostgresInput",
  "content_type": "application/cdc+json",
  "payload": {
    "operation": "INSERT|UPDATE|DELETE",
    "before": {...},
    "after": {...}
  },
  "metadata": {
    "operation": "INSERT",
    "schema": "public",
    "table": "users",
    "timestamp": "2024-02-10T12:34:56Z",
    "transaction_id": 12345,
    "lsn": 1048576
  },
  "created_at": "2024-02-10T12:34:56Z",
  "expires_at": "2024-02-10T12:49:56Z"
}
```

### Metadata Structure

| Field | Type | Purpose |
|-------|------|---------|
| `operation` | string | INSERT, UPDATE, or DELETE |
| `schema` | string | Database schema (usually "public") |
| `table` | string | Table name |
| `timestamp` | ISO8601 | When change occurred |
| `transaction_id` | uint32 | PostgreSQL transaction ID |
| `lsn` | uint64 | Log Sequence Number (for recovery) |

## Data Guarantees

### Consistency Model

**Strong Consistency:**
- Each transaction in producer is atomic (all-or-nothing)
- Operations apply in order received from NATS
- Parameterized queries prevent corruption
- Database constraints enforced

**Eventual Consistency:**
- Source and target eventually match
- Brief lag during batch accumulation
- Lag bounded by batch timeout (default 5s)
- No data loss (all changes captured)

### Ordering Guarantees

**Per-Transaction Ordering:**
- Changes from same source transaction executed together
- Transaction ID preserved in metadata
- Order preserved within batch

**No Global Ordering:**
- Changes from different transactions may reorder
- NATS guarantees message order within subject
- Producer may batch from different transactions

### Idempotency

**With UPSERT:**
- Safe to replay messages
- Duplicate messages don't create duplicates
- Retries automatically handle failures

**With REPLACE:**
- Safe to replay (delete+insert)
- Idempotent but potentially lossy

**With SKIP:**
- Safe to replay (no-op on duplicates)
- May lose target updates

## Performance Characteristics

### Throughput

**Factors:**
- Batch size (larger = higher throughput)
- Network latency (consumer to PostgreSQL, producer to PostgreSQL)
- PostgreSQL write performance
- NATS broker speed

**Optimization:**
- Increase `POSTGRES_INPUT_BATCH_SIZE` for higher throughput
- Use connection pooling (enabled by default)
- Monitor NATS broker for bottlenecks
- Consider database indexes on frequently updated columns

### Latency

**Components:**
- Consumer: Batch accumulation (up to 5s default)
- NATS: Message transit (milliseconds)
- Producer: Batch accumulation (up to 5s default)
- Database: Transaction execution (milliseconds to seconds)

**Typical Latency:** 100ms - 10s (depends on batch timeout and database load)

**Optimization:**
- Reduce batch timeout for lower latency
- Reduce batch size for faster responses
- Increase database resources (CPU, memory, disk)

### Resource Usage

**Consumer:**
- Memory: ~50MB base + batch_size * 1KB per change
- CPU: Low (polling and parsing)
- Network: 1-10 Mbps (depends on change volume)

**Producer:**
- Memory: ~50MB base + batch_size * 1KB per change
- CPU: Low (parsing and query execution)
- Network: 1-10 Mbps (depends on change volume)
- Database I/O: Heavy during batch execution

**Optimization:**
- Batch size: 100-1000 for most workloads
- Connection pool size: 10-20 connections

## Error Handling

### Consumer Error Scenarios

| Error | Severity | Action | Recovery |
|-------|----------|--------|----------|
| PostgreSQL connection lost | HIGH | Log, exponential backoff | Auto-reconnect |
| Invalid WAL record | MEDIUM | Log, skip | Continue |
| NATS publish timeout | MEDIUM | Log, retry | Exponential backoff |
| Replication slot error | CRITICAL | Log, shutdown | Manual intervention |
| Publication missing | CRITICAL | Log, shutdown | Recreate publication |

### Producer Error Scenarios

| Error | Severity | Action | Recovery |
|-------|----------|--------|----------|
| NATS subscription error | CRITICAL | Log, shutdown | Manual intervention |
| PostgreSQL connection lost | HIGH | Log, exponential backoff | Auto-reconnect |
| Invalid envelope | MEDIUM | Log, skip | Continue |
| SQL syntax error | MEDIUM | Log, skip | Review payload |
| Constraint violation | LOW | Log, skip (or retry) | Check data |
| Transaction deadlock | LOW | Log, skip, retry | Exponential backoff |

## Scaling Considerations

### Horizontal Scaling

**Consumer:**
- Cannot scale horizontally (each source DB has one replication slot)
- Can scale by adding multiple source databases
- Each consumer instance subscribes to own source

**Producer:**
- Can scale horizontally (NATS queue group)
- Multiple producers process same subject
- Each consumer processes subset of messages
- Load balanced automatically by NATS

**Pattern:**
```
Source DB 1 → Consumer 1 → NATS (postgres.changes) ↓
Source DB 2 → Consumer 2 → NATS (postgres.changes) ↓ → Producer Group 1
Source DB 3 → Consumer 3 → NATS (postgres.changes) ↓
                                                    ↓
                                           (Load balanced)
                                                    ↓
                                           Target DB
```

### Vertical Scaling

**Consumer:**
- Increase batch size for higher throughput
- Optimize PostgreSQL replication settings
- Add more network bandwidth
- Increase CPU/memory for larger batches

**Producer:**
- Increase batch size for higher throughput
- Increase target database resources
- Add database indexes for faster lookups
- Optimize conflict resolution strategy

## Operational Runbooks

### Start Consumer
1. Verify PostgreSQL is running and accessible
2. Verify NATS is running and accessible
3. Set environment variables
4. Start consumer: `./postgres-consumer`
5. Monitor logs for "started successfully"

### Start Producer
1. Verify target PostgreSQL is running and accessible
2. Verify NATS is running and accessible
3. Set environment variables
4. Create target tables (if not exists)
5. Start producer: `./postgres-producer`
6. Monitor logs for "started successfully"

### Monitor Health
```bash
# Consumer logs
docker logs -f postgres-consumer

# Producer logs
docker logs -f postgres-producer

# NATS messages
nats sub "postgres.changes" --server=nats://localhost:4222

# Database verification
psql -h localhost -U postgres -d target_db -c "SELECT COUNT(*) FROM users;"
```

### Troubleshoot Lag
1. Check consumer logs for errors
2. Check producer logs for errors
3. Verify NATS is not backlogged: `nats stat`
4. Verify database is not slow: `psql ... EXPLAIN ...`
5. Consider increasing batch size
6. Check network latency

## See Also

- [PostgreSQL Consumer Guide](./DB_CONSUMER_GUIDE.md)
- [PostgreSQL Producer Guide](./DB_PRODUCER_GUIDE.md)
- [PostgreSQL CDC Guide](./POSTGRES_CDC_GUIDE.md)
