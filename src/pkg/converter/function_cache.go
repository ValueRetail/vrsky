package converter

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// CacheMetrics tracks caching performance metrics
type CacheMetrics struct {
	hits             int64
	misses           int64
	evictions        int64
	expirations      int64
	totalCacheHits   int64
	totalCacheMisses int64
}

// FunctionCacheEntry holds a cached function result with expiration
type FunctionCacheEntry struct {
	Result    interface{}
	ExpiresAt time.Time
}

// FunctionCache provides result caching for pure functions
// This improves performance for repeated function calls with same arguments
type FunctionCache struct {
	registry    *FunctionRegistry
	cache       map[string]*FunctionCacheEntry
	cacheLock   sync.RWMutex
	defaultTTL  time.Duration
	maxSize     int
	logger      Logger
	metrics     *CacheMetrics
	metricsLock sync.RWMutex
}

// PureFunctions defines which functions can be cached
// Pure functions always return the same result for the same inputs
var PureFunctions = map[string]bool{
	"sum":         true,
	"avg":         true,
	"count":       true,
	"max":         true,
	"min":         true,
	"concat":      true,
	"uppercase":   true,
	"lowercase":   true,
	"trim":        true,
	"split":       true,
	"replace":     true,
	"multiply":    true,
	"divide":      true,
	"as_string":   true,
	"as_number":   true,
	"lookup":      true,
	"http_lookup": true,
}

// NonPureFunctions defines functions that should never be cached
var NonPureFunctions = map[string]bool{
	"now":        true, // Changes every call
	"random":     true, // Non-deterministic
	"uuid":       true, // Generates new values
	"date_now":   true, // Current timestamp
	"date_today": true, // Current date
	"get_env":    true, // Environment-dependent
}

// NewFunctionCache creates a new function cache wrapping a registry
func NewFunctionCache(registry *FunctionRegistry, logger Logger) (*FunctionCache, error) {
	if registry == nil {
		return nil, fmt.Errorf("registry cannot be nil")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	return &FunctionCache{
		registry:   registry,
		cache:      make(map[string]*FunctionCacheEntry),
		defaultTTL: 1 * time.Hour,
		maxSize:    10000,
		logger:     logger,
		metrics:    &CacheMetrics{},
	}, nil
}

// NewFunctionCacheWithConfig creates a cache with custom configuration
func NewFunctionCacheWithConfig(registry *FunctionRegistry, logger Logger, defaultTTL time.Duration, maxSize int) (*FunctionCache, error) {
	if registry == nil {
		return nil, fmt.Errorf("registry cannot be nil")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if defaultTTL <= 0 {
		defaultTTL = 1 * time.Hour
	}
	if maxSize <= 0 {
		maxSize = 10000
	}

	return &FunctionCache{
		registry:   registry,
		cache:      make(map[string]*FunctionCacheEntry),
		defaultTTL: defaultTTL,
		maxSize:    maxSize,
		logger:     logger,
		metrics:    &CacheMetrics{},
	}, nil
}

// IsPureFunction checks if a function is pure and cacheable
func (fc *FunctionCache) IsPureFunction(funcName string) bool {
	// Check if explicitly marked as non-pure
	if NonPureFunctions[funcName] {
		return false
	}

	// Check if explicitly marked as pure
	return PureFunctions[funcName]
}

// generateCacheKey creates a deterministic cache key from function name and args
func (fc *FunctionCache) generateCacheKey(funcName string, args ...interface{}) string {
	// Create JSON representation of args
	argsJSON, _ := json.Marshal(args)
	data := funcName + string(argsJSON)

	// SHA256 hash for collision-resistant key
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", hash)
}

// Get retrieves a cached function result if available and not expired
func (fc *FunctionCache) Get(funcName string, args ...interface{}) (interface{}, bool) {
	if !fc.IsPureFunction(funcName) {
		return nil, false
	}

	cacheKey := fc.generateCacheKey(funcName, args...)

	fc.cacheLock.RLock()
	entry, exists := fc.cache[cacheKey]
	fc.cacheLock.RUnlock()

	if !exists {
		fc.metricsLock.Lock()
		fc.metrics.misses++
		fc.metricsLock.Unlock()
		return nil, false
	}

	// Check if expired
	if time.Now().After(entry.ExpiresAt) {
		fc.metricsLock.Lock()
		fc.metrics.expirations++
		fc.metricsLock.Unlock()
		return nil, false
	}

	fc.metricsLock.Lock()
	fc.metrics.hits++
	fc.metricsLock.Unlock()
	return entry.Result, true
}

// Set caches a function result with default TTL
func (fc *FunctionCache) Set(funcName string, result interface{}, args ...interface{}) {
	if !fc.IsPureFunction(funcName) {
		return
	}

	fc.SetWithTTL(funcName, result, fc.defaultTTL, args...)
}

// SetWithTTL caches a function result with custom TTL
func (fc *FunctionCache) SetWithTTL(funcName string, result interface{}, ttl time.Duration, args ...interface{}) {
	if !fc.IsPureFunction(funcName) {
		return
	}

	if ttl <= 0 {
		ttl = fc.defaultTTL
	}

	cacheKey := fc.generateCacheKey(funcName, args...)

	fc.cacheLock.Lock()
	defer fc.cacheLock.Unlock()

	// Enforce max size with O(1) eviction of an arbitrary entry if at capacity.
	if len(fc.cache) >= fc.maxSize {
		for key := range fc.cache {
			delete(fc.cache, key)
			fc.metricsLock.Lock()
			fc.metrics.evictions++
			fc.metricsLock.Unlock()
			break
		}
	}

	now := time.Now()
	fc.cache[cacheKey] = &FunctionCacheEntry{
		Result:    result,
		ExpiresAt: now.Add(ttl),
	}
}

// Cleanup removes expired entries from cache
func (fc *FunctionCache) Cleanup() {
	fc.cacheLock.Lock()
	defer fc.cacheLock.Unlock()

	now := time.Now()
	removed := 0
	for key, entry := range fc.cache {
		if now.After(entry.ExpiresAt) {
			delete(fc.cache, key)
			removed++
		}
	}

	if removed > 0 {
		fc.metricsLock.Lock()
		fc.metrics.expirations += int64(removed)
		fc.metricsLock.Unlock()
	}
}

// ClearCache empties the entire cache
func (fc *FunctionCache) ClearCache() {
	fc.cacheLock.Lock()
	defer fc.cacheLock.Unlock()

	fc.cache = make(map[string]*FunctionCacheEntry)
	fc.logger.InfoContext(fc.registry.ctx, "Function cache cleared")
}

// GetMetrics returns current cache metrics
func (fc *FunctionCache) GetMetrics() CacheMetrics {
	fc.cacheLock.RLock()
	defer fc.cacheLock.RUnlock()

	return CacheMetrics{
		hits:             fc.metrics.hits,
		misses:           fc.metrics.misses,
		evictions:        fc.metrics.evictions,
		expirations:      fc.metrics.expirations,
		totalCacheHits:   fc.metrics.hits,
		totalCacheMisses: fc.metrics.misses,
	}
}

// Size returns the current number of entries in cache
func (fc *FunctionCache) Size() int {
	fc.cacheLock.RLock()
	defer fc.cacheLock.RUnlock()

	return len(fc.cache)
}

// Call executes a function with caching for pure functions
// This is a convenience method that checks cache, calls function if miss, and caches result
func (fc *FunctionCache) Call(funcName string, args ...interface{}) (interface{}, error) {
	// For pure functions, check cache first
	if fc.IsPureFunction(funcName) {
		if result, ok := fc.Get(funcName, args...); ok {
			return result, nil
		}
	}

	// Call through registry
	result, err := fc.registry.Call(funcName, args...)

	// Cache result if successful and function is pure
	if err == nil && fc.IsPureFunction(funcName) {
		fc.Set(funcName, result, args...)
	}

	return result, err
}
