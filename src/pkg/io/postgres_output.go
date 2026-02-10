package io

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// PostgresOutput implements a Producer that writes changes to PostgreSQL
type PostgresOutput struct {
	// Configuration
	host               string
	port               int
	user               string
	password           string
	database           string
	natsURL            string
	natsSubject        string
	batchSize          int
	batchTimeout       time.Duration
	conflictResolution string // "UPSERT" - handles INSERT conflicts by updating existing rows

	// Connection
	pool     *pgxpool.Pool
	natsSub  *nats.Subscription
	natsConn *nats.Conn

	// Runtime
	logger       *slog.Logger
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	closed       bool
	closedOnce   sync.Once
	pendingBatch []*envelope.Envelope
	batchTimer   *time.Timer
	written      int64 // Track written messages
}

// CDCWriteOperation represents a database write operation
type CDCWriteOperation struct {
	Operation    string                 `json:"operation"` // INSERT, UPDATE, DELETE
	Schema       string                 `json:"schema"`
	Table        string                 `json:"table"`
	Values       map[string]interface{} `json:"values"`
	PrimaryKey   map[string]interface{} `json:"primary_key"`
	Before       map[string]interface{} `json:"before,omitempty"`
	After        map[string]interface{} `json:"after,omitempty"`
	Timestamp    time.Time              `json:"timestamp"`
	TransactionID uint32               `json:"transaction_id"`
}

// NewPostgresOutput creates a new PostgreSQL producer
func NewPostgresOutput(logger *slog.Logger) (*PostgresOutput, error) {
	if logger == nil {
		logger = slog.Default()
	}

	po := &PostgresOutput{
		logger:       logger,
		batchSize:    100,
		batchTimeout: 5 * time.Second,
	}

	// Read configuration from environment
	po.host = os.Getenv("POSTGRES_OUTPUT_HOST")
	if po.host == "" {
		po.host = "localhost"
	}

	portStr := os.Getenv("POSTGRES_OUTPUT_PORT")
	if portStr == "" {
		po.port = 5432
	} else {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("invalid POSTGRES_OUTPUT_PORT: %w", err)
		}
		po.port = port
	}

	po.user = os.Getenv("POSTGRES_OUTPUT_USER")
	if po.user == "" {
		po.user = "postgres"
	}

	po.password = os.Getenv("POSTGRES_OUTPUT_PASSWORD")
	if po.password == "" {
		return nil, fmt.Errorf("POSTGRES_OUTPUT_PASSWORD must be set")
	}

	po.database = os.Getenv("POSTGRES_OUTPUT_DATABASE")
	if po.database == "" {
		return nil, fmt.Errorf("POSTGRES_OUTPUT_DATABASE must be set")
	}

	// NATS configuration
	po.natsURL = os.Getenv("NATS_URL")
	if po.natsURL == "" {
		po.natsURL = "nats://localhost:4222"
	}

	po.natsSubject = os.Getenv("POSTGRES_OUTPUT_SUBJECT")
	if po.natsSubject == "" {
		po.natsSubject = "postgres.changes"
	}

	// Batch configuration
	if batchSizeStr := os.Getenv("POSTGRES_OUTPUT_BATCH_SIZE"); batchSizeStr != "" {
		if batchSize, err := strconv.Atoi(batchSizeStr); err == nil && batchSize > 0 {
			po.batchSize = batchSize
		}
	}

	// Conflict resolution strategy
	po.conflictResolution = os.Getenv("POSTGRES_CONFLICT_RESOLUTION")
	if po.conflictResolution == "" {
		po.conflictResolution = "UPSERT"
	}

	// Validate conflict resolution strategy (only UPSERT is currently implemented)
	validStrategies := map[string]bool{
		"UPSERT": true,
	}
	if !validStrategies[po.conflictResolution] {
		return nil, fmt.Errorf("unsupported POSTGRES_CONFLICT_RESOLUTION strategy: %s (only UPSERT is supported)", po.conflictResolution)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())

	po.logger.Info("PostgreSQL Output initialized",
		"host", po.host,
		"port", po.port,
		"database", po.database,
		"nats_subject", po.natsSubject,
		"conflict_resolution", po.conflictResolution,
	)

	return po, nil
}

// Start begins consuming messages from NATS and writing to PostgreSQL
func (po *PostgresOutput) Start(ctx context.Context) error {
	po.mu.Lock()
	if po.closed {
		po.mu.Unlock()
		return fmt.Errorf("producer already closed")
	}
	po.mu.Unlock()

	// Connect to PostgreSQL
	if err := po.connectPostgres(); err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Connect to NATS
	if err := po.connectNATS(); err != nil {
		po.pool.Close()
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	// Subscribe to changes
	if err := po.subscribeChanges(); err != nil {
		po.pool.Close()
		po.natsConn.Close()
		return fmt.Errorf("failed to subscribe to changes: %w", err)
	}

	// Flush any batches that arrived before pool was initialized
	po.mu.Lock()
	if len(po.pendingBatch) > 0 {
		po.logger.Info("Flushing pending batch after pool initialized",
			"batch_size", len(po.pendingBatch))
		po.writeBatch()
	}
	po.mu.Unlock()

	po.logger.Info("PostgreSQL Output started")
	return nil
}

// connectPostgres establishes connection pool to PostgreSQL
func (po *PostgresOutput) connectPostgres() error {
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?application_name=vrsky_producer",
		po.user,
		po.password,
		po.host,
		po.port,
		po.database,
	)

	pool, err := pgxpool.Connect(po.ctx, connStr)
	if err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Test connection
	if err := pool.Ping(po.ctx); err != nil {
		pool.Close()
		return fmt.Errorf("failed to ping PostgreSQL: %w", err)
	}

	po.pool = pool
	po.logger.Info("Connected to PostgreSQL",
		"host", po.host,
		"database", po.database,
	)

	return nil
}

// connectNATS connects to NATS broker
func (po *PostgresOutput) connectNATS() error {
	nc, err := nats.Connect(po.natsURL)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	po.natsConn = nc
	po.logger.Info("Connected to NATS", "url", po.natsURL)
	return nil
}

// subscribeChanges subscribes to CDC changes from NATS
func (po *PostgresOutput) subscribeChanges() error {
	sub, err := po.natsConn.Subscribe(po.natsSubject, func(msg *nats.Msg) {
		env := &envelope.Envelope{}
		if err := json.Unmarshal(msg.Data, env); err != nil {
			po.logger.Warn("Failed to unmarshal envelope", "error", err)
			return
		}

		po.addToPendingBatch(env)
	})

	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	po.natsSub = sub
	return nil
}

// addToPendingBatch adds envelope to pending batch and writes if full
func (po *PostgresOutput) addToPendingBatch(env *envelope.Envelope) {
	po.mu.Lock()
	defer po.mu.Unlock()

	po.pendingBatch = append(po.pendingBatch, env)

	// Write if batch is full
	if len(po.pendingBatch) >= po.batchSize {
		po.writeBatch()
	} else if po.batchTimer == nil {
		// Start timer for batch timeout
		po.batchTimer = time.AfterFunc(po.batchTimeout, func() {
			po.mu.Lock()
			defer po.mu.Unlock()
			po.writeBatch()
		})
	}
}

// writeBatch writes pending batch to PostgreSQL
// Must be called with po.mu held
func (po *PostgresOutput) writeBatch() {
	if len(po.pendingBatch) == 0 {
		return
	}

	// Check if pool is initialized (may not be if Start() hasn't been called yet)
	if po.pool == nil {
		po.logger.Warn("writeBatch called but pool not initialized - will retry with timer",
			"pending_batch_size", len(po.pendingBatch))
		// Set/restart timer to retry writing after pool initializes
		if po.batchTimer == nil {
			po.batchTimer = time.AfterFunc(po.batchTimeout, func() {
				po.mu.Lock()
				defer po.mu.Unlock()
				po.writeBatch()
			})
		}
		return
	}

	batch := po.pendingBatch
	po.pendingBatch = nil

	if po.batchTimer != nil {
		po.batchTimer.Stop()
		po.batchTimer = nil
	}

	// Write in background to avoid blocking
	go po.executeBatch(batch)
}

// executeBatch executes the batch of writes
func (po *PostgresOutput) executeBatch(batch []*envelope.Envelope) {
	if len(batch) == 0 {
		return
	}

	// Start transaction
	tx, err := po.pool.Begin(po.ctx)
	if err != nil {
		po.logger.Error("Failed to begin transaction", "error", err, "batch_size", len(batch))
		return
	}
	defer tx.Rollback(po.ctx)

	for _, env := range batch {
		if err := po.writeEnvelope(tx, env); err != nil {
			po.logger.Error("Failed to write envelope", "error", err, "envelope_id", env.ID)
			// Continue with other envelopes instead of failing entire batch
			continue
		}
		atomic.AddInt64(&po.written, 1)
	}

	// Commit transaction
	if err := tx.Commit(po.ctx); err != nil {
		po.logger.Error("Failed to commit transaction", "error", err, "batch_size", len(batch))
		return
	}

	po.logger.Debug("Batch written successfully", "batch_size", len(batch), "total_written", po.written)
}

// writeEnvelope writes a single envelope to PostgreSQL
func (po *PostgresOutput) writeEnvelope(tx pgx.Tx, env *envelope.Envelope) error {
	if env == nil {
		return fmt.Errorf("envelope is nil")
	}

	if env.Payload == nil {
		return fmt.Errorf("envelope payload is nil")
	}

	// Validate metadata exists
	if env.Metadata == nil {
		return fmt.Errorf("envelope metadata is nil")
	}

	// Parse payload
	var payload map[string]interface{}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	if payload == nil {
		return fmt.Errorf("payload unmarshaled to nil")
	}

	// Get operation with type assertion check
	operation, ok := payload["operation"]
	if !ok {
		return fmt.Errorf("operation not found in payload")
	}

	op, ok := operation.(string)
	if !ok {
		return fmt.Errorf("operation is not a string, got %T", operation)
	}

	// Get metadata table name
	tableName, ok := env.Metadata["table"].(string)
	if !ok {
		return fmt.Errorf("table not found in metadata or is not a string")
	}

	// Execute appropriate write operation
	switch op {
	case "INSERT":
		return po.executeInsert(tx, tableName, payload)
	case "UPDATE":
		return po.executeUpdate(tx, tableName, payload)
	case "DELETE":
		return po.executeDelete(tx, tableName, payload)
	default:
		return fmt.Errorf("unknown operation: %s", op)
	}
}

// executeInsert inserts a record
func (po *PostgresOutput) executeInsert(tx pgx.Tx, tableName string, payload map[string]interface{}) error {
	after, ok := payload["after"].(map[string]interface{})
	if !ok || len(after) == 0 {
		return fmt.Errorf("after values not found for INSERT")
	}

	// Build parameterized query
	columns := make([]string, 0, len(after))
	values := make([]interface{}, 0, len(after))
	placeholders := make([]string, 0, len(after))

	idx := 1
	for col, val := range after {
		columns = append(columns, po.quoteIdentifier(col))
		values = append(values, val)
		placeholders = append(placeholders, fmt.Sprintf("$%d", idx))
		idx++
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		po.quoteIdentifier(tableName),
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
	)

	// For UPSERT strategy, add ON CONFLICT clause
	if po.conflictResolution == "UPSERT" {
		// Find primary key columns (heuristic: assume 'id' or first column)
		pkCol := "id"
		for col := range after {
			if col == "id" {
				pkCol = col
				break
			}
		}

		// Build SET clause for update part
		setClause := make([]string, 0)
		for col := range after {
			if col != pkCol {
				setClause = append(setClause, fmt.Sprintf("%s = EXCLUDED.%s", po.quoteIdentifier(col), po.quoteIdentifier(col)))
			}
		}

		if len(setClause) > 0 {
			query += fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s",
				po.quoteIdentifier(pkCol),
				strings.Join(setClause, ", "))
		}
	}

	_, err := tx.Exec(po.ctx, query, values...)
	return err
}

// executeUpdate updates a record
func (po *PostgresOutput) executeUpdate(tx pgx.Tx, tableName string, payload map[string]interface{}) error {
	after, ok := payload["after"].(map[string]interface{})
	if !ok || len(after) == 0 {
		return fmt.Errorf("after values not found for UPDATE")
	}

	before, ok := payload["before"].(map[string]interface{})
	if !ok || len(before) == 0 {
		return fmt.Errorf("before values not found for UPDATE")
	}

	// Find primary key from before values
	pkCol := "id"
	var pkValue interface{}

	if id, ok := before["id"]; ok {
		pkValue = id
	} else {
		// Use first column as primary key fallback
		for col, val := range before {
			pkCol = col
			pkValue = val
			break
		}
	}

	// Build SET clause
	setClause := make([]string, 0)
	values := make([]interface{}, 0)
	idx := 1

	for col, val := range after {
		if col != pkCol {
			setClause = append(setClause, fmt.Sprintf("%s = $%d", po.quoteIdentifier(col), idx))
			values = append(values, val)
			idx++
		}
	}

	if len(setClause) == 0 {
		return fmt.Errorf("no columns to update")
	}

	// Add WHERE clause
	values = append(values, pkValue)
	whereClause := fmt.Sprintf("%s = $%d", po.quoteIdentifier(pkCol), idx)

	query := fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s",
		po.quoteIdentifier(tableName),
		strings.Join(setClause, ", "),
		whereClause,
	)

	_, err := tx.Exec(po.ctx, query, values...)
	return err
}

// executeDelete deletes a record
func (po *PostgresOutput) executeDelete(tx pgx.Tx, tableName string, payload map[string]interface{}) error {
	before, ok := payload["before"].(map[string]interface{})
	if !ok || len(before) == 0 {
		return fmt.Errorf("before values not found for DELETE")
	}

	// Find primary key
	pkCol := "id"
	var pkValue interface{}

	if id, ok := before["id"]; ok {
		pkValue = id
	} else {
		// Use first column as primary key fallback
		for col, val := range before {
			pkCol = col
			pkValue = val
			break
		}
	}

	query := fmt.Sprintf(
		"DELETE FROM %s WHERE %s = $1",
		po.quoteIdentifier(tableName),
		po.quoteIdentifier(pkCol),
	)

	_, err := tx.Exec(po.ctx, query, pkValue)
	return err
}

// quoteIdentifier safely quotes SQL identifiers to prevent injection
func (po *PostgresOutput) quoteIdentifier(identifier string) string {
	// Validate identifier contains only safe characters
	for _, ch := range identifier {
		if !((ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '_') {
			// For identifiers with special chars, quote with double quotes
			return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
		}
	}
	return identifier
}

// Write is part of the Producer interface (for direct writes)
func (po *PostgresOutput) Write(ctx context.Context, env *envelope.Envelope) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-po.ctx.Done():
		return fmt.Errorf("producer closed")
	default:
	}

	po.addToPendingBatch(env)
	return nil
}

// WriteBatch is part of the Producer interface (for batch writes)
func (po *PostgresOutput) WriteBatch(ctx context.Context, envelopes []*envelope.Envelope) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-po.ctx.Done():
		return fmt.Errorf("producer closed")
	default:
	}

	if len(envelopes) == 0 {
		return nil
	}

	po.mu.Lock()
	defer po.mu.Unlock()

	po.pendingBatch = append(po.pendingBatch, envelopes...)

	// Check if batch is full and needs to be written
	if len(po.pendingBatch) >= po.batchSize {
		po.writeBatch()
	} else if po.batchTimer == nil && len(po.pendingBatch) > 0 {
		// Start timer for batch timeout if not already running
		po.batchTimer = time.AfterFunc(po.batchTimeout, func() {
			po.mu.Lock()
			defer po.mu.Unlock()
			po.writeBatch()
		})
	}

	return nil
}

// Close gracefully shuts down the producer
func (po *PostgresOutput) Close() error {
	po.closedOnce.Do(func() {
		po.mu.Lock()
		defer po.mu.Unlock()

		po.closed = true

		// Flush any pending batch
		po.writeBatch()

		// Wait a bit for pending writes to complete
		time.Sleep(100 * time.Millisecond)

		// Unsubscribe from NATS
		if po.natsSub != nil {
			po.natsSub.Unsubscribe()
		}

		// Cancel context
		if po.cancel != nil {
			po.cancel()
		}

		// Close connections
		if po.natsConn != nil {
			po.natsConn.Close()
		}

		if po.pool != nil {
			po.pool.Close()
		}
	})

	po.logger.Info("PostgreSQL Output closed", "total_written", po.written)
	return nil
}

// GetWritten returns the count of written messages
func (po *PostgresOutput) GetWritten() int64 {
	return atomic.LoadInt64(&po.written)
}
