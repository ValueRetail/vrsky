package converter

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
)

// PostgresMetrics tracks PostgreSQL operation metrics with thread-safe atomic operations
type PostgresMetrics struct {
	queriesTotal  atomic.Int64
	queriesFailed atomic.Int64
}

// PostgresLookupBackend provides production-grade database lookups with connection pooling.
// Implements the LookupBackend interface for real PostgreSQL queries.
type PostgresLookupBackend struct {
	pool             *pgxpool.Pool
	logger           Logger
	ctx              context.Context
	config           PostgresConfig
	metricsCollector *PostgresMetrics
	queryTimeout     time.Duration
}

// PostgresConfig holds PostgreSQL configuration
type PostgresConfig struct {
	// Connection string (e.g., "postgresql://user:pass@localhost:5432/dbname")
	ConnStr string

	// Connection pool settings
	MinConns        int32
	MaxConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration

	// Query settings
	QueryTimeout time.Duration
}

// DefaultPostgresConfig returns sensible defaults for PostgreSQL connection pool
func DefaultPostgresConfig() PostgresConfig {
	return PostgresConfig{
		MinConns:        2,
		MaxConns:        10,
		MaxConnLifetime: 5 * time.Minute,
		MaxConnIdleTime: 30 * time.Second,
		QueryTimeout:    5 * time.Second,
	}
}

// NewPostgresLookupBackend creates a new PostgreSQL lookup backend with connection pooling.
// Requires a valid PostgreSQL connection string.
// Returns error if connection pool fails to initialize.
func NewPostgresLookupBackend(ctx context.Context, connStr string, logger Logger) (*PostgresLookupBackend, error) {
	if connStr == "" {
		return nil, fmt.Errorf("PostgreSQL connection string cannot be empty")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	config := DefaultPostgresConfig()
	config.ConnStr = connStr
	return NewPostgresLookupBackendWithConfig(ctx, config, logger)
}

// NewPostgresLookupBackendWithConfig creates a new PostgreSQL lookup backend with custom configuration.
func NewPostgresLookupBackendWithConfig(ctx context.Context, config PostgresConfig, logger Logger) (*PostgresLookupBackend, error) {
	if config.ConnStr == "" {
		return nil, fmt.Errorf("PostgreSQL connection string cannot be empty")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Create pgxpool configuration
	poolConfig, err := pgxpool.ParseConfig(config.ConnStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PostgreSQL connection string: %w", err)
	}

	// Apply connection pool settings
	poolConfig.MinConns = config.MinConns
	poolConfig.MaxConns = config.MaxConns
	poolConfig.MaxConnLifetime = config.MaxConnLifetime
	poolConfig.MaxConnIdleTime = config.MaxConnIdleTime

	// Create pool
	pool, err := pgxpool.ConnectConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL connection pool: %w", err)
	}

	// Test connection
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(testCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	backend := &PostgresLookupBackend{
		pool:             pool,
		logger:           logger,
		ctx:              ctx,
		config:           config,
		metricsCollector: &PostgresMetrics{},
		queryTimeout:     config.QueryTimeout,
	}

	logger.InfoContext(ctx, "PostgreSQL lookup backend initialized", "min_conns", config.MinConns, "max_conns", config.MaxConns)
	return backend, nil
}

// Lookup retrieves a row from PostgreSQL by table, field, and value.
// Implements LookupBackend.Lookup().
// Returns a map of column names to values, or nil if not found.
// Gracefully returns nil (with warning) on errors instead of throwing exceptions.
// Uses parameterized identifiers to prevent SQL injection.
func (pb *PostgresLookupBackend) Lookup(ctx context.Context, table, field string, value interface{}) (map[string]interface{}, error) {
	if table == "" || field == "" || value == nil {
		return nil, nil
	}

	// Create context with timeout
	queryCtx, cancel := context.WithTimeout(ctx, pb.queryTimeout)
	defer cancel()

	// Build parameterized query using pgx.Identifier for safe table/field names
	// pgx.Identifier properly escapes SQL identifiers to prevent injection
	// Query: SELECT * FROM table WHERE field = $1 LIMIT 1
	query := fmt.Sprintf("SELECT * FROM %s WHERE %s = $1 LIMIT 1",
		pgx.Identifier{table}.Sanitize(),
		pgx.Identifier{field}.Sanitize())

	// Execute query
	rows, err := pb.pool.Query(queryCtx, query, value)
	if err != nil {
		pb.logger.WarnContext(ctx, "PostgreSQL query error",
			"table", table, "field", field, "value", value, "error", err.Error())
		pb.metricsCollector.queriesFailed.Add(1)
		return nil, nil // Graceful degradation: return nil, not error
	}
	defer rows.Close()

	// Parse first row into map
	output := make(map[string]interface{})
	if rows.Next() {
		// Get column descriptions
		columnDescriptions := rows.FieldDescriptions()
		values := make([]interface{}, len(columnDescriptions))
		valuePtrs := make([]interface{}, len(columnDescriptions))

		// Create pointers for Scan
		for i := range columnDescriptions {
			valuePtrs[i] = &values[i]
		}

		// Scan row values
		if err := rows.Scan(valuePtrs...); err != nil {
			pb.logger.WarnContext(ctx, "PostgreSQL row parsing error",
				"table", table, "field", field, "error", err.Error())
			return nil, nil // Graceful degradation
		}

		// Build output map
		for i, col := range columnDescriptions {
			output[string(col.Name)] = values[i]
		}
	} else {
		// Not found is not an error - return nil gracefully
		return nil, nil
	}

	pb.metricsCollector.queriesTotal.Add(1)
	pb.logger.InfoContext(ctx, "PostgreSQL lookup successful",
		"table", table, "field", field, "columns", len(output))

	return output, nil
}

// HTTPLookup is not implemented for PostgreSQL backend.
// Use HTTPLookupBackend for HTTP API calls.
func (pb *PostgresLookupBackend) HTTPLookup(ctx context.Context, url string, params map[string]interface{}) (map[string]interface{}, error) {
	pb.logger.WarnContext(ctx, "HTTPLookup called on PostgreSQL backend - use HTTPLookupBackend instead")
	return nil, nil
}

// Close closes the PostgreSQL connection pool.
// Should be called to release resources.
func (pb *PostgresLookupBackend) Close() error {
	if pb.pool != nil {
		pb.pool.Close()
		pb.logger.InfoContext(pb.ctx, "PostgreSQL lookup backend closed")
	}
	return nil
}

// GetMetricsValues returns the metrics values (not the atomic types themselves to avoid copy locks).
func (pb *PostgresLookupBackend) GetMetricsValues() (queriesTotal, queriesFailed int64) {
	return pb.metricsCollector.queriesTotal.Load(), pb.metricsCollector.queriesFailed.Load()
}
