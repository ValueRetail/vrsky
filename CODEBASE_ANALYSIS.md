# VRSky Codebase Analysis - Complete Reference

**Analysis Date**: March 16, 2026
**Project Status**: Active Development (Post Phase 1)
**Scope**: Logger, Envelope/Message, Component Interfaces, Consumer Examples, Database Patterns

---

## 1. Logger Interface and Setup

### Location
- **Setup function**: `/home/ludvik/vrsky/src/pkg/converter/logger.go`
- **Logging library**: Go standard library `log/slog` (structured logging)

### Logger Implementation
The project uses Go's built-in `log/slog` package directly, NOT a custom interface.

```go
// From converter/logger.go
func SetupLogger(logLevel string) *slog.Logger {
	// Parse log level from environment (debug, info, warn, error)
	// Creates JSON handler for structured logging
	// Returns *slog.Logger
}
```

### Usage Pattern
```go
import "log/slog"

// Declare in your component
logger *slog.Logger

// Use:
logger.Info("message", "key", value)
logger.Warn("warning", "error", err)
logger.Error("error", "stack", stack)
logger.Debug("debug info")

// With context:
logger.InfoContext(ctx, "message", "key", value)
logger.WarnContext(ctx, "message", "key", value)
logger.ErrorContext(ctx, "message", "key", value)
```

### Setup in Components
Most components receive `*slog.Logger` as a parameter during initialization:
```go
func NewComponentName(logger *slog.Logger) (*ComponentImpl, error) {
    if logger == nil {
        logger = slog.Default()
    }
    // Use logger throughout component
}
```

---

## 2. Envelope/Message Type

### Location
- **Definition**: `/home/ludvik/vrsky/src/pkg/envelope/envelope.go`
- **Package**: `github.com/ValueRetail/vrsky/pkg/envelope`

### Envelope Structure
```go
type Envelope struct {
	// Core identifiers
	ID            string `json:"id"`            // Unique message ID
	TenantID      string `json:"tenant_id"`    // Multi-tenant isolation
	IntegrationID string `json:"integration_id"` // Integration instance

	// Payload (inline or reference)
	Payload     []byte `json:"payload,omitempty"`     // For payloads < 256KB (inline)
	PayloadRef  string `json:"payload_ref,omitempty"` // MinIO reference for large payloads
	PayloadSize int64  `json:"payload_size"`          // Actual payload size
	ContentType string `json:"content_type"`          // MIME type (e.g., "application/json")

	// Pipeline tracking
	Source      string   `json:"source"`       // Component that created this (e.g., "http", "FileConsumer")
	CurrentStep int      `json:"current_step"` // Position in pipeline
	StepHistory []string `json:"step_history"` // Path through pipeline

	// Metadata - arbitrary key-value for custom data
	Metadata map[string]interface{} `json:"metadata,omitempty"` // For CDC: operation, table, etc.

	// Timestamps
	CreatedAt time.Time `json:"created_at"`  // When created
	ExpiresAt time.Time `json:"expires_at"`  // TTL (default: 15 minutes)

	// Error handling
	RetryCount int    `json:"retry_count"`   // Number of retry attempts
	LastError  string `json:"last_error,omitempty"` // Last error message
}
```

### Helper Functions
```go
import "github.com/ValueRetail/vrsky/pkg/envelope"

// Create new envelope with generated ID and default TTL
env := envelope.New()

// Serialize to JSON bytes
data, err := envelope.Marshal(env)

// Deserialize from JSON bytes
env, err := envelope.Unmarshal(data)
```

### Import Statement
```go
import "github.com/ValueRetail/vrsky/pkg/envelope"
```

---

## 3. Component Interfaces

### Location
- **Base Component**: `/home/ludvik/vrsky/src/pkg/component/component.go`
- **I/O Interfaces**: `/home/ludvik/vrsky/src/pkg/component/io.go`
- **Producer Interface**: `/home/ludvik/vrsky/src/pkg/component/producer.go`

### Base Component Interface
```go
type ComponentType string

const (
	TypeConsumer  ComponentType = "consumer"
	TypeProducer  ComponentType = "producer"
	TypeConverter ComponentType = "converter"
	TypeFilter    ComponentType = "filter"
)

type HealthStatus string

const (
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthStopped   HealthStatus = "stopped"
)

type Component interface {
	// Name returns the component's human-readable name
	Name() string

	// Type returns the component type (consumer, producer, converter, filter)
	Type() ComponentType

	// Version returns the component version
	Version() string

	// Start initializes and starts the component
	Start(ctx context.Context) error

	// Stop gracefully shuts down the component
	Stop(ctx context.Context) error

	// Health returns the current health status of the component
	Health() HealthStatus
}
```

### Input Interface (for reading messages)
```go
type Input interface {
	// Start initializes the input and connects to the source
	// Must be called before Read()
	Start(ctx context.Context) error

	// Read retrieves the next message from the input source
	// Returns an error if the input fails or context is cancelled
	Read(ctx context.Context) (*envelope.Envelope, error)

	// Close gracefully shuts down the input source
	Close() error
}
```

### Output Interface (for writing messages)
```go
type Output interface {
	// Start initializes the output and prepares the destination
	// Must be called before Write()
	Start(ctx context.Context) error

	// Write sends an envelope to the output destination
	// Returns an error if the output fails
	Write(ctx context.Context, env *envelope.Envelope) error

	// Close gracefully shuts down the output destination
	Close() error
}
```

### Producer Interface
```go
type Producer interface {
	Component  // Embeds Component interface (Name, Type, Version, Start, Stop, Health)

	// Configure sets up the producer with the given configuration (JSON)
	Configure(config []byte) error

	// Process starts the producer's main loop:
	// - Reads messages from the Input
	// - Publishes them to the Output
	// - Handles retries and errors
	Process(ctx context.Context, input Input, output Output) error
}
```

### Import Statements
```go
import (
	"context"
	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)
```

---

## 4. Existing Consumer Examples

### HTTP Input Consumer

**Location**: `/home/ludvik/vrsky/src/pkg/io/http_input.go`

**Key Pattern**:
- Implements the `Input` interface
- Uses buffered channel to queue messages (buffer size: 100)
- HTTP server handles webhook requests
- Fire-and-forget philosophy (returns 202 Accepted immediately)
- Wraps incoming payload in `envelope.Envelope`

**Key Methods**:
```go
// Create from JSON config
func NewHTTPInput(configJSON json.RawMessage) (*HTTPInput, error)

// Start HTTP server listening on port
func (h *HTTPInput) Start(ctx context.Context) error

// Read next envelope from channel
func (h *HTTPInput) Read(ctx context.Context) (*envelope.Envelope, error)

// Close gracefully shuts down
func (h *HTTPInput) Close() error
```

**Message Flow**:
1. HTTP webhook received at `POST /webhook`
2. Payload wrapped in `Envelope` with ID, source, content type, step history
3. Sent to internal `messages` channel (non-blocking)
4. Caller reads from channel via `Read()`
5. Messages published to NATS or next consumer in pipeline

---

### File Input Consumer

**Location**: `/home/ludvik/vrsky/src/pkg/io/file_input.go`

**Key Pattern**:
- Implements the `Input` interface
- Polls directory for files at configurable interval (default: 5 seconds)
- Publishes to NATS directly in `processFile()`
- Tracks processed files by hash and mtime to prevent reprocessing
- Archive/error directory support
- Exponential backoff retry logic
- Connects to NATS directly (not just using channel)

**Key Fields**:
```go
type FileConsumer struct {
	dir                   string
	pattern               string
	pollInterval          time.Duration
	archiveDir            string
	errorDir              string
	deleteAfterProcessing bool
	maxRetries            int
	retryBackoffMs        int
	messages              chan *envelope.Envelope  // Buffered channel
	subject               string                    // NATS subject
	nc                    *nats.Conn               // Direct NATS connection
	logger                *slog.Logger
	processedFiles        map[string]ProcessedFile // State tracking
	failedFiles           map[string]FileRetry     // Retry tracking
}
```

**Key Methods**:
```go
// Create from environment configuration
func NewFileConsumer(logger *slog.Logger) (*FileConsumer, error)

// Start directory polling
func (f *FileConsumer) Start(ctx context.Context) error

// Read next envelope from channel
func (f *FileConsumer) Read(ctx context.Context) (*envelope.Envelope, error)

// Close consumer gracefully
func (f *FileConsumer) Close() error
```

**Message Flow**:
1. `Start()` creates NATS connection and starts poll goroutine
2. Poll loop runs at configurable interval
3. Files matching pattern are processed
4. `processFile()`:
   - Calculates hash to detect reprocessing
   - Creates envelope with payload
   - Sends to NATS: `nc.Publish(subject, marshaledEnvelope)`
   - Sends to channel: `messages <- envelope`
   - Handles post-processing (delete/archive/leave)
5. Caller reads from channel via `Read()`

**State Persistence**:
- Processed files tracked in memory: `map[string]ProcessedFile`
- Each entry has: hash and modification time
- Failed files tracked with retry count and backoff

---

### PostgreSQL Input Consumer

**Location**: `/home/ludvik/vrsky/src/pkg/io/postgres_input.go`

**Key Pattern**:
- Implements the `Input` interface
- CDC (Change Data Capture) polling with 100ms interval
- Polls table changes by comparing row IDs between polls
- Detects INSERT, UPDATE, DELETE operations
- Batches messages for efficiency (configurable batch size, timeout)
- Direct NATS publishing
- Comprehensive metrics with Prometheus

**Key Fields**:
```go
type PostgresInput struct {
	// Configuration
	host            string
	port            int
	user            string
	password        string
	database        string
	replicationSlot string
	publication     string
	tableFilters    map[string]bool
	batchSize       int
	batchTimeout    time.Duration
	natsSubject     string

	// Connections
	pool     *pgxpool.Pool
	natsConn *nats.Conn

	// Runtime
	logger          *slog.Logger
	messages        chan *envelope.Envelope  // Buffered channel
	pendingBatch    []*envelope.Envelope     // Batch accumulation
	batchTimer      *time.Timer
	seenRows        map[string]map[int64]bool // CDC state tracking
	lsn             uint64                     // Last acknowledged LSN
	metrics         *PostgresConsumerMetrics
	dlqPublisher    *DLQPublisher
}
```

**Message Flow**:
1. `Start()` creates connections to PostgreSQL and NATS
2. `setupReplication()` creates replication slot and publication
3. Poll loop runs every 100ms
4. `fetchChanges()` queries replication slot and table changes
5. `pollTableChanges()` compares current rows with `seenRows` map
6. Changes (INSERT, UPDATE, DELETE) detected and wrapped in envelopes
7. Envelopes added to pending batch
8. Batch flushed when: full OR timeout reached
9. `flushBatch()` publishes all envelopes:
   - Sends to channel: `messages <- envelope`
   - Publishes to NATS: `natsConn.Publish(subject, marshaled)`
10. Caller reads from channel via `Read()`

**State Persistence**:
- In-memory tracking of seen rows: `map[string]map[int64]bool`
- Key: table name → Value: map of row IDs seen
- Detects deletes by comparing with previous state
- LSN (Log Sequence Number) tracked for replication

**Batching Strategy**:
```go
// Batches envelopes for efficient publishing
if len(pendingBatch) >= batchSize {
    flushBatch()  // Publish immediately
} else if batchTimer == nil {
    batchTimer = time.AfterFunc(batchTimeout, flushBatch)  // Publish after timeout
}
```

---

## 5. How Consumers Send Messages to NATS

### Pattern 1: Channel + Direct Publish (FileConsumer, PostgresInput)

Both file and postgres consumers use a **dual approach**:

1. **Send to internal channel** (for application flow):
   ```go
   select {
   case f.messages <- env:
       // Successfully queued to channel
   default:
       // Channel full, drop message (fire-and-forget)
   }
   ```

2. **Publish to NATS** (after channel send):
   ```go
   data, err := envelope.Marshal(env)
   if err != nil {
       // Handle error
   }
   if err := f.nc.Publish(f.subject, data); err != nil {
       // Handle error
   }
   ```

### Pattern 2: Channel Only (HTTPInput)

HTTPInput uses **channel only**:
- Messages queued to channel: `h.messages <- env`
- NATS publishing happens elsewhere (outside consumer)
- Separation of concerns

### NATS Connection Setup

```go
import "github.com/nats-io/nats.go"

// In Start() method:
natsURL := os.Getenv("NATS_URL")
if natsURL == "" {
    natsURL = nats.DefaultURL  // "nats://localhost:4222"
}

nc, err := nats.Connect(natsURL)
if err != nil {
    return fmt.Errorf("connect to NATS: %w", err)
}
f.nc = nc
```

### Publishing to NATS

```go
// Publish envelope to subject
subject := os.Getenv("FILE_INPUT_NATS_SUBJECT")  // e.g., "file.input"

// Marshal envelope to JSON
data, err := envelope.Marshal(env)
if err != nil {
    return fmt.Errorf("marshal: %w", err)
}

// Publish
if err := nc.Publish(subject, data); err != nil {
    return fmt.Errorf("publish: %w", err)
}
```

### Configuration Environment Variables

**FileConsumer**:
- `FILE_INPUT_NATS_URL` - NATS connection URL
- `FILE_INPUT_NATS_SUBJECT` - Subject to publish to (default: "file.input")

**PostgresInput**:
- `NATS_URL` - NATS connection URL
- `POSTGRES_INPUT_SUBJECT` - Subject to publish to (default: "postgres.changes")

---

## 6. Database/StateStore Pattern

### Location
- **Checkpoint Store**: `/home/ludvik/vrsky/src/pkg/checkpoint/store.go`
- **Repository Pattern**: `/home/ludvik/vrsky/src/pkg/managementapi/repository.go`

### Checkpoint Store Pattern

Used for persisting component processing state (resumable checkpoints).

**Interface**:
```go
type Store interface {
	// Save persists a checkpoint (insert or update)
	Save(ctx context.Context, cp *Checkpoint) error

	// Get retrieves a checkpoint for a specific node
	Get(ctx context.Context, tenantID, connectionID, nodeID string) (*Checkpoint, error)

	// Delete removes a checkpoint
	Delete(ctx context.Context, tenantID, connectionID, nodeID string) error

	// DeleteForConnection removes all checkpoints for a connection
	DeleteForConnection(ctx context.Context, tenantID, connectionID string) error
}
```

**Checkpoint Data Structure**:
```go
type Checkpoint struct {
	TenantID               string    `db:"tenant_id"`
	ConnectionID           string    `db:"connection_id"`
	NodeID                 string    `db:"node_id"`
	LastProcessedMessageID string    `db:"last_processed_message_id"`
	LastProcessedAt        time.Time `db:"last_processed_at"`
	MessageCount           int64     `db:"message_count"`
	UpdatedAt              time.Time `db:"updated_at"`
}
```

**PostgreSQL Implementation**:
```go
type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// Uses UPSERT for save (INSERT ... ON CONFLICT)
func (s *PostgresStore) Save(ctx context.Context, cp *Checkpoint) error {
	query := `
		INSERT INTO connection_node_checkpoints (
			tenant_id, connection_id, node_id,
			last_processed_message_id, last_processed_at, message_count, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, connection_id, node_id)
		DO UPDATE SET
			last_processed_message_id = EXCLUDED.last_processed_message_id,
			last_processed_at = EXCLUDED.last_processed_at,
			message_count = EXCLUDED.message_count,
			updated_at = EXCLUDED.updated_at
	`
	// Parameterized queries prevent SQL injection
	_, err := s.db.ExecContext(ctx, query, ...)
	return err
}
```

**In-Memory Implementation** (for testing):
```go
type InMemoryStore struct {
	checkpoints map[string]*Checkpoint
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		checkpoints: make(map[string]*Checkpoint),
	}
}
```

### Repository Pattern

Generic interface for persistence operations.

**Interface**:
```go
type Repository interface {
	// Connection operations
	CreateConnection(ctx context.Context, connection *Connection) error
	GetConnection(ctx context.Context, id string) (*Connection, error)
	ListConnections(ctx context.Context, tenantID string, filters *ListFilters) ([]*Connection, int64, error)
	UpdateConnection(ctx context.Context, connection *Connection) error
	DeleteConnection(ctx context.Context, id string) error
	UpdateConnectionStatus(ctx context.Context, id string, status string, lastError *string) error

	// Event tracking
	CreateConnectionEvent(ctx context.Context, event *ConnectionEvent) error
	GetConnectionEvents(ctx context.Context, connectionID string) ([]*ConnectionEvent, error)

	// Lifecycle
	Close() error
}
```

**Usage Pattern**:
```go
// Create repository
repo := NewPostgresRepository(db)
defer repo.Close()

// CRUD operations
err := repo.CreateConnection(ctx, connection)
conn, err := repo.GetConnection(ctx, id)
err := repo.UpdateConnectionStatus(ctx, id, "running", nil)
err := repo.DeleteConnection(ctx, id)

// List with filtering
conns, total, err := repo.ListConnections(ctx, tenantID, &ListFilters{
	Status: "running",
	Search: "prod",
	Limit: 20,
	Offset: 0,
})
```

---

## 7. Summary Table: Key File Locations

| Component | Location | Type | Key Interface |
|-----------|----------|------|----------------|
| Logger Setup | `src/pkg/converter/logger.go` | Function | N/A (uses `*slog.Logger`) |
| Envelope | `src/pkg/envelope/envelope.go` | Struct | `Envelope` |
| Base Component | `src/pkg/component/component.go` | Interface | `Component` |
| Input | `src/pkg/component/io.go` | Interface | `Input` |
| Output | `src/pkg/component/io.go` | Interface | `Output` |
| Producer | `src/pkg/component/producer.go` | Interface | `Producer` |
| HTTP Consumer | `src/pkg/io/http_input.go` | Struct | `HTTPInput` |
| File Consumer | `src/pkg/io/file_input.go` | Struct | `FileConsumer` |
| Postgres Consumer | `src/pkg/io/postgres_input.go` | Struct | `PostgresInput` |
| HTTP Producer | `src/pkg/io/http_output.go` | Struct | `HTTPOutput` |
| NATS Producer | `src/pkg/io/nats_output.go` | Struct | `NATSOutput` |
| Filter | `src/pkg/filter/filter.go` | Struct | `FilterImpl` |
| Converter | `src/pkg/converter/converter.go` | Struct | `ConverterImpl` |
| Checkpoint Store | `src/pkg/checkpoint/store.go` | Interface + Impl | `Store` |
| Repository | `src/pkg/managementapi/repository.go` | Interface | `Repository` |

---

## 8. Import Paths Quick Reference

```go
// Core interfaces
import "github.com/ValueRetail/vrsky/pkg/component"
import "github.com/ValueRetail/vrsky/pkg/envelope"

// Consumers (Input implementations)
import "github.com/ValueRetail/vrsky/src/pkg/io"

// Producers (Output implementations)
import "github.com/ValueRetail/vrsky/src/pkg/io"

// Filters
import "github.com/ValueRetail/vrsky/pkg/filter"

// Converters
import "github.com/ValueRetail/vrsky/pkg/converter"

// State persistence
import "github.com/ValueRetail/vrsky/pkg/checkpoint"
import "github.com/ValueRetail/vrsky/pkg/managementapi"

// Logging
import "log/slog"

// NATS
import "github.com/nats-io/nats.go"

// Database
import "database/sql"
import "github.com/jackc/pgx/v4/pgxpool"

// Configuration
import "encoding/json"
```

---

## 9. Key Patterns Summary

### Component Lifecycle
1. **Create**: Constructor initializes with dependencies (logger, NATS, config)
2. **Start**: Establishes connections, starts goroutines
3. **Process**: Main loop reading/writing messages
4. **Stop/Close**: Graceful shutdown, cleanup resources

### Message Flow
1. **Input.Read()** → Gets envelope from source
2. **Process** → Transform/validate message (optional)
3. **Output.Write()** → Send to destination
4. **Error handling** → Retries, DLQ, logging

### State Management
- **In-memory state**: Fast, lost on restart (FileConsumer tracked files)
- **Database checkpoints**: Persistent, for resumable processing
- **Environment config**: Loaded at startup
- **NATS subjects**: Routing and multi-tenancy

### Error Handling
- **Wrapped errors**: `fmt.Errorf("context: %w", err)`
- **Structured logging**: `logger.Error("message", "field", value, "error", err)`
- **DLQ**: Failed messages sent to dead-letter queue
- **Retries**: Exponential backoff with max attempts

