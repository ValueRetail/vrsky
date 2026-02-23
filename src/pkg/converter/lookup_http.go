package converter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// HTTPMetrics tracks HTTP operation metrics
type HTTPMetrics struct {
	requestsTotal      int64
	requestsSucceeded  int64
	requestsFailed     int64
	cacheHits          int64
	cacheMisses        int64
	retries            int64
	circuitBreakerOpen int64
}

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState string

const (
	CircuitBreakerClosed   CircuitBreakerState = "closed"
	CircuitBreakerOpen     CircuitBreakerState = "open"
	CircuitBreakerHalfOpen CircuitBreakerState = "half_open"
)

// CircuitBreaker tracks endpoint health and prevents cascading failures
type CircuitBreaker struct {
	state            CircuitBreakerState
	failureCount     int
	failureThreshold int
	lastFailureTime  time.Time
	recoveryTimeout  time.Duration
	mu               sync.Mutex
}

// NewCircuitBreaker creates a new circuit breaker with configured thresholds
func NewCircuitBreaker(failureThreshold int, recoveryTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:            CircuitBreakerClosed,
		failureThreshold: failureThreshold,
		recoveryTimeout:  recoveryTimeout,
	}
}

// RecordSuccess resets the circuit breaker on successful request
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0
	if cb.state == CircuitBreakerHalfOpen {
		cb.state = CircuitBreakerClosed
	}
}

// RecordFailure increments failure count and opens circuit if threshold exceeded
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	if cb.failureCount >= cb.failureThreshold {
		cb.state = CircuitBreakerOpen
	}
}

// CanAttempt checks if request can be attempted (respects circuit breaker state)
func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == CircuitBreakerClosed {
		return true
	}

	if cb.state == CircuitBreakerOpen {
		// Check if recovery timeout has passed
		if time.Since(cb.lastFailureTime) > cb.recoveryTimeout {
			cb.state = CircuitBreakerHalfOpen
			cb.failureCount = 0 // Reset failure count when entering half-open
			return true
		}
		return false
	}

	// Half-open state: allow one attempt
	return true
}

// GetState returns the current circuit breaker state
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ResponseCache stores HTTP responses with TTL
type ResponseCache struct {
	cache   map[string]*CachedResponse
	mu      sync.RWMutex
	maxSize int
}

// CachedResponse holds response data with expiration time
type CachedResponse struct {
	Data      map[string]interface{}
	ExpiresAt time.Time
}

// NewResponseCache creates a new response cache with size limit
func NewResponseCache(maxSize int) *ResponseCache {
	return &ResponseCache{
		cache:   make(map[string]*CachedResponse),
		maxSize: maxSize,
	}
}

// Get retrieves a cached response if not expired
func (rc *ResponseCache) Get(key string) (map[string]interface{}, bool) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	cached, exists := rc.cache[key]
	if !exists {
		return nil, false
	}

	if time.Now().After(cached.ExpiresAt) {
		// Expired - will be cleaned up elsewhere
		return nil, false
	}

	return cached.Data, true
}

// Set stores a response with TTL
func (rc *ResponseCache) Set(key string, data map[string]interface{}, ttl time.Duration) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Simple eviction: clear cache if at max size (not LRU for simplicity)
	if len(rc.cache) >= rc.maxSize {
		// Clear oldest entries (all for simplicity)
		rc.cache = make(map[string]*CachedResponse)
	}

	rc.cache[key] = &CachedResponse{
		Data:      data,
		ExpiresAt: time.Now().Add(ttl),
	}
}

// Cleanup removes expired entries
func (rc *ResponseCache) Cleanup() {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	now := time.Now()
	for key, cached := range rc.cache {
		if now.After(cached.ExpiresAt) {
			delete(rc.cache, key)
		}
	}
}

// HTTPLookupBackend provides resilient HTTP API lookups with retry, caching, and circuit breaker
type HTTPLookupBackend struct {
	client           *http.Client
	cache            *ResponseCache
	circuitBreaker   *CircuitBreaker
	logger           Logger
	ctx              context.Context
	config           HTTPConfig
	metricsCollector *HTTPMetrics
	backoffDurations []time.Duration // Pre-computed exponential backoff durations
	mu               sync.Mutex
}

// HTTPConfig holds HTTP backend configuration
type HTTPConfig struct {
	// Timeout for individual HTTP requests (default: 10s)
	Timeout time.Duration

	// MaxRetries is the maximum number of retry attempts (default: 3)
	MaxRetries int

	// CacheTTL is the time-to-live for cached responses (default: 5 minutes)
	CacheTTL time.Duration

	// CircuitBreakerThreshold is the failure count to open circuit (default: 5)
	CircuitBreakerThreshold int

	// CircuitBreakerTimeout is the time before attempting recovery (default: 1 minute)
	CircuitBreakerTimeout time.Duration

	// MaxCacheSize is the maximum number of cached responses (default: 1000)
	MaxCacheSize int
}

// DefaultHTTPConfig returns sensible defaults for HTTP backend
func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		Timeout:                 10 * time.Second,
		MaxRetries:              3,
		CacheTTL:                5 * time.Minute,
		CircuitBreakerThreshold: 5,
		CircuitBreakerTimeout:   1 * time.Minute,
		MaxCacheSize:            1000,
	}
}

// NewHTTPLookupBackend creates a new HTTP lookup backend with default configuration
func NewHTTPLookupBackend(logger Logger) (*HTTPLookupBackend, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	return NewHTTPLookupBackendWithConfig(DefaultHTTPConfig(), logger)
}

// NewHTTPLookupBackendWithConfig creates a new HTTP lookup backend with custom configuration
func NewHTTPLookupBackendWithConfig(config HTTPConfig, logger Logger) (*HTTPLookupBackend, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	if config.MaxRetries < 0 {
		config.MaxRetries = 0
	}
	if config.CircuitBreakerThreshold <= 0 {
		config.CircuitBreakerThreshold = 5
	}
	if config.MaxCacheSize <= 0 {
		config.MaxCacheSize = 1000
	}

	// Pre-compute backoff durations: 0s, 1s, 2s, 4s, 8s, 16s... (exponential)
	backoffDurations := make([]time.Duration, config.MaxRetries+1)
	for i := 0; i <= config.MaxRetries; i++ {
		if i == 0 {
			backoffDurations[i] = 0 // First attempt: no backoff
		} else {
			// Exponential backoff: 1s, 2s, 4s, 8s, 16s...
			backoffDurations[i] = time.Duration(1<<uint(i-1)) * time.Second
		}
	}

	backend := &HTTPLookupBackend{
		client: &http.Client{
			Timeout: config.Timeout,
		},
		cache:            NewResponseCache(config.MaxCacheSize),
		circuitBreaker:   NewCircuitBreaker(config.CircuitBreakerThreshold, config.CircuitBreakerTimeout),
		logger:           logger,
		ctx:              context.Background(),
		config:           config,
		metricsCollector: &HTTPMetrics{},
		backoffDurations: backoffDurations,
	}

	logger.InfoContext(backend.ctx, "HTTP lookup backend initialized",
		"timeout", config.Timeout, "max_retries", config.MaxRetries, "cache_ttl", config.CacheTTL)
	return backend, nil
}

// Lookup is not implemented for HTTP backend
// Use HTTPLookup instead
func (hb *HTTPLookupBackend) Lookup(ctx context.Context, table, field string, value interface{}) (map[string]interface{}, error) {
	hb.logger.WarnContext(ctx, "Lookup called on HTTP backend - use HTTPLookup instead")
	return nil, nil
}

// HTTPLookup performs an HTTP request to fetch data with retry logic and caching
// Implements LookupBackend.HTTPLookup()
func (hb *HTTPLookupBackend) HTTPLookup(ctx context.Context, url string, params map[string]interface{}) (map[string]interface{}, error) {
	if url == "" {
		return nil, nil
	}

	// Check circuit breaker
	if !hb.circuitBreaker.CanAttempt() {
		hb.metricsCollector.circuitBreakerOpen++
		hb.logger.WarnContext(ctx, "Circuit breaker open, rejecting request", "url", url)
		return nil, nil
	}

	// Generate cache key from URL and params
	cacheKey := hb.generateCacheKey(url, params)

	// Check cache first
	if cached, ok := hb.cache.Get(cacheKey); ok {
		hb.metricsCollector.cacheHits++
		hb.logger.InfoContext(ctx, "HTTP lookup cache hit", "url", url)
		return cached, nil
	}
	hb.metricsCollector.cacheMisses++

	for attempt := 0; attempt <= hb.config.MaxRetries; attempt++ {
		// Wait before retry (except first attempt)
		if attempt > 0 {
			waitTime := hb.backoffDurations[attempt]
			hb.logger.InfoContext(ctx, "HTTP lookup retry",
				"url", url, "attempt", attempt, "wait_ms", waitTime.Milliseconds())
			hb.metricsCollector.retries++

			select {
			case <-time.After(waitTime):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		// Make request
		result, err := hb.makeRequest(ctx, url, params)
		hb.metricsCollector.requestsTotal++

		if err == nil && result != nil {
			// Success
			hb.metricsCollector.requestsSucceeded++
			hb.circuitBreaker.RecordSuccess()

			// Cache result
			hb.cache.Set(cacheKey, result, hb.config.CacheTTL)

			hb.logger.InfoContext(ctx, "HTTP lookup successful",
				"url", url, "attempt", attempt+1)
			return result, nil
		}

		if err != nil {
			hb.circuitBreaker.RecordFailure()
			hb.logger.WarnContext(ctx, "HTTP lookup failed",
				"url", url, "attempt", attempt+1, "error", err.Error())
		}
	}

	// All retries exhausted
	hb.metricsCollector.requestsFailed++
	hb.logger.WarnContext(ctx, "HTTP lookup failed after all retries",
		"url", url, "max_retries", hb.config.MaxRetries)
	return nil, nil // Graceful degradation
}

// makeRequest performs a single HTTP request
func (hb *HTTPLookupBackend) makeRequest(ctx context.Context, url string, params map[string]interface{}) (map[string]interface{}, error) {
	// Create base request
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Build URL-encoded query string from params
	if len(params) > 0 {
		q := req.URL.Query()
		for k, v := range params {
			q.Set(k, fmt.Sprint(v))
		}
		req.URL.RawQuery = q.Encode()
	}
	resp, err := hb.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result, nil
}

// generateCacheKey creates a deterministic cache key from URL and params
func (hb *HTTPLookupBackend) generateCacheKey(url string, params map[string]interface{}) string {
	// Create consistent JSON representation
	paramsJSON, _ := json.Marshal(params)
	data := url + string(paramsJSON)

	// SHA256 hash for collision-resistant key
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", hash)
}

// ClearCache empties the response cache
func (hb *HTTPLookupBackend) ClearCache() {
	hb.cache = NewResponseCache(hb.config.MaxCacheSize)
	hb.logger.InfoContext(hb.ctx, "HTTP response cache cleared")
}

// CleanupCache removes expired entries
func (hb *HTTPLookupBackend) CleanupCache() {
	hb.cache.Cleanup()
}

// GetMetrics returns accumulated metrics for monitoring
func (hb *HTTPLookupBackend) GetMetrics() HTTPMetrics {
	hb.mu.Lock()
	defer hb.mu.Unlock()

	return HTTPMetrics{
		requestsTotal:      hb.metricsCollector.requestsTotal,
		requestsSucceeded:  hb.metricsCollector.requestsSucceeded,
		requestsFailed:     hb.metricsCollector.requestsFailed,
		cacheHits:          hb.metricsCollector.cacheHits,
		cacheMisses:        hb.metricsCollector.cacheMisses,
		retries:            hb.metricsCollector.retries,
		circuitBreakerOpen: hb.metricsCollector.circuitBreakerOpen,
	}
}

// GetCircuitBreakerState returns the current circuit breaker state
func (hb *HTTPLookupBackend) GetCircuitBreakerState() CircuitBreakerState {
	return hb.circuitBreaker.GetState()
}
