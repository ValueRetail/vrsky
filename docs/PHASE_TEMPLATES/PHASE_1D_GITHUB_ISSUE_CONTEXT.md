# Phase 1D: Database Connectors (PostgreSQL Consumer & Producer)

## 📋 Overview

Phase 1D implements database connectivity to the VRSky integration platform through PostgreSQL Change Data Capture (CDC) consumer and bulk write producer components. These enable real-time data synchronization between databases and external systems.

**Timeline**: February 17-23, 2026 (1 week, 30-40 hours)  
**Team Size**: 2 engineers  
**Priority**: High (enables core data pipeline functionality)  
**Phase Status**: Planned (ready to start after Phase 1C merge)

---

## 🎯 Acceptance Criteria

### PostgreSQL Consumer Component (postgres_input.go)

#### 1.1 CDC via Logical Replication
- [x] Establish connection to PostgreSQL database using connection string
- [x] Create logical replication slot for change capture
- [x] Stream WAL (Write-Ahead Log) changes from replication slot
- [x] Parse replication stream and extract table changes
- [x] Support filtering by table name (configurable via environment variables)
- [x] Handle inserts, updates, and deletes
- [x] Configuration via environment variables:
  - `POSTGRES_INPUT_HOST` - Database host
  - `POSTGRES_INPUT_PORT` - Database port (default: 5432)
  - `POSTGRES_INPUT_USER` - Database user
  - `POSTGRES_INPUT_PASSWORD` - Database password
  - `POSTGRES_INPUT_DATABASE` - Database name
  - `POSTGRES_INPUT_REPLICATION_SLOT` - Logical replication slot name
  - `POSTGRES_INPUT_PUBLICATION` - Publication name for changes
  - `POSTGRES_INPUT_TABLES` - Comma-separated list of tables to monitor (default: all)
  - `POSTGRES_INPUT_BATCH_SIZE` - Batch size for processing (default: 100)

#### 1.2 Transaction Batching
- [x] Group changes by transaction ID
- [x] Deliver complete transactions atomically via NATS
- [x] Preserve transaction ordering
- [x] Handle multi-row transactions efficiently
- [x] Configurable batch timeout (default: 5 seconds)

#### 1.3 Change Filtering & Preprocessing
- [x] Filter changes by table name
- [x] Option to filter by operation type (INSERT, UPDATE, DELETE)
- [x] Include before/after values for updates
- [x] Add CDC metadata (LSN, timestamp, operation, schema, table)
- [x] Schema information in envelope

#### 1.4 Connection Management
- [x] Graceful connection establishment and teardown
- [x] Automatic reconnection with exponential backoff on failure
- [x] Connection pooling for efficiency
- [x] Health check mechanism
- [x] Graceful shutdown with pending transaction completion

#### 1.5 Envelope Creation
- [x] Create VRSky Envelope with metadata:
  - CDC Operation (INSERT/UPDATE/DELETE)
  - Before values (for UPDATE/DELETE)
  - After values (for INSERT/UPDATE)
  - Timestamp (when change occurred in DB)
  - Transaction ID
  - LSN (Log Sequence Number)
  - Schema and table name
  - Column metadata

#### 1.6 Error Handling & Resilience
- [x] Handle connection failures gracefully
- [x] Retry failed operations with exponential backoff (max 3 retries)
- [x] Dead letter queue for unprocessable messages
- [x] Detailed error logging with context
- [x] Monitoring/observability hooks

### PostgreSQL Producer Component (postgres_output.go)

#### 2.1 Batch Write Operations
- [x] Accept messages from NATS topic
- [x] Parse envelope to extract insert/update/delete operations
- [x] Group messages by operation type
- [x] Execute batch INSERT statements
- [x] Execute batch UPDATE statements
- [x] Execute batch DELETE statements
- [x] Configurable batch size (default: 100, max: 1000)
- [x] Configurable flush timeout (default: 5 seconds)

#### 2.2 Prepared Statements (Security)
- [x] Use parameterized queries for all SQL operations
- [x] Prevent SQL injection attacks
- [x] Dynamic table name validation (against whitelist)
- [x] Column name validation
- [x] Type checking and conversion

#### 2.3 Transaction Management
- [x] Wrap batch operations in transactions
- [x] Atomic multi-statement execution
- [x] Rollback on error
- [x] Commit only on success
- [x] Deadlock detection and retry

#### 2.4 Conflict Resolution (UPSERT)
- [x] Support INSERT OR UPDATE (UPSERT) operations
- [x] Use PostgreSQL ON CONFLICT syntax
- [x] Configurable conflict resolution strategy:
  - INSERT only (fail on conflict)
  - UPSERT (insert or update)
  - UPDATE only (fail if not exists)
- [x] Handle constraint violations gracefully

#### 2.5 Schema & Metadata
- [x] Table name extraction from envelope
- [x] Column mapping from envelope data
- [x] Data type conversion (JSON → PostgreSQL types)
- [x] Null value handling
- [x] Timestamp handling

#### 2.6 Configuration
- [x] Connection configuration via environment variables:
  - `POSTGRES_OUTPUT_HOST` - Database host
  - `POSTGRES_OUTPUT_PORT` - Database port (default: 5432)
  - `POSTGRES_OUTPUT_USER` - Database user
  - `POSTGRES_OUTPUT_PASSWORD` - Database password
  - `POSTGRES_OUTPUT_DATABASE` - Database name
  - `POSTGRES_OUTPUT_BATCH_SIZE` - Batch size (default: 100)
  - `POSTGRES_OUTPUT_FLUSH_INTERVAL` - Flush timeout (default: 5s)
  - `POSTGRES_OUTPUT_CONFLICT_STRATEGY` - Conflict resolution (default: UPSERT)
  - `POSTGRES_OUTPUT_TABLES` - Comma-separated allowed tables (whitelist)

#### 2.7 Connection Management
- [x] Connection pooling
- [x] Automatic reconnection on failure
- [x] Connection health checks
- [x] Graceful shutdown (flush pending batches)
- [x] Idle connection cleanup

#### 2.8 Error Handling & Resilience
- [x] Log all errors with context
- [x] Send failed messages to dead letter queue
- [x] Retry logic with exponential backoff
- [x] Connection failure handling
- [x] Deadlock and timeout handling

### Unit Tests

#### 3.1 PostgreSQL Consumer Tests (15+ tests)
- [ ] **Connection Tests (3)**
  - Test successful database connection
  - Test connection failure and retry
  - Test connection pool management

- [ ] **CDC Replication Tests (4)**
  - Test logical replication slot creation
  - Test WAL stream parsing
  - Test INSERT capture
  - Test UPDATE/DELETE capture

- [ ] **Filtering Tests (3)**
  - Test table name filtering
  - Test operation type filtering
  - Test combined filters

- [ ] **Batching Tests (2)**
  - Test transaction grouping
  - Test batch timeout

- [ ] **Envelope Creation Tests (2)**
  - Test CDC metadata inclusion
  - Test before/after value handling

- [ ] **Error Handling Tests (2)**
  - Test connection failure recovery
  - Test malformed message handling

#### 3.2 PostgreSQL Producer Tests (15+ tests)
- [ ] **Connection Tests (3)**
  - Test successful database connection
  - Test connection failure and retry
  - Test connection pool management

- [ ] **Batch Write Tests (4)**
  - Test batch INSERT
  - Test batch UPDATE
  - Test batch DELETE
  - Test batch timeout and flush

- [ ] **SQL Injection Prevention Tests (3)**
  - Test parameterized queries
  - Test table name validation
  - Test column name validation

- [ ] **Transaction Tests (2)**
  - Test atomic batch execution
  - Test rollback on error

- [ ] **Conflict Resolution Tests (3)**
  - Test INSERT OR UPDATE (UPSERT)
  - Test constraint violation handling
  - Test conflict strategy configuration

- [ ] **Error Handling Tests (2)**
  - Test connection failure recovery
  - Test deadlock handling

#### 3.3 Integration Tests (10+ tests)
- [ ] **End-to-End Pipeline (5)**
  - Insert source data → CDC captures → Producer writes
  - Update source data → CDC captures → Producer writes
  - Delete source data → CDC captures → Producer writes
  - Multi-row transactions
  - Large batch handling (1000+ rows)

- [ ] **Consumer-Producer Integration (3)**
  - CDC → NATS → Producer pipeline
  - Verify data integrity through pipeline
  - Test with various data types

- [ ] **Error Scenarios (2)**
  - Connection failure during pipeline
  - Duplicate message handling (idempotency)

### Docker Support

#### 4.1 PostgreSQL Consumer Docker Image
- [ ] Multi-stage Dockerfile
- [ ] Based on Go Alpine base image
- [ ] ~25MB final image size
- [ ] Health check endpoint or script
- [ ] Environment variable documentation
- [ ] Example docker-compose entry
- [ ] Volume mounts for config/logs (if needed)

#### 4.2 PostgreSQL Producer Docker Image
- [ ] Multi-stage Dockerfile
- [ ] Based on Go Alpine base image
- [ ] ~25MB final image size
- [ ] Health check endpoint or script
- [ ] Environment variable documentation
- [ ] Example docker-compose entry
- [ ] Volume mounts for config/logs (if needed)

#### 4.3 Docker Compose Orchestration
- [ ] Add PostgreSQL service to docker-compose
- [ ] Add consumer service with proper config
- [ ] Add producer service with proper config
- [ ] Define NATS network connectivity
- [ ] Health checks for all services
- [ ] Environment file for configuration
- [ ] Volume mounts for persistent data

### E2E Testing

#### 5.1 E2E Test Script
- [ ] Test script: `test/e2e_database_components.sh`
- [ ] Docker-based pipeline test
- [ ] Test 8+ scenarios:
  1. Basic INSERT capture and write
  2. UPDATE capture and write
  3. DELETE capture and write
  4. Multi-row transaction
  5. Large batch (1000+ rows)
  6. Error recovery
  7. Connection failure resilience
  8. Data integrity verification

- [ ] Automated cleanup
- [ ] Clear pass/fail reporting
- [ ] Support for `--local` and `--docker` flags

### Documentation

#### 6.1 PostgreSQL Consumer Guide (DB_CONSUMER_GUIDE.md)
- [ ] Installation & prerequisites
- [ ] Building from source
- [ ] Configuration options (environment variables)
- [ ] 5+ usage examples:
  1. Basic CDC capture from single table
  2. Multi-table monitoring
  3. Filtered by operation type
  4. Integration with File Producer
  5. Integration with HTTP Producer
- [ ] How it works (CDC architecture)
- [ ] 10+ troubleshooting scenarios with solutions
- [ ] Performance tuning recommendations
- [ ] Monitoring section
- [ ] Integration examples

#### 6.2 PostgreSQL Producer Guide (DB_PRODUCER_GUIDE.md)
- [ ] Installation & prerequisites
- [ ] Building from source
- [ ] Configuration options (environment variables)
- [ ] 5+ usage examples:
  1. Basic write to single table
  2. UPSERT strategy
  3. Multi-table writes
  4. Bulk load scenario
  5. Error handling and DLQ
- [ ] How it works (batching, transactions)
- [ ] 8+ troubleshooting scenarios with solutions
- [ ] Performance tuning for different workloads
- [ ] Data type mapping reference
- [ ] Security best practices

#### 6.3 Database Components Architecture (DATABASE_COMPONENTS_ARCHITECTURE.md)
- [ ] Architecture diagrams (text-based ASCII)
- [ ] Design principles and rationale
- [ ] CDC vs polling comparison
- [ ] Component interaction flow
- [ ] 8+ design decisions with rationale/alternatives:
  1. Logical replication vs polling
  2. Batch vs streaming writes
  3. UPSERT vs separate tables
  4. Connection pooling strategy
  5. Error handling approach
  6. Transaction boundaries
  7. Schema validation timing
  8. Monitoring approach
- [ ] Scalability analysis (horizontal & vertical)
- [ ] Fault tolerance patterns
- [ ] Performance characteristics
- [ ] Limitations and future enhancements

#### 6.4 PostgreSQL CDC Guide (POSTGRES_CDC_GUIDE.md)
- [ ] What is CDC and why it matters
- [ ] PostgreSQL logical replication basics
- [ ] Publication and subscription concepts
- [ ] WAL (Write-Ahead Log) overview
- [ ] LSN (Log Sequence Number) explanation
- [ ] Setup for production systems
- [ ] Troubleshooting common issues
- [ ] Performance considerations

---

## 📊 Implementation Metrics

### Code Statistics (Estimated)
- **Consumer Code**: ~400-500 lines (postgres_input.go)
- **Producer Code**: ~400-500 lines (postgres_output.go)
- **Entry Points**: ~80 lines combined
- **Test Code**: ~800-1000 lines
- **Documentation**: ~1,500 lines
- **Total**: ~3,500-3,700 lines

### Testing
- **Unit Tests**: 30+ tests
- **Integration Tests**: 10+ tests
- **E2E Tests**: 8+ scenarios
- **Total Test Cases**: 48+
- **Expected Pass Rate**: 100%

### Docker
- **Consumer Image Size**: ~25MB
- **Producer Image Size**: ~25MB
- **Build Time**: <30 seconds each

---

## 🔧 Technical Approach

### PostgreSQL Consumer (CDC-Based)

1. **Connection & Setup**
   - Use pgx driver for connection pooling
   - Create logical replication slot (if not exists)
   - Create publication (if not exists)

2. **WAL Streaming**
   - Subscribe to WAL changes via replication slot
   - Parse logical replication messages
   - Extract operation type (INSERT/UPDATE/DELETE)

3. **Change Processing**
   - Buffer changes by transaction
   - Apply table and operation filters
   - Enrich with metadata

4. **Message Publishing**
   - Create VRSky Envelope with CDC data
   - Publish to NATS topic
   - Handle publishing errors

5. **Resilience**
   - Automatic reconnection on failure
   - Exponential backoff for retries
   - LSN tracking for recovery

### PostgreSQL Producer (Batch Writer)

1. **Message Reception**
   - Subscribe to NATS topic
   - Parse incoming envelopes
   - Extract data and operation type

2. **Batch Accumulation**
   - Group messages by operation (INSERT/UPDATE/DELETE)
   - Buffer up to batch_size messages
   - Flush on timeout or batch full

3. **SQL Generation**
   - Build parameterized INSERT/UPDATE/DELETE statements
   - Apply conflict resolution strategy
   - Validate table and column names

4. **Execution**
   - Begin transaction
   - Execute batched statements
   - Commit on success or rollback on error

5. **Error Handling**
   - Retry failed operations
   - Send to DLQ on permanent failure
   - Log detailed error context

---

## 🗂️ File Structure

### New Files to Create
```
src/pkg/io/
├── postgres_input.go              (400-500 lines)
├── postgres_input_test.go         (400-500 lines)
├── postgres_output.go             (400-500 lines)
├── postgres_output_test.go        (400-500 lines)
├── postgres_integration_test.go   (200-300 lines)
└── [existing files unchanged]

src/cmd/
├── postgres-consumer/
│   ├── main.go                    (40 lines)
│   └── Dockerfile                 (multi-stage)
├── postgres-producer/
│   ├── main.go                    (40 lines)
│   └── Dockerfile                 (multi-stage)
└── [existing files]

docs/
├── DB_CONSUMER_GUIDE.md           (400-500 lines)
├── DB_PRODUCER_GUIDE.md           (400-500 lines)
├── DATABASE_COMPONENTS_ARCHITECTURE.md  (500+ lines)
├── POSTGRES_CDC_GUIDE.md          (300-400 lines)
└── [existing files]

test/
├── e2e_database_components.sh     (400-500 lines)
└── [existing files]
```

### Modified Files
```
go.mod                  (add pgx driver)
docker-compose.yml      (add PostgreSQL service, consumer, producer)
factory.go              (register database components)
README.md               (add links to DB guides)
```

---

## 🚀 Implementation Timeline

### Day 1-2: PostgreSQL Consumer
- PostgreSQL connection and setup
- Logical replication implementation
- CDC message parsing and filtering
- Envelope creation with metadata
- Initial unit tests (8-10 tests)
- ~10-12 hours

### Day 3-4: PostgreSQL Producer
- Message reception and batching
- SQL statement generation
- Transaction management
- UPSERT conflict resolution
- Initial unit tests (8-10 tests)
- ~10-12 hours

### Day 5: Integration & Docker
- Integration tests (10+ tests)
- Docker image creation (both components)
- docker-compose orchestration
- E2E test script
- All 48+ tests passing
- ~8-10 hours

### Day 6-7: Documentation & Cleanup
- Consumer guide (400-500 lines)
- Producer guide (400-500 lines)
- Architecture documentation
- CDC guide
- Code review and cleanup
- Final PR preparation
- ~8-10 hours

---

## 📦 Dependencies

### Go Packages Required
- `github.com/jackc/pgx/v4` - PostgreSQL driver with connection pooling
- `github.com/jackc/pgconn` - PostgreSQL connection library
- `github.com/nats-io/nats.go` - NATS client (already in project)
- `github.com/google/uuid` - UUID generation (already in project)

### System Requirements
- PostgreSQL 12+ with logical replication enabled
- Go 1.21+
- Docker & Docker Compose (for containerization)

### Configuration
- `max_wal_senders = 3` in PostgreSQL (for replication)
- `wal_level = logical` in PostgreSQL
- User with replication permissions for CDC

---

## 🔐 Security Considerations

### SQL Injection Prevention
- All SQL uses parameterized queries
- Table and column names validated against whitelist
- Input sanitization for dynamic SQL

### Connection Security
- Support for SSL/TLS database connections
- Password not logged or exposed
- Connection string from environment variables

### Data Privacy
- No unencrypted sensitive data in logs
- Envelope data treated as-is (no modification)
- Proper error messages (no data leakage)

---

## 📈 Success Criteria

### Code Quality
- [ ] All code follows Go best practices (Effective Go)
- [ ] gofmt and goimports clean
- [ ] No golangci-lint errors
- [ ] 80%+ test coverage
- [ ] Clean git history with conventional commits

### Testing
- [ ] 30+ unit tests all passing
- [ ] 10+ integration tests all passing
- [ ] 8+ E2E scenarios all passing
- [ ] All edge cases covered
- [ ] Docker E2E tests passing

### Documentation
- [ ] 1,500+ lines of comprehensive guides
- [ ] 5+ usage examples per component
- [ ] 10+ troubleshooting scenarios per guide
- [ ] Architecture decisions documented
- [ ] Clear integration examples

### Production Readiness
- [ ] Graceful shutdown implemented
- [ ] Exponential backoff for retries
- [ ] Dead letter queue for failures
- [ ] Connection pooling working
- [ ] Health checks implemented
- [ ] Observability hooks in place

---

## 🎯 Definition of Done

Phase 1D is considered complete when:

1. ✅ PostgreSQL Consumer fully functional with CDC
2. ✅ PostgreSQL Producer fully functional with batch writes
3. ✅ 48+ tests passing (unit + integration + E2E)
4. ✅ Docker images built and verified
5. ✅ docker-compose orchestration working
6. ✅ E2E pipeline tests passing
7. ✅ 1,500+ lines of comprehensive documentation
8. ✅ All acceptance criteria met
9. ✅ Code reviewed and approved
10. ✅ Ready for merge to main branch

---

## 📞 Integration Context

### Phase 1 Timeline
- **Phase 1A**: ✅ Complete (Foundation)
- **Phase 1B**: ✅ Complete (HTTP Components)
- **Phase 1C**: ✅ Complete (File Components)
- **Phase 1D**: → Starting (Database Components)
- **Phase 1E**: Planned (Filter Components)

### Related Components
- NATS for messaging (Phase 1A)
- HTTP Consumer/Producer (Phase 1B)
- File Consumer/Producer (Phase 1C)
- Component Factory (Phase 1A)
- Envelope system (Phase 1A)

### Phase 2 Dependencies
- Phase 2 Multi-Tenancy will build on Phase 1D isolation
- Phase 2 APIs will expose database components
- Phase 2 UI will configure DB connectors

---

## 💡 Notes & Considerations

1. **CDC Complexity**: Logical replication is more complex than HTTP but provides real-time, low-overhead data capture
2. **Batch Size Tuning**: Different workloads benefit from different batch sizes; provide good defaults and docs
3. **Connection Pool Sizing**: Essential for performance; document sizing recommendations
4. **Monitoring**: Consider Prometheus metrics for database connections, batch sizes, latency
5. **Idempotency**: Ensure duplicate message handling doesn't corrupt data
6. **Schema Evolution**: Document how schema changes should be handled
7. **Network Resilience**: Test with simulated network failures and package loss

---

**Phase 1D Status**: Ready to start  
**Expected Completion**: February 23, 2026  
**Estimated Effort**: 30-40 hours for 2 engineers  
**Predecessor**: Phase 1C (must be merged first)  
**Successor**: Phase 1E (Filter Components)
