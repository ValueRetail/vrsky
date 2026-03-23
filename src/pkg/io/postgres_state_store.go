package io

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ============================================================================
// METRICS
// ============================================================================

var (
	stateStoreLoadDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "api_state_store_load_duration_seconds",
			Help:    "Duration of state store load operations",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"}, // "success", "not_found", "error"
	)

	stateStoreSaveDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "api_state_store_save_duration_seconds",
			Help:    "Duration of state store save operations",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"}, // "success", "error"
	)

	stateStoreErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_state_store_errors_total",
			Help: "Total number of state store errors",
		},
		[]string{"operation", "error_type"}, // operation: "load", "save"; error_type: "connection", "query", "marshal"
	)
)

// ============================================================================
// POSTGRES STATE STORE
// ============================================================================

// PostgresStateStore implements StateStore using PostgreSQL for persistence.
// It stores API consumer state in the api_consumer_state table, enabling
// consumers to resume from their last position after restarts.
type PostgresStateStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewPostgresStateStore creates a new PostgreSQL-backed state store.
// The db connection should be managed externally (opened and closed by caller).
func NewPostgresStateStore(db *sql.DB, logger *slog.Logger) (*PostgresStateStore, error) {
	if db == nil {
		return nil, errors.New("database connection is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PostgresStateStore{
		db:     db,
		logger: logger,
	}, nil
}

// Load retrieves state for a consumer from PostgreSQL.
// Returns nil, nil if no state exists (not an error condition).
// Returns error only on actual database failures.
func (s *PostgresStateStore) Load(ctx context.Context, consumerID string) (*apiInputState, error) {
	start := time.Now()

	query := `
		SELECT state_data, created_at, updated_at
		FROM api_consumer_state
		WHERE consumer_id = $1
	`

	var stateDataJSON []byte
	var createdAt, updatedAt time.Time

	err := s.db.QueryRowContext(ctx, query, consumerID).Scan(&stateDataJSON, &createdAt, &updatedAt)
	if err != nil {
		duration := time.Since(start).Seconds()

		if errors.Is(err, sql.ErrNoRows) {
			// Not found is not an error - return nil state
			stateStoreLoadDuration.WithLabelValues("not_found").Observe(duration)
			s.logger.Debug("no state found for consumer",
				"consumer_id", consumerID,
				"duration_ms", duration*1000,
			)
			return nil, nil
		}

		// Actual error
		stateStoreLoadDuration.WithLabelValues("error").Observe(duration)
		stateStoreErrorsTotal.WithLabelValues("load", "query").Inc()
		s.logger.Error("failed to load consumer state",
			"consumer_id", consumerID,
			"error", err,
		)
		return nil, fmt.Errorf("load api consumer state: %w", err)
	}

	// Unmarshal state from JSONB
	var state apiInputState
	if len(stateDataJSON) > 0 {
		if err := json.Unmarshal(stateDataJSON, &state); err != nil {
			duration := time.Since(start).Seconds()
			stateStoreLoadDuration.WithLabelValues("error").Observe(duration)
			stateStoreErrorsTotal.WithLabelValues("load", "unmarshal").Inc()
			s.logger.Error("failed to unmarshal state data",
				"consumer_id", consumerID,
				"error", err,
			)
			return nil, fmt.Errorf("unmarshal api consumer state: %w", err)
		}
	}

	// Set timestamps from database
	state.CreatedAt = createdAt
	state.UpdatedAt = updatedAt

	duration := time.Since(start).Seconds()
	stateStoreLoadDuration.WithLabelValues("success").Observe(duration)

	s.logger.Debug("loaded consumer state",
		"consumer_id", consumerID,
		"offset", state.Offset,
		"cursor", state.Cursor,
		"pagination_type", state.PaginationType,
		"duration_ms", duration*1000,
	)

	return &state, nil
}

// Save persists state for a consumer to PostgreSQL using upsert semantics.
// If state already exists for the consumer, it is updated; otherwise inserted.
func (s *PostgresStateStore) Save(ctx context.Context, consumerID string, state *apiInputState) error {
	start := time.Now()

	// Marshal state to JSONB
	stateDataJSON, err := json.Marshal(state)
	if err != nil {
		duration := time.Since(start).Seconds()
		stateStoreSaveDuration.WithLabelValues("error").Observe(duration)
		stateStoreErrorsTotal.WithLabelValues("save", "marshal").Inc()
		s.logger.Error("failed to marshal state data",
			"consumer_id", consumerID,
			"error", err,
		)
		return fmt.Errorf("marshal api consumer state: %w", err)
	}

	// Upsert query - insert or update on conflict
	query := `
		INSERT INTO api_consumer_state (
			consumer_id,
			state_data,
			total_polls,
			total_records_fetched,
			created_at,
			updated_at
		) VALUES ($1, $2, 1, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT (consumer_id) DO UPDATE SET
			state_data = EXCLUDED.state_data,
			total_polls = api_consumer_state.total_polls + 1,
			updated_at = CURRENT_TIMESTAMP
	`

	_, err = s.db.ExecContext(ctx, query, consumerID, stateDataJSON)
	if err != nil {
		duration := time.Since(start).Seconds()
		stateStoreSaveDuration.WithLabelValues("error").Observe(duration)
		stateStoreErrorsTotal.WithLabelValues("save", "query").Inc()
		s.logger.Error("failed to save consumer state",
			"consumer_id", consumerID,
			"error", err,
		)
		return fmt.Errorf("save api consumer state: %w", err)
	}

	duration := time.Since(start).Seconds()
	stateStoreSaveDuration.WithLabelValues("success").Observe(duration)

	s.logger.Debug("saved consumer state",
		"consumer_id", consumerID,
		"offset", state.Offset,
		"cursor", state.Cursor,
		"pagination_type", state.PaginationType,
		"duration_ms", duration*1000,
	)

	return nil
}

// SaveWithStats persists state and updates statistics counters.
// Use this when you want to track poll and record counts.
func (s *PostgresStateStore) SaveWithStats(ctx context.Context, consumerID string, state *apiInputState, recordsFetched int64) error {
	start := time.Now()

	// Marshal state to JSONB
	stateDataJSON, err := json.Marshal(state)
	if err != nil {
		duration := time.Since(start).Seconds()
		stateStoreSaveDuration.WithLabelValues("error").Observe(duration)
		stateStoreErrorsTotal.WithLabelValues("save", "marshal").Inc()
		return fmt.Errorf("marshal api consumer state: %w", err)
	}

	// Upsert with statistics update
	query := `
		INSERT INTO api_consumer_state (
			consumer_id,
			state_data,
			total_polls,
			total_records_fetched,
			created_at,
			updated_at
		) VALUES ($1, $2, 1, $3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT (consumer_id) DO UPDATE SET
			state_data = EXCLUDED.state_data,
			total_polls = api_consumer_state.total_polls + 1,
			total_records_fetched = api_consumer_state.total_records_fetched + EXCLUDED.total_records_fetched,
			updated_at = CURRENT_TIMESTAMP
	`

	_, err = s.db.ExecContext(ctx, query, consumerID, stateDataJSON, recordsFetched)
	if err != nil {
		duration := time.Since(start).Seconds()
		stateStoreSaveDuration.WithLabelValues("error").Observe(duration)
		stateStoreErrorsTotal.WithLabelValues("save", "query").Inc()
		return fmt.Errorf("save api consumer state with stats: %w", err)
	}

	duration := time.Since(start).Seconds()
	stateStoreSaveDuration.WithLabelValues("success").Observe(duration)

	s.logger.Debug("saved consumer state with stats",
		"consumer_id", consumerID,
		"records_fetched", recordsFetched,
		"duration_ms", duration*1000,
	)

	return nil
}

// SaveError records an error for a consumer without updating state.
// Useful for tracking error history for debugging.
func (s *PostgresStateStore) SaveError(ctx context.Context, consumerID string, errMsg string) error {
	query := `
		UPDATE api_consumer_state
		SET last_error = $2,
		    last_error_at = CURRENT_TIMESTAMP,
		    updated_at = CURRENT_TIMESTAMP
		WHERE consumer_id = $1
	`

	result, err := s.db.ExecContext(ctx, query, consumerID, errMsg)
	if err != nil {
		stateStoreErrorsTotal.WithLabelValues("save_error", "query").Inc()
		return fmt.Errorf("save api consumer error: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		// Consumer doesn't exist yet, insert with error
		insertQuery := `
			INSERT INTO api_consumer_state (
				consumer_id,
				state_data,
				last_error,
				last_error_at,
				created_at,
				updated_at
			) VALUES ($1, '{}'::jsonb, $2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		`
		_, err = s.db.ExecContext(ctx, insertQuery, consumerID, errMsg)
		if err != nil {
			stateStoreErrorsTotal.WithLabelValues("save_error", "query").Inc()
			return fmt.Errorf("insert api consumer error: %w", err)
		}
	}

	return nil
}

// Delete removes state for a consumer.
// Returns nil if consumer doesn't exist (idempotent).
func (s *PostgresStateStore) Delete(ctx context.Context, consumerID string) error {
	query := `DELETE FROM api_consumer_state WHERE consumer_id = $1`

	_, err := s.db.ExecContext(ctx, query, consumerID)
	if err != nil {
		stateStoreErrorsTotal.WithLabelValues("delete", "query").Inc()
		return fmt.Errorf("delete api consumer state: %w", err)
	}

	s.logger.Debug("deleted consumer state", "consumer_id", consumerID)
	return nil
}

// GetStats retrieves statistics for a consumer without the full state.
// Returns total_polls and total_records_fetched.
func (s *PostgresStateStore) GetStats(ctx context.Context, consumerID string) (totalPolls, totalRecordsFetched int64, err error) {
	query := `
		SELECT total_polls, total_records_fetched
		FROM api_consumer_state
		WHERE consumer_id = $1
	`

	err = s.db.QueryRowContext(ctx, query, consumerID).Scan(&totalPolls, &totalRecordsFetched)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("get api consumer stats: %w", err)
	}

	return totalPolls, totalRecordsFetched, nil
}

// Ping verifies the database connection is alive.
func (s *PostgresStateStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
