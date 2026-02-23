package converter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHTTPMockLogger creates a new mock logger for HTTP tests
func newHTTPMockLogger() Logger {
	return &mockLogger{logs: make([]map[string]interface{}, 0)}
}

// TestHTTPLookupBackend_NewHTTPLookupBackend tests backend creation
func TestHTTPLookupBackend_NewHTTPLookupBackend(t *testing.T) {
	tests := []struct {
		name    string
		logger  Logger
		wantErr bool
	}{
		{
			name:    "nil logger",
			logger:  nil,
			wantErr: true,
		},
		{
			name:    "valid logger",
			logger:  newHTTPMockLogger(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, err := NewHTTPLookupBackend(tt.logger)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, backend)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, backend)
			}
		})
	}
}

// TestHTTPLookupBackend_DefaultConfig tests default configuration
func TestHTTPLookupBackend_DefaultConfig(t *testing.T) {
	config := DefaultHTTPConfig()

	assert.Equal(t, 10*time.Second, config.Timeout)
	assert.Equal(t, 3, config.MaxRetries)
	assert.Equal(t, 5*time.Minute, config.CacheTTL)
	assert.Equal(t, 5, config.CircuitBreakerThreshold)
	assert.Equal(t, 1*time.Minute, config.CircuitBreakerTimeout)
	assert.Equal(t, 1000, config.MaxCacheSize)
}

// TestHTTPLookupBackend_SuccessfulRequest tests successful HTTP request
func TestHTTPLookupBackend_SuccessfulRequest(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "123",
			"name":  "Test",
			"value": 42,
		})
	}))
	defer server.Close()

	ctx := context.Background()
	result, err := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "123", result["id"])
	assert.Equal(t, "Test", result["name"])

	metrics := backend.GetMetrics()
	assert.Equal(t, int64(1), metrics.requestsTotal)
	assert.Equal(t, int64(1), metrics.requestsSucceeded)
}

// TestHTTPLookupBackend_CachingWorks tests response caching
func TestHTTPLookupBackend_CachingWorks(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"call": callCount})
	}))
	defer server.Close()

	ctx := context.Background()

	// First call - should hit server
	result1, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	assert.Equal(t, float64(1), result1["call"])

	// Second call - should hit cache
	result2, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	assert.Equal(t, float64(1), result2["call"]) // Same result from cache

	metrics := backend.GetMetrics()
	assert.Equal(t, int64(1), metrics.cacheMisses)
	assert.Equal(t, int64(1), metrics.cacheHits)
}

// TestHTTPLookupBackend_RetryLogic tests exponential backoff retry
func TestHTTPLookupBackend_RetryLogic(t *testing.T) {
	logger := newHTTPMockLogger()
	config := DefaultHTTPConfig()
	config.MaxRetries = 2
	backend, err := NewHTTPLookupBackendWithConfig(config, logger)
	require.NoError(t, err)

	attemptCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount < 3 {
			// Fail first 2 attempts
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Succeed on 3rd attempt
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	}))
	defer server.Close()

	ctx := context.Background()
	result, err := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})

	// Should succeed after retries
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, true, result["success"])

	metrics := backend.GetMetrics()
	assert.Equal(t, int64(2), metrics.retries) // 2 retries (3 total attempts)
}

// TestHTTPLookupBackend_CircuitBreaker tests circuit breaker pattern
func TestHTTPLookupBackend_CircuitBreaker(t *testing.T) {
	logger := newHTTPMockLogger()
	config := DefaultHTTPConfig()
	config.CircuitBreakerThreshold = 2
	config.CircuitBreakerTimeout = 100 * time.Millisecond
	backend, err := NewHTTPLookupBackendWithConfig(config, logger)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := context.Background()

	// Make requests to trigger circuit breaker
	// First 2 requests fail
	for i := 0; i < 2; i++ {
		backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	}

	// Circuit should be open now
	assert.Equal(t, CircuitBreakerOpen, backend.GetCircuitBreakerState())

	// Next request should fail immediately without retry
	result, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	assert.Nil(t, result)

	metrics := backend.GetMetrics()
	assert.Greater(t, metrics.circuitBreakerOpen, int64(0))
}

// TestHTTPLookupBackend_Lookup tests that Lookup returns nil
func TestHTTPLookupBackend_Lookup(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := backend.Lookup(ctx, "table", "field", "value")

	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestHTTPLookupBackend_EmptyURL tests with empty URL
func TestHTTPLookupBackend_EmptyURL(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := backend.HTTPLookup(ctx, "", map[string]interface{}{})

	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestHTTPLookupBackend_InvalidURL tests with invalid URL
func TestHTTPLookupBackend_InvalidURL(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := backend.HTTPLookup(ctx, "not a valid url", map[string]interface{}{})

	// Should gracefully return nil instead of error
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestHTTPLookupBackend_HTTPError tests handling of HTTP errors
func TestHTTPLookupBackend_HTTPError(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ctx := context.Background()
	result, err := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})

	// Should gracefully return nil
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestHTTPLookupBackend_InvalidJSON tests handling of invalid JSON response
func TestHTTPLookupBackend_InvalidJSON(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	ctx := context.Background()
	result, err := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})

	// Should gracefully return nil
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// TestHTTPLookupBackend_CacheKeyGeneration tests deterministic cache keys
func TestHTTPLookupBackend_CacheKeyGeneration(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	url := "http://api.example.com"
	params := map[string]interface{}{"key": "value"}

	key1 := backend.generateCacheKey(url, params)
	key2 := backend.generateCacheKey(url, params)

	// Same inputs should produce same key
	assert.Equal(t, key1, key2)

	// Different inputs should produce different keys
	key3 := backend.generateCacheKey(url, map[string]interface{}{"key": "different"})
	assert.NotEqual(t, key1, key3)
}

// TestHTTPLookupBackend_ConcurrentRequests tests concurrent access
func TestHTTPLookupBackend_ConcurrentRequests(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "123"})
	}))
	defer server.Close()

	ctx := context.Background()
	done := make(chan bool, 10)

	// Run concurrent requests
	for i := 0; i < 10; i++ {
		go func(id int) {
			result, err := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{"id": id})
			assert.NoError(t, err)
			assert.NotNil(t, result)
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	metrics := backend.GetMetrics()
	assert.Greater(t, metrics.requestsSucceeded, int64(0))
}

// TestHTTPLookupBackend_ClearCache tests cache clearing
func TestHTTPLookupBackend_ClearCache(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "123"})
	}))
	defer server.Close()

	ctx := context.Background()

	// First request caches result
	backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})

	// Clear cache
	backend.ClearCache()

	// Verify cache is empty by checking metrics
	initialMetrics := backend.GetMetrics()
	initialCacheHits := initialMetrics.cacheHits

	// Next request should miss cache
	backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})

	finalMetrics := backend.GetMetrics()
	// Cache should have missed (new request increments misses)
	assert.Equal(t, initialCacheHits, finalMetrics.cacheHits)
}

// TestHTTPLookupBackend_CustomConfig tests custom configuration
func TestHTTPLookupBackend_CustomConfig(t *testing.T) {
	logger := newHTTPMockLogger()
	config := HTTPConfig{
		Timeout:                 5 * time.Second,
		MaxRetries:              2,
		CacheTTL:                1 * time.Minute,
		CircuitBreakerThreshold: 3,
		CircuitBreakerTimeout:   30 * time.Second,
		MaxCacheSize:            500,
	}

	backend, err := NewHTTPLookupBackendWithConfig(config, logger)
	require.NoError(t, err)
	assert.NotNil(t, backend)
	assert.Equal(t, config.Timeout, backend.config.Timeout)
}

// TestHTTPLookupBackend_WithParams tests HTTP requests with params
func TestHTTPLookupBackend_WithParams(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	paramsReceived := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.RawQuery) > 0 {
			paramsReceived = true
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "123"})
	}))
	defer server.Close()

	ctx := context.Background()
	params := map[string]interface{}{"from": "USD", "to": "EUR"}
	result, err := backend.HTTPLookup(ctx, server.URL, params)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, paramsReceived)
}

// TestHTTPLookupBackend_InterfaceImplementation tests interface compliance
func TestHTTPLookupBackend_InterfaceImplementation(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	// Verify it implements LookupBackend interface
	var _ LookupBackend = backend
}

// TestHTTPLookupBackend_MetricsTracking tests comprehensive metrics
func TestHTTPLookupBackend_MetricsTracking(t *testing.T) {
	logger := newHTTPMockLogger()
	config := DefaultHTTPConfig()
	config.MaxRetries = 1
	backend, err := NewHTTPLookupBackendWithConfig(config, logger)
	require.NoError(t, err)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "123"})
	}))
	defer server.Close()

	ctx := context.Background()
	backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})

	metrics := backend.GetMetrics()
	assert.Equal(t, int64(2), metrics.requestsTotal)       // 1 initial + 1 retry
	assert.Equal(t, int64(1), metrics.requestsSucceeded)   // Succeeded after retry
	assert.Equal(t, int64(1), metrics.retries)              // 1 retry happened
	assert.Equal(t, int64(1), metrics.cacheMisses)          // Cache miss on first try
}

// TestHTTPLookupBackend_CacheExpiration tests cache TTL
func TestHTTPLookupBackend_CacheExpiration(t *testing.T) {
	logger := newHTTPMockLogger()
	config := DefaultHTTPConfig()
	config.CacheTTL = 100 * time.Millisecond // Very short TTL for testing
	backend, err := NewHTTPLookupBackendWithConfig(config, logger)
	require.NoError(t, err)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"call": callCount})
	}))
	defer server.Close()

	ctx := context.Background()

	// First call
	result1, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	assert.Equal(t, float64(1), result1["call"])

	// Second call immediately (should hit cache)
	result2, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	assert.Equal(t, float64(1), result2["call"])

	// Wait for cache to expire
	time.Sleep(150 * time.Millisecond)

	// Third call (cache expired, should miss)
	result3, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	assert.Equal(t, float64(2), result3["call"]) // New call made
}

// TestHTTPLookupBackend_ContextCancellation tests context cancellation
func TestHTTPLookupBackend_ContextCancellation(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(5 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "123"})
	}))
	defer server.Close()

	// Create context that cancels immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})

	// Should handle cancellation gracefully
	assert.Nil(t, result)
}

// TestHTTPLookupBackend_CircuitBreakerRecovery tests circuit breaker recovery
func TestHTTPLookupBackend_CircuitBreakerRecovery(t *testing.T) {
	logger := newHTTPMockLogger()
	config := DefaultHTTPConfig()
	config.CircuitBreakerThreshold = 1
	config.CircuitBreakerTimeout = 50 * time.Millisecond
	backend, err := NewHTTPLookupBackendWithConfig(config, logger)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := context.Background()

	// Make one failing request to trigger circuit breaker
	backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	assert.Equal(t, CircuitBreakerOpen, backend.GetCircuitBreakerState())

	// Wait for recovery timeout
	time.Sleep(100 * time.Millisecond)

	// Attempt a request during recovery - should still fail due to server error
	// But the circuit should transition through half-open
	backend.HTTPLookup(ctx, server.URL, map[string]interface{}{})
	
	// After failed recovery attempt, state should be open again
	assert.Equal(t, CircuitBreakerOpen, backend.GetCircuitBreakerState())
}

// TestHTTPLookupBackend_DifferentParamsCacheSeparately tests cache separation
func TestHTTPLookupBackend_DifferentParamsCacheSeparately(t *testing.T) {
	logger := newHTTPMockLogger()
	backend, err := NewHTTPLookupBackend(logger)
	require.NoError(t, err)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"call": callCount})
	}))
	defer server.Close()

	ctx := context.Background()

	// Call with different params
	result1, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{"x": 1})
	assert.Equal(t, float64(1), result1["call"])

	result2, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{"x": 2})
	assert.Equal(t, float64(2), result2["call"]) // Different cache key

	// Repeat first params (should hit cache)
	result3, _ := backend.HTTPLookup(ctx, server.URL, map[string]interface{}{"x": 1})
	assert.Equal(t, float64(1), result3["call"])

	metrics := backend.GetMetrics()
	assert.Equal(t, int64(1), metrics.cacheHits)   // Only one cache hit (result3 hit cache from result1)
	assert.Equal(t, int64(2), metrics.cacheMisses) // Two cache misses (result1 and result2)
}
