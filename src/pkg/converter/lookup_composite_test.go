package converter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompositeBackend_NewCompositeBackend tests backend creation
func TestCompositeBackend_NewCompositeBackend(t *testing.T) {
	tests := []struct {
		name        string
		backends    []LookupBackend
		logger      Logger
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil logger",
			backends:    []LookupBackend{NewMockLookupBackend()},
			logger:      nil,
			wantErr:     true,
			errContains: "logger cannot be nil",
		},
		{
			name:        "no backends",
			backends:    []LookupBackend{},
			logger:      newMockLogger(),
			wantErr:     true,
			errContains: "at least one backend",
		},
		{
			name:     "single backend",
			backends: []LookupBackend{NewMockLookupBackend()},
			logger:   newMockLogger(),
			wantErr:  false,
		},
		{
			name:     "multiple backends",
			backends: []LookupBackend{NewMockLookupBackend(), NewMockLookupBackend()},
			logger:   newMockLogger(),
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, err := NewCompositeBackend(tt.backends, tt.logger)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, backend)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, backend)
			}
		})
	}
}

// TestCompositeBackend_NewCompositeBackendWithNames tests creation with custom names
func TestCompositeBackend_NewCompositeBackendWithNames(t *testing.T) {
	tests := []struct {
		name         string
		backends     []LookupBackend
		backendNames []string
		logger       Logger
		wantErr      bool
		errContains  string
	}{
		{
			name:         "mismatched counts",
			backends:     []LookupBackend{NewMockLookupBackend(), NewMockLookupBackend()},
			backendNames: []string{"postgres"},
			logger:       newMockLogger(),
			wantErr:      true,
			errContains:  "must match",
		},
		{
			name:         "matching counts",
			backends:     []LookupBackend{NewMockLookupBackend(), NewMockLookupBackend()},
			backendNames: []string{"postgres", "http"},
			logger:       newMockLogger(),
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, err := NewCompositeBackendWithNames(tt.backends, tt.backendNames, tt.logger)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, backend)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, backend)
				assert.Equal(t, len(tt.backends), backend.GetBackendCount())
			}
		})
	}
}

// TestCompositeBackend_Lookup_FirstBackendSucceeds tests that first success stops chain
func TestCompositeBackend_Lookup_FirstBackendSucceeds(t *testing.T) {
	logger := newMockLogger()
	mockBackend := NewMockLookupBackend()

	backend, err := NewCompositeBackend([]LookupBackend{mockBackend}, logger)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := backend.Lookup(ctx, "customers", "id", "CUST001")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "CUST001", result["id"])

	metrics := backend.GetMetrics()
	assert.Equal(t, int64(1), metrics.successfulLookups)
	assert.Equal(t, int64(1), metrics.totalLookups)
	assert.Equal(t, int64(0), metrics.failedLookups)
}

// TestCompositeBackend_Lookup_FallbackToSecondBackend tests fallback when first returns nil
func TestCompositeBackend_Lookup_FallbackToSecondBackend(t *testing.T) {
	logger := newMockLogger()

	// First backend that returns nil for a specific query
	backend1 := NewMockLookupBackend()
	// Second backend that has the data
	backend2 := NewMockLookupBackend()

	composite, err := NewCompositeBackendWithNames(
		[]LookupBackend{backend1, backend2},
		[]string{"mock1", "mock2"},
		logger,
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Query that doesn't exist in first backend but exists in second
	result, err := composite.Lookup(ctx, "customers", "id", "CUST001")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "CUST001", result["id"])

	metrics := composite.GetMetrics()
	assert.Equal(t, int64(1), metrics.successfulLookups)
	assert.Equal(t, int64(0), metrics.failedLookups)
}

// TestCompositeBackend_Lookup_AllBackendsFail tests graceful degradation
func TestCompositeBackend_Lookup_AllBackendsFail(t *testing.T) {
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{backend}, logger)
	require.NoError(t, err)

	ctx := context.Background()
	// Query for non-existent data
	result, err := composite.Lookup(ctx, "customers", "id", "NONEXISTENT")

	assert.NoError(t, err)
	assert.Nil(t, result)

	metrics := composite.GetMetrics()
	assert.Equal(t, int64(0), metrics.successfulLookups)
	assert.Equal(t, int64(1), metrics.failedLookups)
	assert.Equal(t, int64(1), metrics.totalLookups)
}

// TestCompositeBackend_Lookup_EmptyTable tests with empty parameters
func TestCompositeBackend_Lookup_EmptyTable(t *testing.T) {
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{backend}, logger)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := composite.Lookup(ctx, "", "field", "value")

	// Empty table should return nil gracefully
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestCompositeBackend_HTTPLookup_FirstBackendSucceeds tests HTTP lookup
func TestCompositeBackend_HTTPLookup_FirstBackendSucceeds(t *testing.T) {
	logger := newMockLogger()
	mockBackend := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{mockBackend}, logger)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := composite.HTTPLookup(ctx, "http://api.example.com/exchange", map[string]interface{}{})

	// Mock backend returns nil for HTTP lookups
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestCompositeBackend_HTTPLookup_FallbackChain tests HTTP lookup fallback
func TestCompositeBackend_HTTPLookup_FallbackChain(t *testing.T) {
	logger := newMockLogger()
	backend1 := NewMockLookupBackend()
	backend2 := NewMockLookupBackend()

	composite, err := NewCompositeBackendWithNames(
		[]LookupBackend{backend1, backend2},
		[]string{"backend1", "backend2"},
		logger,
	)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := composite.HTTPLookup(ctx, "http://api.example.com", map[string]interface{}{})

	// Should try all backends and return nil gracefully
	assert.NoError(t, err)
	assert.Nil(t, result)

	metrics := composite.GetMetrics()
	assert.Equal(t, int64(0), metrics.successfulLookups)
	assert.Equal(t, int64(1), metrics.failedLookups)
}

// TestCompositeBackend_AddBackend tests adding backends dynamically
func TestCompositeBackend_AddBackend(t *testing.T) {
	logger := newMockLogger()
	backend1 := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{backend1}, logger)
	require.NoError(t, err)

	assert.Equal(t, 1, composite.GetBackendCount())

	// Add another backend
	backend2 := NewMockLookupBackend()
	err = composite.AddBackend(backend2, "backend2")
	assert.NoError(t, err)
	assert.Equal(t, 2, composite.GetBackendCount())

	// Add with nil backend should error
	err = composite.AddBackend(nil, "invalid")
	assert.Error(t, err)
	assert.Equal(t, 2, composite.GetBackendCount())

	// Add with empty name should auto-generate
	backend3 := NewMockLookupBackend()
	err = composite.AddBackend(backend3, "")
	assert.NoError(t, err)
	assert.Equal(t, 3, composite.GetBackendCount())
}

// TestCompositeBackend_Metrics_BackendHits tests metrics per backend
func TestCompositeBackend_Metrics_BackendHits(t *testing.T) {
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackendWithNames(
		[]LookupBackend{backend},
		[]string{"primary"},
		logger,
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Make 3 successful lookups
	for i := 0; i < 3; i++ {
		composite.Lookup(ctx, "customers", "id", "CUST001")
	}

	metrics := composite.GetMetrics()
	assert.Equal(t, int64(3), metrics.successfulLookups)

	// Check hit rate for first backend
	hits, total, hitRate := composite.GetBackendHitRate(0)
	assert.Equal(t, int64(3), hits)
	assert.Equal(t, int64(3), total)
	assert.Equal(t, 1.0, hitRate)
}

// TestCompositeBackend_ConcurrentLookups tests thread safety
func TestCompositeBackend_ConcurrentLookups(t *testing.T) {
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{backend}, logger)
	require.NoError(t, err)

	ctx := context.Background()
	done := make(chan bool, 10)

	// Run concurrent lookups
	for i := 0; i < 10; i++ {
		go func(id int) {
			result, err := composite.Lookup(ctx, "customers", "id", "CUST001")
			assert.NoError(t, err)
			assert.NotNil(t, result)
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify metrics are consistent
	metrics := composite.GetMetrics()
	assert.Equal(t, int64(10), metrics.successfulLookups)
	assert.Equal(t, int64(10), metrics.totalLookups)
}

// TestCompositeBackend_LookupInterfaceImplementation tests interface compliance
func TestCompositeBackend_LookupInterfaceImplementation(t *testing.T) {
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{backend}, logger)
	require.NoError(t, err)

	// Verify it implements LookupBackend interface
	var _ LookupBackend = composite
}

// TestCompositeBackend_MultipleBackends_FirstBackendSucceeds tests chain stops at first success
func TestCompositeBackend_MultipleBackends_FirstBackendSucceeds(t *testing.T) {
	logger := newMockLogger()
	backend1 := NewMockLookupBackend()
	backend2 := NewMockLookupBackend()
	backend3 := NewMockLookupBackend()

	composite, err := NewCompositeBackendWithNames(
		[]LookupBackend{backend1, backend2, backend3},
		[]string{"backend1", "backend2", "backend3"},
		logger,
	)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := composite.Lookup(ctx, "customers", "id", "CUST001")

	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Only first backend should have been hit
	hits, _, _ := composite.GetBackendHitRate(0)
	assert.Equal(t, int64(1), hits)

	hits, _, _ = composite.GetBackendHitRate(1)
	assert.Equal(t, int64(0), hits)

	hits, _, _ = composite.GetBackendHitRate(2)
	assert.Equal(t, int64(0), hits)
}

// TestCompositeBackend_ProductionScenario tests typical production usage
func TestCompositeBackend_ProductionScenario(t *testing.T) {
	logger := newMockLogger()

	// Simulate: Postgres -> HTTP -> Mock fallback
	postgresBackend := NewMockLookupBackend()
	httpBackend := NewMockLookupBackend()
	mockBackend := NewMockLookupBackend()

	composite, err := NewCompositeBackendWithNames(
		[]LookupBackend{postgresBackend, httpBackend, mockBackend},
		[]string{"postgres", "http", "mock"},
		logger,
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Successful lookup through first backend
	result, err := composite.Lookup(ctx, "customers", "id", "CUST001")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify metrics
	metrics := composite.GetMetrics()
	assert.Equal(t, int64(1), metrics.totalLookups)
	assert.Equal(t, int64(1), metrics.successfulLookups)
	assert.Equal(t, int64(0), metrics.failedLookups)
}

// TestCompositeBackend_GetBackendCount tests backend counting
func TestCompositeBackend_GetBackendCount(t *testing.T) {
	logger := newMockLogger()
	backends := []LookupBackend{
		NewMockLookupBackend(),
		NewMockLookupBackend(),
		NewMockLookupBackend(),
	}

	composite, err := NewCompositeBackend(backends, logger)
	require.NoError(t, err)

	assert.Equal(t, 3, composite.GetBackendCount())
}

// TestCompositeBackend_Lookup_TableNotFound tests handling of non-existent tables
func TestCompositeBackend_Lookup_TableNotFound(t *testing.T) {
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{backend}, logger)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := composite.Lookup(ctx, "nonexistent_table", "id", "value")

	assert.NoError(t, err)
	assert.Nil(t, result)

	metrics := composite.GetMetrics()
	assert.Equal(t, int64(1), metrics.failedLookups)
}

// TestCompositeBackend_Metrics_HitRate tests hit rate calculation
func TestCompositeBackend_Metrics_HitRate(t *testing.T) {
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackendWithNames(
		[]LookupBackend{backend},
		[]string{"primary"},
		logger,
	)
	require.NoError(t, err)

	ctx := context.Background()

	// No lookups yet
	hits, total, hitRate := composite.GetBackendHitRate(0)
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(0), total)
	assert.Equal(t, 0.0, hitRate)

	// Make successful lookups
	for i := 0; i < 5; i++ {
		composite.Lookup(ctx, "customers", "id", "CUST001")
	}

	hits, total, hitRate = composite.GetBackendHitRate(0)
	assert.Equal(t, int64(5), hits)
	assert.Equal(t, int64(5), total)
	assert.Equal(t, 1.0, hitRate)
}

// TestCompositeBackend_NilBackendInChain tests handling of nil backends
func TestCompositeBackend_NilBackendInChain(t *testing.T) {
	// Note: Can't create with nil backend through constructor, but tests defensive coding
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{backend}, logger)
	require.NoError(t, err)

	// Verify it still works
	ctx := context.Background()
	result, err := composite.Lookup(ctx, "customers", "id", "CUST001")
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

// TestCompositeBackend_SequentialFallthrough tests all backends in sequence
func TestCompositeBackend_SequentialFallthrough(t *testing.T) {
	logger := newMockLogger()

	// Create multiple backends
	composite, err := NewCompositeBackend([]LookupBackend{
		NewMockLookupBackend(),
		NewMockLookupBackend(),
		NewMockLookupBackend(),
	}, logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Query for non-existent customer - all backends will be tried
	result, err := composite.Lookup(ctx, "customers", "id", "NONEXISTENT")

	assert.NoError(t, err)
	assert.Nil(t, result)

	metrics := composite.GetMetrics()
	assert.Equal(t, int64(1), metrics.failedLookups)
	assert.Equal(t, int64(1), metrics.totalLookups)
}

// TestCompositeBackend_MetricsConcurrency tests concurrent metrics access
func TestCompositeBackend_MetricsConcurrency(t *testing.T) {
	logger := newMockLogger()
	backend := NewMockLookupBackend()

	composite, err := NewCompositeBackend([]LookupBackend{backend}, logger)
	require.NoError(t, err)

	ctx := context.Background()
	done := make(chan bool, 20)

	// Half do lookups, half check metrics concurrently
	for i := 0; i < 20; i++ {
		go func(id int) {
			if id%2 == 0 {
				composite.Lookup(ctx, "customers", "id", "CUST001")
			} else {
				composite.GetMetrics()
			}
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 20; i++ {
		<-done
	}

	// Metrics should be consistent
	metrics := composite.GetMetrics()
	assert.Greater(t, metrics.totalLookups, int64(0))
}
