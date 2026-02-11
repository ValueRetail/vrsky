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
	"github.com/prometheus/client_golang/prometheus"

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
	dropSlotOnClose    bool            // Whether to drop replication slot on Close()

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

	// Observability
	metricsRegistry    prometheus.Registerer
	metrics            *PostgresConsumerMetrics
	dlqPublisher       *DLQPublisher
	backoffConfig      BackoffConfig
	maxRetries         int
	batchStartTime     time.Time // Track latency from capture to publish
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
func NewPostgresInput(logger *slog.Logger, metricsRegistry prometheus.Registerer) (*PostgresInput, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if metricsRegistry == nil {
		metricsRegistry = prometheus.DefaultRegisterer
	}

	pi := &PostgresInput{
		logger:           logger,
		metricsRegistry:  metricsRegistry,
		messages:         make(chan *envelope.Envelope, 100),
		tableFilters:     make(map[string]bool),
		batchSize:        100,
		batchTimeout:     5 * time.Second,
		backoffConfig:    DefaultBackoffConfig(),
		maxRetries:       3,
		metrics:          NewPostgresConsumerMetrics(metricsRegistry),
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

	// Batch configuration with validation
	batchSizeStr := os.Getenv("POSTGRES_INPUT_BATCH_SIZE")
	batchSize, _ := parsePositiveInt(logger, "POSTGRES_INPUT_BATCH_SIZE", batchSizeStr, pi.batchSize)
	pi.batchSize = batchSize

	// Batch timeout configuration with validation
	batchTimeoutStr := os.Getenv("POSTGRES_INPUT_BATCH_TIMEOUT_MS")
	pi.batchTimeout = parseDurationMs(logger, "POSTGRES_INPUT_BATCH_TIMEOUT_MS", batchTimeoutStr, pi.batchTimeout)

	// NATS configuration
	pi.natsURL = os.Getenv("NATS_URL")
	if pi.natsURL == "" {
		pi.natsURL = "nats://localhost:4222"
	}

	pi.natsSubject = os.Getenv("POSTGRES_INPUT_SUBJECT")
	if pi.natsSubject == "" {
		pi.natsSubject = "postgres.changes"
	}

	// Slot cleanup configuration (default: false, don't drop slot automatically)
	// Set POSTGRES_INPUT_DROP_SLOT_ON_CLOSE=true to enable slot cleanup on shutdown
	dropSlotStr := os.Getenv("POSTGRES_INPUT_DROP_SLOT_ON_CLOSE")
	pi.dropSlotOnClose = strings.ToLower(dropSlotStr) == "true"

	// Backoff configuration from environment with validation
	pi.backoffConfig.InitialDuration = parseDurationMs(logger, "POSTGRES_INPUT_INITIAL_BACKOFF_MS",
		os.Getenv("POSTGRES_INPUT_INITIAL_BACKOFF_MS"), DefaultBackoffConfig().InitialDuration)
	pi.backoffConfig.MaxDuration = parseDurationMs(logger, "POSTGRES_INPUT_MAX_BACKOFF_MS",
		os.Getenv("POSTGRES_INPUT_MAX_BACKOFF_MS"), DefaultBackoffConfig().MaxDuration)

	// Validate backoff config (max >= initial, both positive)
	validateBackoffConfig(logger, &pi.backoffConfig)

	// Max retries configuration with validation
	maxRetriesStr := os.Getenv("POSTGRES_INPUT_MAX_RETRIES")
	maxRetries, _ := parsePositiveInt(logger, "POSTGRES_INPUT_MAX_RETRIES", maxRetriesStr, pi.maxRetries)
	pi.maxRetries = maxRetries

	// DLQ configuration from environment
	dlqConfig := DefaultDLQConfig()
	if dlqEnabledStr := os.Getenv("POSTGRES_INPUT_DLQ_ENABLED"); dlqEnabledStr != "" {
		dlqConfig.Enabled = strings.ToLower(dlqEnabledStr) == "true"
	}

	if dlqSubject := os.Getenv("POSTGRES_INPUT_DLQ_SUBJECT"); dlqSubject != "" {
		dlqConfig.Subject = dlqSubject
	}

	// DLQ max retries with validation
	dlqMaxRetriesStr := os.Getenv("POSTGRES_INPUT_DLQ_MAX_RETRIES")
	dlqMaxRetries, _ := parsePositiveInt(logger, "POSTGRES_INPUT_DLQ_MAX_RETRIES", dlqMaxRetriesStr, dlqConfig.MaxRetries)
	dlqConfig.MaxRetries = dlqMaxRetries

	// DLQ publisher will be created after NATS connection in Start()
	// For now, just store the config
	pi.dlqPublisher = &DLQPublisher{
		natsConn: nil, // Will be set in Start()
		config:   dlqConfig,
		logger:   logger,
	}

	pi.ctx, pi.cancel = context.WithCancel(context.Background())

	pi.logger.Info("PostgreSQL Input initialized",
		"host", pi.host,
		"port", pi.port,
		"database", pi.database,
		"replication_slot", pi.replicationSlot,
		"publication", pi.publication,
		"nats_subject", pi.natsSubject,
		"drop_slot_on_close", pi.dropSlotOnClose,
		"max_retries", pi.maxRetries,
		"dlq_enabled", dlqConfig.Enabled,
		"backoff_initial_ms", pi.backoffConfig.InitialDuration.Milliseconds(),
		"backoff_max_ms", pi.backoffConfig.MaxDuration.Milliseconds(),
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
	
	// Derive pi.ctx from the passed context so cancellation/timeouts work
	// If ctx is nil, fall back to background context
	if ctx == nil {
		ctx = context.Background()
	}
	pi.ctx, pi.cancel = context.WithCancel(ctx)
	pi.mu.Unlock()

	// Connect to PostgreSQL
	if err := pi.connectPostgres(); err != nil {
		pi.cancel()
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Connect to NATS
	if err := pi.connectNATS(); err != nil {
		pi.pool.Close()
		pi.cancel()
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	// Setup replication
	if err := pi.setupReplication(); err != nil {
		pi.pool.Close()
		pi.natsConn.Close()
		pi.cancel()
		return fmt.Errorf("failed to setup replication: %w", err)
	}

	// Start polling for changes
	go pi.pollChanges()

	pi.logger.Info("PostgreSQL Input started")
	return nil
}

// connectPostgres establishes connection pool to PostgreSQL
func (pi *PostgresInput) connectPostgres() error {
	// NOTE: Do NOT add replication=database to the main connection pool
	// because replication connections don't support extended query protocol
	// We'll create separate replication connections in fetchChanges() when needed
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?application_name=vrsky_consumer&sslmode=disable",
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

	// Initialize DLQ publisher with NATS connection
	pi.dlqPublisher = NewDLQPublisher(nc, pi.dlqPublisher.config, pi.logger)

	pi.logger.Info("Connected to NATS", "url", pi.natsURL)
	return nil
}

// setupReplication creates replication slot and publication if they don't exist
func (pi *PostgresInput) setupReplication() error {
	// Pre-create replication slot using external tool
	// The consumer can't create it via prepared statements due to PostgreSQL replication protocol limitations
	// Instead, we'll try to create it and ignore errors if it already exists
	
	connStr := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		pi.user, pi.password, pi.host, pi.port, pi.database)
	
	// Open a temporary direct connection to setup replication
	tmpConn, err := pgx.Connect(pi.ctx, connStr)
	if err != nil {
		return fmt.Errorf("failed to connect for replication setup: %w", err)
	}
	defer tmpConn.Close(pi.ctx)
	
	// Try to create replication slot using parameterized query
	// Use pg_create_logical_replication_slot function with parameters
	_, err = tmpConn.Exec(pi.ctx,
		"SELECT pg_create_logical_replication_slot($1, 'pgoutput')",
		pi.replicationSlot)
	if err != nil {
		// Slot might already exist, which is fine
		if !strings.Contains(err.Error(), "already exists") {
			pi.logger.Warn("Replication slot may already exist or failed to create", "slot", pi.replicationSlot, "error", err)
		}
	} else {
		pi.logger.Info("Created replication slot", "slot", pi.replicationSlot)
	}

	// Try to create publication (ignore error if it already exists)
	// Use quoted identifier for publication name to safely handle special characters
	quotedPub := fmt.Sprintf(`"%s"`, strings.ReplaceAll(pi.publication, `"`, `""`))
	_, err = tmpConn.Exec(pi.ctx,
		fmt.Sprintf("CREATE PUBLICATION %s FOR ALL TABLES", quotedPub))
	if err != nil {
		// Publication might already exist, which is fine
		if !strings.Contains(err.Error(), "already exists") {
			pi.logger.Warn("Publication may already exist or failed to create", "publication", pi.publication, "error", err)
		}
	} else {
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

		// Retry with exponential backoff
		attempt := 0
		var lastErr error

		for attempt < pi.maxRetries {
			attempt++

			if err := pi.fetchChanges(); err != nil {
				lastErr = err
				pi.logger.Warn("Failed to fetch changes",
					"error", err,
					"attempt", attempt,
					"max_retries", pi.maxRetries)

				// Record error metric
				pi.metrics.ConnectionErrorsTotal.Inc()

				// If we've exhausted retries, log and break to wait
				if attempt >= pi.maxRetries {
					pi.logger.Error("Max retries exhausted for fetchChanges",
						"error", err,
						"attempt", attempt)
					break
				}

				// Calculate backoff and wait
				backoff := CalculateBackoff(attempt, pi.backoffConfig)
				pi.logger.Debug("Exponential backoff before retry",
					"backoff_duration", backoff,
					"attempt", attempt)

				select {
				case <-pi.ctx.Done():
					return
				case <-time.After(backoff):
					continue // Retry
				}
			} else {
				// Success - reset for next poll
				lastErr = nil // Clear error so cooldown doesn't apply on success
				attempt = 0
				break
			}
		}

		// If we had errors and exhausted retries, wait before next poll cycle
		if lastErr != nil {
			select {
			case <-pi.ctx.Done():
				return
			case <-time.After(5 * time.Second):
				// Continue to next poll cycle
			}
		}
	}
}

// fetchChanges retrieves changes from the replication slot
// NOTE: This POC implementation uses polling instead of true streaming replication
// A production implementation would use pglogrepl with pgconn for actual CDC
func (pi *PostgresInput) fetchChanges() error {
	// Instead of using the replication protocol (which requires pglogrepl and pgconn),
	// we'll use polling with the regular connection pool to simulate CDC for this POC
	
	// Poll for new changes by checking the replication slot status
	// In production, you would use:
	// - pglogrepl.StartReplication() with pgconn
	// - Receive and parse XLogData messages
	// - Send StandbyStatusUpdate to acknowledge received data
	
	// Use parameterized query to safely query replication slot status
	rows, err := pi.pool.Query(pi.ctx,
		"SELECT restart_lsn, confirmed_flush_lsn FROM pg_replication_slots WHERE slot_name = $1",
		pi.replicationSlot,
	)
	if err != nil {
		pi.logger.Error("Failed to query replication slot status", "error", err)
		return fmt.Errorf("failed to query replication slot status: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		var restartLSN, confirmedFlushLSN string
		if err := rows.Scan(&restartLSN, &confirmedFlushLSN); err != nil {
			pi.logger.Error("Failed to scan slot status", "error", err)
			pi.metrics.ParseErrorsTotal.Inc()
			return fmt.Errorf("failed to scan slot status: %w", err)
		}
		pi.logger.Debug("Replication slot status", "restart_lsn", restartLSN, "confirmed_flush_lsn", confirmedFlushLSN)
	}

	// Check for changes in the table
	// This is a simplified polling approach - in production use actual CDC
	changes, err := pi.pollTableChanges()
	if err != nil {
		pi.logger.Error("Failed to poll table changes", "error", err)
		return fmt.Errorf("failed to poll table changes: %w", err)
	}

	// Track batch start time for latency measurement
	if len(changes) > 0 && pi.batchStartTime.IsZero() {
		pi.batchStartTime = time.Now()
	}

	for _, change := range changes {
		env := pi.createEnvelopeFromChange(change)
		if env != nil {
			// Record change captured metric
			pi.metrics.ChangesCapturedTotal.WithLabelValues(change.Operation).Inc()
			pi.addToPendingBatch(env)
		}
	}

	return rows.Err()
}

// pollTableChanges polls for changes using a simple approach
func (pi *PostgresInput) pollTableChanges() ([]CDCChange, error) {
	// This is a placeholder for POC - in production, use real CDC with pglogrepl
	// For now, just return empty to avoid errors
	return []CDCChange{}, nil
}

// createEnvelopeFromChange creates envelope from a CDC change
func (pi *PostgresInput) createEnvelopeFromChange(change CDCChange) *envelope.Envelope {
	// Filter by table if configured
	if len(pi.tableFilters) > 0 && !pi.tableFilters[change.Table] {
		return nil
	}

	env := envelope.New()
	env.ID = fmt.Sprintf("cdc-%d-%d", change.TransactionID, change.LSN)
	env.ContentType = "application/cdc+json"
	env.Source = "PostgresInput"
	
	// Populate metadata for downstream consumers
	env.Metadata = map[string]interface{}{
		"operation":      change.Operation,
		"schema":         change.Schema,
		"table":          change.Table,
		"timestamp":      change.Timestamp,
		"transaction_id": change.TransactionID,
		"lsn":            change.LSN,
	}

	// Marshal change to payload
	data, err := json.Marshal(change)
	if err != nil {
		pi.logger.Error("Failed to marshal CDC change", "error", err, "table", change.Table)
		pi.metrics.ParseErrorsTotal.Inc()

		// Send to DLQ
		if pi.dlqPublisher != nil {
			dlqErr, _ := pi.dlqPublisher.PublishConsumerError(
				env,
				"marshal_error",
				fmt.Sprintf("Failed to marshal CDC change: %v", err),
				1,
				change.Table,
				change.Operation,
				change.LSN,
			)
			if dlqErr != nil {
				pi.logger.Error("Failed to publish marshal error to DLQ", "error", dlqErr)
			}
		}
		return nil
	}
	env.Payload = data
	env.PayloadSize = int64(len(data))

	// Update LSN gauge
	pi.metrics.LSNOffsetGauge.Set(float64(change.LSN))

	return env
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

	// Update pending batch size gauge
	pi.metrics.PendingBatchSizeGauge.Set(float64(len(pi.pendingBatch)))

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

	// Record batch size metric
	pi.metrics.BatchSizeHistogram.Observe(float64(len(pi.pendingBatch)))

	// Record latency from first change to publish
	if !pi.batchStartTime.IsZero() {
		latency := time.Since(pi.batchStartTime).Seconds()
		pi.metrics.CaptureLatencyHistogram.Observe(latency)
		pi.logger.Debug("Batch latency recorded",
			"latency_seconds", latency,
			"batch_size", len(pi.pendingBatch))
		pi.batchStartTime = time.Time{} // Reset
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

	// Record batch published metric
	pi.metrics.BatchesPublishedTotal.Inc()

	// Update pending batch size gauge
	pi.metrics.PendingBatchSizeGauge.Set(0)

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
		// Capture references and update flag while holding lock
		pi.mu.Lock()
		pi.closed = true
		cancel := pi.cancel
		pool := pi.pool
		natsConn := pi.natsConn
		dropSlot := pi.dropSlotOnClose
		slotName := pi.replicationSlot
		pi.mu.Unlock()

		// Release lock before performing I/O operations
		// Flush any pending batch
		pi.mu.Lock()
		pi.flushBatch()
		pi.mu.Unlock()

		// Cancel context
		if cancel != nil {
			cancel()
		}

		// Close channels
		close(pi.messages)

		// Close connections
		if natsConn != nil {
			natsConn.Close()
		}

		// Drop replication slot before closing pool (only if configured)
		if pool != nil && dropSlot {
			// Use context with timeout for cleanup
			ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer ctxCancel()

			// Acquire connection BEFORE closing pool for cleanup
			conn, err := pool.Acquire(ctx)
			if err == nil {
				defer conn.Release()
				// Use parameterized query to safely drop the replication slot
				_, slotErr := conn.Exec(ctx, "SELECT pg_drop_replication_slot($1)", slotName)
				if slotErr != nil {
					pi.logger.Warn("Failed to drop replication slot", "slot", slotName, "error", slotErr)
				} else {
					pi.logger.Info("Dropped replication slot", "slot", slotName)
				}
			} else {
				pi.logger.Warn("Failed to acquire connection for cleanup", "error", err)
			}
		} else if pool != nil {
			pi.logger.Info("Preserving replication slot (POSTGRES_INPUT_DROP_SLOT_ON_CLOSE=false)",
				"slot", slotName)
		}

		// Close pool
		if pool != nil {
			pool.Close()
		}
	})

	pi.logger.Info("PostgreSQL Input closed")
	return nil
}
