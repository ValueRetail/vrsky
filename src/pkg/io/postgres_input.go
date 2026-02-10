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
	"time"

	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// PostgresInput implements a Consumer that captures changes from PostgreSQL via CDC
type PostgresInput struct {
	// Configuration
	host               string
	port               int
	user               string
	password           string
	database           string
	replicationSlot    string
	publication        string
	tableFilters       map[string]bool // Whitelist of tables to monitor
	batchSize          int
	batchTimeout       time.Duration
	natsURL            string
	natsSubject        string

	// Connection
	pool               *pgxpool.Pool
	conn               *pgx.Conn
	natsConn           *nats.Conn

	// Runtime
	logger             *slog.Logger
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	closed             bool
	closedOnce         sync.Once
	messages           chan *envelope.Envelope
	lsn                uint64 // Last acknowledged LSN
	pendingBatch       []*envelope.Envelope
	batchTimer         *time.Timer
}

// CDCChange represents a change captured from PostgreSQL WAL
type CDCChange struct {
	Operation   string                 `json:"operation"` // INSERT, UPDATE, DELETE
	Schema      string                 `json:"schema"`
	Table       string                 `json:"table"`
	Before      map[string]interface{} `json:"before,omitempty"`
	After       map[string]interface{} `json:"after,omitempty"`
	Timestamp   time.Time              `json:"timestamp"`
	TransactionID uint32               `json:"transaction_id"`
	LSN         uint64                 `json:"lsn"`
}

// NewPostgresInput creates a new PostgreSQL CDC consumer
func NewPostgresInput(logger *slog.Logger) (*PostgresInput, error) {
	if logger == nil {
		logger = slog.Default()
	}

	pi := &PostgresInput{
		logger:           logger,
		messages:         make(chan *envelope.Envelope, 100),
		tableFilters:     make(map[string]bool),
		batchSize:        100,
		batchTimeout:     5 * time.Second,
	}

	// Read configuration from environment
	pi.host = os.Getenv("POSTGRES_INPUT_HOST")
	if pi.host == "" {
		pi.host = "localhost"
	}

	portStr := os.Getenv("POSTGRES_INPUT_PORT")
	if portStr == "" {
		pi.port = 5432
	} else {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("invalid POSTGRES_INPUT_PORT: %w", err)
		}
		pi.port = port
	}

	pi.user = os.Getenv("POSTGRES_INPUT_USER")
	if pi.user == "" {
		pi.user = "postgres"
	}

	pi.password = os.Getenv("POSTGRES_INPUT_PASSWORD")
	if pi.password == "" {
		return nil, fmt.Errorf("POSTGRES_INPUT_PASSWORD must be set")
	}

	pi.database = os.Getenv("POSTGRES_INPUT_DATABASE")
	if pi.database == "" {
		return nil, fmt.Errorf("POSTGRES_INPUT_DATABASE must be set")
	}

	pi.replicationSlot = os.Getenv("POSTGRES_INPUT_REPLICATION_SLOT")
	if pi.replicationSlot == "" {
		pi.replicationSlot = "vrsky_slot"
	}

	pi.publication = os.Getenv("POSTGRES_INPUT_PUBLICATION")
	if pi.publication == "" {
		pi.publication = "vrsky_publication"
	}

	// Parse table filters (comma-separated list)
	tablesStr := os.Getenv("POSTGRES_INPUT_TABLES")
	if tablesStr != "" {
		for _, table := range strings.Split(tablesStr, ",") {
			pi.tableFilters[strings.TrimSpace(table)] = true
		}
	}

	// Batch configuration
	if batchSizeStr := os.Getenv("POSTGRES_INPUT_BATCH_SIZE"); batchSizeStr != "" {
		if batchSize, err := strconv.Atoi(batchSizeStr); err == nil && batchSize > 0 {
			pi.batchSize = batchSize
		}
	}

	// NATS configuration
	pi.natsURL = os.Getenv("NATS_URL")
	if pi.natsURL == "" {
		pi.natsURL = "nats://localhost:4222"
	}

	pi.natsSubject = os.Getenv("POSTGRES_INPUT_SUBJECT")
	if pi.natsSubject == "" {
		pi.natsSubject = "postgres.changes"
	}

	pi.ctx, pi.cancel = context.WithCancel(context.Background())

	pi.logger.Info("PostgreSQL Input initialized",
		"host", pi.host,
		"port", pi.port,
		"database", pi.database,
		"replication_slot", pi.replicationSlot,
		"publication", pi.publication,
		"nats_subject", pi.natsSubject,
	)

	return pi, nil
}

// Start begins consuming changes from PostgreSQL
func (pi *PostgresInput) Start(ctx context.Context) error {
	pi.mu.Lock()
	if pi.closed {
		pi.mu.Unlock()
		return fmt.Errorf("consumer already closed")
	}
	pi.mu.Unlock()

	// Connect to PostgreSQL
	if err := pi.connectPostgres(); err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Connect to NATS
	if err := pi.connectNATS(); err != nil {
		pi.pool.Close()
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	// Setup replication
	if err := pi.setupReplication(); err != nil {
		pi.pool.Close()
		pi.natsConn.Close()
		return fmt.Errorf("failed to setup replication: %w", err)
	}

	// Start polling for changes
	go pi.pollChanges()

	pi.logger.Info("PostgreSQL Input started")
	return nil
}

// connectPostgres establishes connection pool to PostgreSQL
func (pi *PostgresInput) connectPostgres() error {
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?application_name=vrsky_consumer&replication=database",
		pi.user,
		pi.password,
		pi.host,
		pi.port,
		pi.database,
	)

	pool, err := pgxpool.Connect(pi.ctx, connStr)
	if err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Test connection
	if err := pool.Ping(pi.ctx); err != nil {
		pool.Close()
		return fmt.Errorf("failed to ping PostgreSQL: %w", err)
	}

	pi.pool = pool
	pi.logger.Info("Connected to PostgreSQL",
		"host", pi.host,
		"database", pi.database,
	)

	return nil
}

// connectNATS connects to NATS broker
func (pi *PostgresInput) connectNATS() error {
	nc, err := nats.Connect(pi.natsURL)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	pi.natsConn = nc
	pi.logger.Info("Connected to NATS", "url", pi.natsURL)
	return nil
}

// setupReplication creates replication slot and publication if they don't exist
func (pi *PostgresInput) setupReplication() error {
	conn, err := pi.pool.Acquire(pi.ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Release()

	// Check if replication slot exists
	var exists bool
	err = conn.QueryRow(pi.ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name = $1)",
		pi.replicationSlot,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check replication slot: %w", err)
	}

	// Create replication slot if it doesn't exist
	if !exists {
		_, err = conn.Exec(pi.ctx,
			fmt.Sprintf("CREATE_REPLICATION_SLOT %s LOGICAL pgoutput", pi.replicationSlot),
		)
		if err != nil {
			return fmt.Errorf("failed to create replication slot: %w", err)
		}
		pi.logger.Info("Created replication slot", "slot", pi.replicationSlot)
	}

	// Check if publication exists
	err = conn.QueryRow(pi.ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_publication WHERE pubname = $1)",
		pi.publication,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check publication: %w", err)
	}

	// Create publication if it doesn't exist
	if !exists {
		_, err = conn.Exec(pi.ctx,
			fmt.Sprintf("CREATE PUBLICATION %s FOR ALL TABLES", pi.publication),
		)
		if err != nil {
			return fmt.Errorf("failed to create publication: %w", err)
		}
		pi.logger.Info("Created publication", "publication", pi.publication)
	}

	return nil
}

// pollChanges polls for changes in a separate goroutine
func (pi *PostgresInput) pollChanges() {
	defer func() {
		if r := recover(); r != nil {
			pi.logger.Error("Panic in pollChanges", "error", r)
		}
	}()

	for {
		select {
		case <-pi.ctx.Done():
			return
		default:
		}

		if err := pi.fetchChanges(); err != nil {
			pi.logger.Error("Failed to fetch changes", "error", err)
			// Exponential backoff on error
			select {
			case <-pi.ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}
	}
}

// fetchChanges retrieves changes from the replication slot
func (pi *PostgresInput) fetchChanges() error {
	// Get a dedicated connection for replication
	conn, err := pi.pool.Acquire(pi.ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Release()

	// Start replication
	rows, err := conn.Query(pi.ctx,
		fmt.Sprintf(
			"START_REPLICATION SLOT %s LOGICAL 0/0 (proto_version '1', publication_names '%s')",
			pi.replicationSlot,
			pi.publication,
		),
	)
	if err != nil {
		return fmt.Errorf("failed to start replication: %w", err)
	}
	defer rows.Close()

	// Read WAL records
	for rows.Next() {
		select {
		case <-pi.ctx.Done():
			return nil
		default:
		}

		var data []byte
		if err := rows.Scan(&data); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Parse WAL record (simplified - real implementation would parse binary protocol)
		// For now, we'll create envelope with placeholder data
		env := pi.createEnvelopeFromWAL(data)
		if env != nil {
			pi.addToPendingBatch(env)
		}
	}

	return rows.Err()
}

// createEnvelopeFromWAL creates an envelope from WAL data
func (pi *PostgresInput) createEnvelopeFromWAL(data []byte) *envelope.Envelope {
	// Parse WAL record (simplified implementation)
	// In production, would use pgx's binary protocol parser

	var change CDCChange
	if err := json.Unmarshal(data, &change); err != nil {
		pi.logger.Warn("Failed to parse WAL record", "error", err)
		return nil
	}

	// Filter by table if configured
	if len(pi.tableFilters) > 0 && !pi.tableFilters[change.Table] {
		return nil
	}

	// Create VRSky envelope
	env := envelope.New()
	env.ID = fmt.Sprintf("cdc-%d-%d", change.TransactionID, change.LSN)
	env.ContentType = "application/cdc+json"
	env.Source = "PostgresInput"

	// Add CDC metadata
	env.Metadata = map[string]interface{}{
		"operation":       change.Operation,
		"schema":          change.Schema,
		"table":           change.Table,
		"timestamp":       change.Timestamp,
		"transaction_id":  change.TransactionID,
		"lsn":             change.LSN,
	}

	// Add payload with before/after values
	payload := map[string]interface{}{
		"operation": change.Operation,
		"before":    change.Before,
		"after":     change.After,
	}

	if payloadBytes, err := json.Marshal(payload); err == nil {
		env.Payload = payloadBytes
	}

	pi.lsn = change.LSN
	return env
}

// addToPendingBatch adds envelope to pending batch and publishes if full
func (pi *PostgresInput) addToPendingBatch(env *envelope.Envelope) {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	pi.pendingBatch = append(pi.pendingBatch, env)

	// Flush if batch is full
	if len(pi.pendingBatch) >= pi.batchSize {
		pi.flushBatch()
	} else if pi.batchTimer == nil {
		// Start timer for batch timeout
		pi.batchTimer = time.AfterFunc(pi.batchTimeout, func() {
			pi.mu.Lock()
			defer pi.mu.Unlock()
			pi.flushBatch()
		})
	}
}

// flushBatch publishes pending batch to NATS
func (pi *PostgresInput) flushBatch() {
	if len(pi.pendingBatch) == 0 {
		return
	}

	for _, env := range pi.pendingBatch {
		select {
		case pi.messages <- env:
			// Published to channel, attempt NATS publish
			if pi.natsConn != nil {
				if payload, err := json.Marshal(env); err == nil {
					if err := pi.natsConn.Publish(pi.natsSubject, payload); err != nil {
						pi.logger.Error("Failed to publish to NATS", "error", err)
					}
				}
			}
		case <-pi.ctx.Done():
			return
		}
	}

	pi.pendingBatch = nil
	if pi.batchTimer != nil {
		pi.batchTimer.Stop()
		pi.batchTimer = nil
	}
}

// Read returns the next change from the consumer
func (pi *PostgresInput) Read(ctx context.Context) (*envelope.Envelope, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-pi.ctx.Done():
		return nil, fmt.Errorf("consumer closed")
	case env, ok := <-pi.messages:
		if !ok {
			return nil, fmt.Errorf("messages channel closed")
		}
		return env, nil
	}
}

// Close gracefully shuts down the consumer
func (pi *PostgresInput) Close() error {
	pi.closedOnce.Do(func() {
		pi.mu.Lock()
		defer pi.mu.Unlock()

		pi.closed = true

		// Flush any pending batch
		pi.flushBatch()

		// Cancel context
		if pi.cancel != nil {
			pi.cancel()
		}

		// Close channels
		close(pi.messages)

		// Close connections
		if pi.natsConn != nil {
			pi.natsConn.Close()
		}

		if pi.pool != nil {
			pi.pool.Close()
		}

		// Drop replication slot
		if pi.pool != nil {
			conn, err := pi.pool.Acquire(context.Background())
			if err == nil {
				defer conn.Release()
				conn.Exec(context.Background(), fmt.Sprintf("DROP_REPLICATION_SLOT %s", pi.replicationSlot))
			}
		}
	})

	pi.logger.Info("PostgreSQL Input closed")
	return nil
}
