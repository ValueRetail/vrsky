package converter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFunctionCache_NewFunctionCache tests cache creation
func TestFunctionCache_NewFunctionCache(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()

	tests := []struct {
		name     string
		registry *FunctionRegistry
		logger   Logger
		wantErr  bool
	}{
		{
			name:     "valid registry and logger",
			registry: registry,
			logger:   logger,
			wantErr:  false,
		},
		{
			name:     "nil registry",
			registry: nil,
			logger:   logger,
			wantErr:  true,
		},
		{
			name:     "nil logger",
			registry: registry,
			logger:   nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache, err := NewFunctionCache(tt.registry, tt.logger)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, cache)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, cache)
			}
		})
	}
}

// TestFunctionCache_DefaultConfig tests default configuration
func TestFunctionCache_DefaultConfig(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()

	cache, err := NewFunctionCache(registry, logger)
	require.NoError(t, err)

	assert.Equal(t, 1*time.Hour, cache.defaultTTL)
	assert.Equal(t, 10000, cache.maxSize)
	assert.Equal(t, 0, cache.Size())
}

// TestFunctionCache_IsPureFunction tests pure function detection
func TestFunctionCache_IsPureFunction(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	tests := []struct {
		name     string
		funcName string
		isPure   bool
	}{
		{"sum is pure", "sum", true},
		{"avg is pure", "avg", true},
		{"concat is pure", "concat", true},
		{"now is not pure", "now", false},
		{"random is not pure", "random", false},
		{"unknown function", "unknown_func", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cache.IsPureFunction(tt.funcName)
			assert.Equal(t, tt.isPure, result)
		})
	}
}

// TestFunctionCache_CacheKeyGeneration tests deterministic key generation
func TestFunctionCache_CacheKeyGeneration(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	// Same inputs should produce same key
	key1 := cache.generateCacheKey("sum", []int{1, 2, 3})
	key2 := cache.generateCacheKey("sum", []int{1, 2, 3})
	assert.Equal(t, key1, key2)

	// Different inputs should produce different keys
	key3 := cache.generateCacheKey("sum", []int{1, 2, 4})
	assert.NotEqual(t, key1, key3)

	// Different function names should produce different keys
	key4 := cache.generateCacheKey("avg", []int{1, 2, 3})
	assert.NotEqual(t, key1, key4)
}

// TestFunctionCache_SetAndGet tests cache storage and retrieval
func TestFunctionCache_SetAndGet(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	// Set a value
	cache.Set("sum", int64(6), []int{1, 2, 3})

	// Get the value
	result, ok := cache.Get("sum", []int{1, 2, 3})
	assert.True(t, ok)
	assert.Equal(t, int64(6), result)

	// Get with different args should miss
	result, ok = cache.Get("sum", []int{1, 2, 4})
	assert.False(t, ok)
	assert.Nil(t, result)
}

// TestFunctionCache_GetNonPure tests that non-pure functions skip cache
func TestFunctionCache_GetNonPure(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	// Try to cache non-pure function
	cache.Set("now", time.Now())

	// Get should return false (not cached)
	result, ok := cache.Get("now")
	assert.False(t, ok)
	assert.Nil(t, result)
}

// TestFunctionCache_TTLExpiration tests cache expiration
func TestFunctionCache_TTLExpiration(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCacheWithConfig(registry, logger, 50*time.Millisecond, 1000)

	// Set value with short TTL
	cache.Set("sum", int64(6), []int{1, 2, 3})

	// Should be available immediately
	result, ok := cache.Get("sum", []int{1, 2, 3})
	assert.True(t, ok)
	assert.Equal(t, int64(6), result)

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should be expired
	result, ok = cache.Get("sum", []int{1, 2, 3})
	assert.False(t, ok)
	assert.Nil(t, result)
}

// TestFunctionCache_CustomTTL tests custom TTL per entry
func TestFunctionCache_CustomTTL(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCacheWithConfig(registry, logger, 1*time.Hour, 1000)

	// Set with custom short TTL
	cache.SetWithTTL("sum", int64(6), 50*time.Millisecond, []int{1, 2, 3})

	// Should be available immediately
	_, ok := cache.Get("sum", []int{1, 2, 3})
	assert.True(t, ok)

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should be expired
	_, ok = cache.Get("sum", []int{1, 2, 3})
	assert.False(t, ok)
}

// TestFunctionCache_CacheHitsAndMisses tests metrics tracking
func TestFunctionCache_CacheHitsAndMisses(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	// Set and retrieve multiple times
	cache.Set("sum", int64(6), []int{1, 2, 3})

	// First retrieval (hit)
	cache.Get("sum", []int{1, 2, 3})

	// Second retrieval (hit)
	cache.Get("sum", []int{1, 2, 3})

	// Miss
	cache.Get("sum", []int{1, 2, 4})

	metrics := cache.GetMetrics()
	assert.Equal(t, int64(2), metrics.hits)
	assert.Equal(t, int64(1), metrics.misses)
}

// TestFunctionCache_ConcurrentAccess tests thread safety
func TestFunctionCache_ConcurrentAccess(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	done := make(chan bool, 20)

	// Concurrent writes and reads
	for i := 0; i < 10; i++ {
		go func(id int) {
			cache.Set("sum", int64(id), []int{id})
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		go func(id int) {
			cache.Get("sum", []int{id})
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Should have completed without race conditions
	assert.True(t, cache.Size() > 0)
}

// TestFunctionCache_ClearCache tests cache clearing
func TestFunctionCache_ClearCache(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	// Add entries
	cache.Set("sum", int64(6), []int{1, 2, 3})
	cache.Set("avg", float64(2), []int{1, 2, 3})

	assert.Equal(t, 2, cache.Size())

	// Clear cache
	cache.ClearCache()

	assert.Equal(t, 0, cache.Size())

	// Entries should be gone
	result, ok := cache.Get("sum", []int{1, 2, 3})
	assert.False(t, ok)
	assert.Nil(t, result)
}

// TestFunctionCache_Cleanup tests expiration cleanup
func TestFunctionCache_Cleanup(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCacheWithConfig(registry, logger, 50*time.Millisecond, 1000)

	// Add entries
	cache.Set("sum", int64(6), []int{1, 2, 3})
	cache.Set("avg", float64(2), []int{1, 2, 3})

	assert.Equal(t, 2, cache.Size())

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Cleanup should remove expired entries
	cache.Cleanup()

	assert.Equal(t, 0, cache.Size())
}

// TestFunctionCache_Size tests size reporting
func TestFunctionCache_Size(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	assert.Equal(t, 0, cache.Size())

	cache.Set("sum", int64(6), []int{1, 2, 3})
	assert.Equal(t, 1, cache.Size())

	cache.Set("avg", float64(2), []int{1, 2, 3})
	assert.Equal(t, 2, cache.Size())

	cache.ClearCache()
	assert.Equal(t, 0, cache.Size())
}

// TestFunctionCache_MaxSizeEviction tests cache eviction at max size
func TestFunctionCache_MaxSizeEviction(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	// Small cache size for testing
	cache, _ := NewFunctionCacheWithConfig(registry, logger, 1*time.Hour, 5)

	// Add more entries than max size
	for i := 0; i < 10; i++ {
		cache.Set("sum", int64(i), []int{i})
	}

	// Cache should have evicted old entries
	// After eviction, size should be small (last few entries)
	metrics := cache.GetMetrics()
	assert.Greater(t, metrics.evictions, int64(0))
}

// TestFunctionCache_Call tests the Call method with caching
func TestFunctionCache_Call(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	// First call to "sum" function - will hit registry
	// Note: This assumes "sum" is registered in the function registry
	// Since it might not be for this test, we'll just verify caching works

	// For pure functions, subsequent calls with same args should use cache
	cache.Set("sum", int64(6), []int{1, 2, 3})

	result, err := cache.Call("sum", []int{1, 2, 3})
	assert.NoError(t, err)
	assert.Equal(t, int64(6), result)

	metrics := cache.GetMetrics()
	assert.Equal(t, int64(1), metrics.hits)
}

// TestFunctionCache_CallNonPure tests Call with non-pure functions
func TestFunctionCache_CallNonPure(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	// Non-pure functions should not be cached
	// They should skip cache and go directly to registry
	_, _ = cache.Call("now")

	metrics := cache.GetMetrics()
	// Should not have cache hits for non-pure functions
	assert.Equal(t, int64(0), metrics.hits)
}

// TestFunctionCache_MultipleArgTypes tests caching with various arg types
func TestFunctionCache_MultipleArgTypes(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCache(registry, logger)

	// Cache different types of arguments
	cache.Set("concat", "hello world", "hello", " ", "world")
	cache.Set("sum", int64(6), []int{1, 2, 3})
	cache.Set("avg", float64(2.5), []float64{1.0, 2.0, 3.0, 4.0})

	// Retrieve all
	result1, ok1 := cache.Get("concat", "hello", " ", "world")
	result2, ok2 := cache.Get("sum", []int{1, 2, 3})
	result3, ok3 := cache.Get("avg", []float64{1.0, 2.0, 3.0, 4.0})

	assert.True(t, ok1)
	assert.True(t, ok2)
	assert.True(t, ok3)

	assert.Equal(t, "hello world", result1)
	assert.Equal(t, int64(6), result2)
	assert.Equal(t, float64(2.5), result3)
}

// TestFunctionCache_GetMetrics tests comprehensive metrics
func TestFunctionCache_GetMetrics(t *testing.T) {
	registry := NewFunctionRegistry(context.Background(), newMockLogger())
	logger := newMockLogger()
	cache, _ := NewFunctionCacheWithConfig(registry, logger, 50*time.Millisecond, 10)

	// Generate various cache operations
	cache.Set("sum", int64(6), []int{1, 2, 3})
	cache.Get("sum", []int{1, 2, 3}) // hit
	cache.Get("sum", []int{1, 2, 3}) // hit
	cache.Get("sum", []int{1, 2, 4}) // miss

	// Add many entries to trigger eviction
	for i := 0; i < 15; i++ {
		cache.Set("avg", int64(i), []int{i})
	}

	metrics := cache.GetMetrics()
	assert.Equal(t, int64(2), metrics.hits)
	assert.Equal(t, int64(1), metrics.misses)
	assert.Greater(t, metrics.evictions, int64(0))
}

// TestFunctionCache_PureFunctionsMap tests complete pure functions list
func TestFunctionCache_PureFunctionsMap(t *testing.T) {
	expectedPure := []string{
		"sum", "avg", "count", "max", "min",
		"concat", "uppercase", "lowercase", "trim", "split", "replace",
		"multiply", "divide", "as_string", "as_number",
		"lookup", "http_lookup",
	}

	for _, fname := range expectedPure {
		assert.True(t, PureFunctions[fname], "expected %s to be in pure functions", fname)
	}

	expectedNonPure := []string{
		"now", "random", "uuid", "date_now", "date_today", "get_env",
	}

	for _, fname := range expectedNonPure {
		assert.True(t, NonPureFunctions[fname], "expected %s to be in non-pure functions", fname)
	}
}
