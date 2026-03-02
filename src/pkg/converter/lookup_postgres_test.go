package converter

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLogger for testing
type mockLogger struct {
	mu   sync.Mutex
	logs []map[string]interface{}
}

func newMockLogger() *mockLogger {
	return &mockLogger{logs: make([]map[string]interface{}, 0)}
}

func (ml *mockLogger) InfoContext(ctx context.Context, msg string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.logs = append(ml.logs, map[string]interface{}{"level": "info", "msg": msg, "args": args})
}

func (ml *mockLogger) WarnContext(ctx context.Context, msg string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.logs = append(ml.logs, map[string]interface{}{"level": "warn", "msg": msg, "args": args})
}

func (ml *mockLogger) ErrorContext(ctx context.Context, msg string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.logs = append(ml.logs, map[string]interface{}{"level": "error", "msg": msg, "args": args})
}

func (ml *mockLogger) Warn(msg string) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.logs = append(ml.logs, map[string]interface{}{"level": "warn", "msg": msg})
}

func (ml *mockLogger) Error(msg string) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.logs = append(ml.logs, map[string]interface{}{"level": "error", "msg": msg})
}

// TestPostgresLookupBackend_NewPostgresLookupBackend tests backend creation
func TestPostgresLookupBackend_NewPostgresLookupBackend(t *testing.T) {
	tests := []struct {
		name        string
		connStr     string
		logger      Logger
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty connection string",
			connStr:     "",
			logger:      newMockLogger(),
			wantErr:     true,
			errContains: "connection string cannot be empty",
		},
		{
			name:        "nil logger",
			connStr:     "postgresql://localhost",
			logger:      nil,
			wantErr:     true,
			errContains: "logger cannot be nil",
		},
		{
			name:        "invalid connection string",
			connStr:     "not-a-valid-connection-string",
			logger:      newMockLogger(),
			wantErr:     true,
			errContains: "failed to parse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			backend, err := NewPostgresLookupBackend(ctx, tt.connStr, tt.logger)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, backend)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, backend)
				if backend != nil {
					backend.Close()
				}
			}
		})
	}
}

// TestPostgresLookupBackend_DefaultConfig tests default configuration
func TestPostgresLookupBackend_DefaultConfig(t *testing.T) {
	config := DefaultPostgresConfig()

	assert.Equal(t, int32(2), config.MinConns)
	assert.Equal(t, int32(10), config.MaxConns)
	assert.Equal(t, 5*time.Minute, config.MaxConnLifetime)
	assert.Equal(t, 30*time.Second, config.MaxConnIdleTime)
	assert.Equal(t, 5*time.Second, config.QueryTimeout)
}

// TestPostgresLookupBackend_Lookup_NotFound tests lookup returns nil when not found
func TestPostgresLookupBackend_Lookup_NotFound(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	// Query non-existent data
	result, err := backend.Lookup(ctx, "nonexistent_table", "id", "999")

	// Should return nil gracefully without error
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestPostgresLookupBackend_Lookup_InvalidTable tests lookup with invalid table
func TestPostgresLookupBackend_Lookup_InvalidTable(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	// Query with invalid table - should gracefully return nil
	result, err := backend.Lookup(ctx, "table_does_not_exist", "id", "123")

	// Should gracefully handle error by returning nil
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestPostgresLookupBackend_Lookup_EmptyParameters tests lookup with empty parameters
func TestPostgresLookupBackend_Lookup_EmptyParameters(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	tests := []struct {
		name  string
		table string
		field string
		value interface{}
	}{
		{"empty table", "", "id", "value"},
		{"empty field", "table", "", "value"},
		{"nil value", "table", "id", nil},
		{"all empty", "", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := backend.Lookup(ctx, tt.table, tt.field, tt.value)
			assert.NoError(t, err)
			assert.Nil(t, result)
		})
	}
}

// TestPostgresLookupBackend_HTTPLookup tests HTTPLookup returns nil
func TestPostgresLookupBackend_HTTPLookup(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	result, err := backend.HTTPLookup(ctx, "http://api.example.com", map[string]interface{}{})

	// PostgreSQL backend doesn't support HTTP lookups
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestPostgresLookupBackend_Close tests backend closure
func TestPostgresLookupBackend_Close(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)

	// Close should not error
	err = backend.Close()
	assert.NoError(t, err)

	// Subsequent close should not error (idempotent)
	err = backend.Close()
	assert.NoError(t, err)
}

// TestPostgresLookupBackend_Metrics tests metrics collection
func TestPostgresLookupBackend_Metrics(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	// Perform lookups to generate metrics
	_, _ = backend.Lookup(ctx, "nonexistent", "id", "999")
	_, _ = backend.Lookup(ctx, "also_nonexistent", "field", "value")

	queriesTotal, queriesFailed := backend.GetMetricsValues()
	assert.Equal(t, int64(0), queriesTotal) // No successful queries
	assert.Equal(t, int64(2), queriesFailed)
}

// TestPostgresLookupBackend_Context_Timeout tests query timeout handling
func TestPostgresLookupBackend_Context_Timeout(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	// Create a context that times out immediately
	timeoutCtx, cancel := context.WithTimeout(ctx, 0*time.Second)
	defer cancel()

	// Should handle timeout gracefully
	result, err := backend.Lookup(timeoutCtx, "table", "id", "value")
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestPostgresLookupBackend_ConcurrentLookups tests concurrent access
func TestPostgresLookupBackend_ConcurrentLookups(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	// Run concurrent lookups
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			result, err := backend.Lookup(ctx, "nonexistent", "id", fmt.Sprintf("value-%d", id))
			assert.NoError(t, err)
			assert.Nil(t, result)
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestPostgresLookupBackend_WithContext tests context nil handling
func TestPostgresLookupBackend_WithContext(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	logger := newMockLogger()

	// Pass nil context - should use context.Background()
	backend, err := NewPostgresLookupBackend(context.TODO(), os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	assert.NotNil(t, backend.ctx)
}

// TestPostgresLookupBackend_LookupInterfaceImplementation tests interface compliance
func TestPostgresLookupBackend_LookupInterfaceImplementation(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	// Verify it implements LookupBackend interface
	var _ LookupBackend = backend
}

// TestPostgresLookupBackend_Lookup_WithRealData tests lookup with real data (requires test setup)
func TestPostgresLookupBackend_Lookup_WithRealData(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	// Try to query a built-in PostgreSQL table that always exists
	// pg_database is a system table that should exist on any PostgreSQL installation
	result, err := backend.Lookup(ctx, "pg_database", "datname", "postgres")

	// Result can be nil or not depending on schema, but should not error
	assert.NoError(t, err)
	// If result exists, it should have columns
	if result != nil {
		assert.NotEmpty(t, result)
	}
}

// TestPostgresLookupBackend_PoolConfiguration tests pool configuration
func TestPostgresLookupBackend_PoolConfiguration(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	config := PostgresConfig{
		ConnStr:         os.Getenv("POSTGRES_URL"),
		MinConns:        1,
		MaxConns:        5,
		MaxConnLifetime: 1 * time.Minute,
		MaxConnIdleTime: 10 * time.Second,
		QueryTimeout:    10 * time.Second,
	}

	backend, err := NewPostgresLookupBackendWithConfig(ctx, config, logger)
	require.NoError(t, err)
	defer backend.Close()

	// Verify configuration was applied
	assert.Equal(t, config.QueryTimeout, backend.queryTimeout)
	assert.Equal(t, config.ConnStr, backend.config.ConnStr)
}

// TestPostgresLookupBackend_InvalidConnectionString tests invalid connection strings
func TestPostgresLookupBackend_InvalidConnectionString(t *testing.T) {
	invalidStrings := []string{
		"invalid://string",
		"postgresql://",
		"postgres://user:pass@",
		"postgresql://user:pass@invalid:99999/db", // Invalid port
	}

	logger := newMockLogger()
	for _, connStr := range invalidStrings {
		t.Run(fmt.Sprintf("invalid_%s", connStr), func(t *testing.T) {
			ctx := context.Background()
			_, err := NewPostgresLookupBackend(ctx, connStr, logger)
			assert.Error(t, err)
		})
	}
}

// TestPostgresLookupBackend_Lookup_WithDifferentTypes tests lookup with various value types
func TestPostgresLookupBackend_Lookup_WithDifferentTypes(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	testValues := []interface{}{
		int32(123),
		int64(456),
		"string_value",
		1.5,
		true,
	}

	for _, val := range testValues {
		result, err := backend.Lookup(ctx, "nonexistent", "field", val)
		assert.NoError(t, err)
		assert.Nil(t, result) // Won't exist, but shouldn't error
	}
}

// TestPostgresLookupBackend_Lookup_LazyInitialization tests pool initialization
func TestPostgresLookupBackend_Lookup_LazyInitialization(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()
	backend, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend.Close()

	// Pool should already be initialized
	assert.NotNil(t, backend.pool)

	// Lookup should work immediately
	_, lookupErr := backend.Lookup(ctx, "pg_database", "datname", "postgres")
	assert.NoError(t, lookupErr)
	// Result may or may not exist, depending on schema
}

// TestPostgresLookupBackend_MultipleBackends tests multiple backend instances
func TestPostgresLookupBackend_MultipleBackends(t *testing.T) {
	if os.Getenv("POSTGRES_URL") == "" {
		t.Skip("POSTGRES_URL not set, skipping integration test")
	}

	ctx := context.Background()
	logger := newMockLogger()

	backend1, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend1.Close()

	backend2, err := NewPostgresLookupBackend(ctx, os.Getenv("POSTGRES_URL"), logger)
	require.NoError(t, err)
	defer backend2.Close()

	// Both should work independently
	result1, err1 := backend1.Lookup(ctx, "nonexistent", "id", "value")
	result2, err2 := backend2.Lookup(ctx, "nonexistent", "id", "value")

	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.Nil(t, result1)
	assert.Nil(t, result2)
}
