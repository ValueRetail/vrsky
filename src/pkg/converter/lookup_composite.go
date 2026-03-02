package converter

import (
	"context"
	"fmt"
	"sync"
)

// CompositeMetrics tracks composite backend operation metrics
type CompositeMetrics struct {
	totalLookups      int64
	successfulLookups int64
	failedLookups     int64
	backendHits       map[int]int64 // which backend succeeded
	mu                sync.Mutex
}

// CompositeBackend chains multiple backends with fallback strategy.
// Implements the LookupBackend interface for composable backend chains.
// Tries each backend in order until one returns a result.
// Gracefully handles failures and returns nil if all backends fail.
type CompositeBackend struct {
	backends         []LookupBackend
	backendNames     []string // For logging/metrics
	logger           Logger
	ctx              context.Context
	metricsCollector *CompositeMetrics
}

// NewCompositeBackend creates a new composite backend with the given chain of backends.
// Backends are tried in order - first successful backend returns.
// At least one backend must be provided.
func NewCompositeBackend(backends []LookupBackend, logger Logger) (*CompositeBackend, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("at least one backend must be provided")
	}

	// Generate names for each backend for logging
	names := make([]string, len(backends))
	for i := range backends {
		names[i] = fmt.Sprintf("backend_%d", i)
	}

	return &CompositeBackend{
		backends:     backends,
		backendNames: names,
		logger:       logger,
		ctx:          context.Background(),
		metricsCollector: &CompositeMetrics{
			backendHits: make(map[int]int64),
		},
	}, nil
}

// NewCompositeBackendWithNames creates a composite backend with custom backend names.
// Names are used for logging and metrics tracking.
func NewCompositeBackendWithNames(backends []LookupBackend, backendNames []string, logger Logger) (*CompositeBackend, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("at least one backend must be provided")
	}
	if len(backends) != len(backendNames) {
		return nil, fmt.Errorf("backend count (%d) must match names count (%d)", len(backends), len(backendNames))
	}

	return &CompositeBackend{
		backends:     backends,
		backendNames: backendNames,
		logger:       logger,
		ctx:          context.Background(),
		metricsCollector: &CompositeMetrics{
			backendHits: make(map[int]int64),
		},
	}, nil
}

// Lookup retrieves a value by trying each backend in order.
// Implements LookupBackend.Lookup().
// Strategy:
// 1. Try each backend in order
// 2. If backend returns a result (non-nil map), return immediately
// 3. If backend returns error, log warning and try next backend
// 4. If backend returns nil, try next backend
// 5. If all backends exhausted, return nil gracefully
func (cb *CompositeBackend) Lookup(ctx context.Context, table, field string, value interface{}) (map[string]interface{}, error) {
	cb.metricsCollector.mu.Lock()
	cb.metricsCollector.totalLookups++
	cb.metricsCollector.mu.Unlock()

	// Try each backend in order
	for i, backend := range cb.backends {
		if backend == nil {
			continue
		}

		// Try this backend
		result, err := backend.Lookup(ctx, table, field, value)
		if err != nil {
			// Continue to next backend on error
			continue
		}

		// If found (result is non-nil), return immediately
		if result != nil {
			cb.metricsCollector.mu.Lock()
			cb.metricsCollector.successfulLookups++
			cb.metricsCollector.backendHits[i]++
			cb.metricsCollector.mu.Unlock()

			cb.logger.InfoContext(ctx, "Composite backend lookup successful",
				"backend", cb.backendNames[i], "table", table, "field", field)
			return result, nil
		}

		// If error, log warning and try next backend
		if err != nil {
			cb.logger.WarnContext(ctx, "Backend lookup error, trying next",
				"backend", cb.backendNames[i], "table", table, "error", err.Error())
			continue
		}

		// If nil (not found), try next backend
		cb.logger.InfoContext(ctx, "Backend returned nil, trying next",
			"backend", cb.backendNames[i], "table", table, "field", field)
	}

	// All backends exhausted - return nil gracefully
	cb.metricsCollector.mu.Lock()
	cb.metricsCollector.failedLookups++
	cb.metricsCollector.mu.Unlock()

	cb.logger.InfoContext(ctx, "All backends exhausted for lookup",
		"table", table, "field", field)
	return nil, nil
}

// HTTPLookup delegates to the first backend that supports HTTP lookups.
// Implements LookupBackend.HTTPLookup().
// Tries each backend until one returns a non-nil result.
// If all return nil, returns nil gracefully.
func (cb *CompositeBackend) HTTPLookup(ctx context.Context, url string, params map[string]interface{}) (map[string]interface{}, error) {
	// Try each backend in order
	for i, backend := range cb.backends {
		if backend == nil {
			continue
		}

		// Try this backend
		result, err := backend.HTTPLookup(ctx, url, params)
		if err != nil {
			// Continue to next backend on error
			continue
		}

		// If found (result is non-nil), return immediately
		if result != nil {
			cb.metricsCollector.mu.Lock()
			cb.metricsCollector.successfulLookups++
			cb.metricsCollector.backendHits[i]++
			cb.metricsCollector.mu.Unlock()

			cb.logger.InfoContext(ctx, "Composite backend HTTP lookup successful",
				"backend", cb.backendNames[i], "url", url)
			return result, nil
		}

		// If error, log warning and try next backend
		if err != nil {
			cb.logger.WarnContext(ctx, "Backend HTTP lookup error, trying next",
				"backend", cb.backendNames[i], "url", url, "error", err.Error())
			continue
		}

		// If nil (not found), try next backend
		cb.logger.InfoContext(ctx, "Backend returned nil for HTTP lookup, trying next",
			"backend", cb.backendNames[i], "url", url)
	}

	// All backends exhausted - return nil gracefully
	cb.metricsCollector.mu.Lock()
	cb.metricsCollector.failedLookups++
	cb.metricsCollector.mu.Unlock()

	cb.logger.InfoContext(ctx, "All backends exhausted for HTTP lookup", "url", url)
	return nil, nil
}

// AddBackend appends a new backend to the chain.
// New backends are tried last (after existing backends).
func (cb *CompositeBackend) AddBackend(backend LookupBackend, name string) error {
	if backend == nil {
		return fmt.Errorf("backend cannot be nil")
	}
	if name == "" {
		name = fmt.Sprintf("backend_%d", len(cb.backends))
	}

	cb.backends = append(cb.backends, backend)
	cb.backendNames = append(cb.backendNames, name)

	cb.logger.InfoContext(cb.ctx, "Backend added to composite",
		"name", name, "position", len(cb.backends)-1)

	return nil
}

// GetBackendCount returns the number of backends in the chain.
func (cb *CompositeBackend) GetBackendCount() int {
	return len(cb.backends)
}

// GetMetrics returns accumulated metrics for monitoring.
func (cb *CompositeBackend) GetMetrics() CompositeMetrics {
	cb.metricsCollector.mu.Lock()
	defer cb.metricsCollector.mu.Unlock()

	return CompositeMetrics{
		totalLookups:      cb.metricsCollector.totalLookups,
		successfulLookups: cb.metricsCollector.successfulLookups,
		failedLookups:     cb.metricsCollector.failedLookups,
		backendHits:       cb.metricsCollector.backendHits,
	}
}

// GetBackendHitRate returns the hit rate for a specific backend.
// Returns (hits, total, hitRate) as (int64, int64, float64).
// Returns (0, 0, 0) if backend doesn't exist.
func (cb *CompositeBackend) GetBackendHitRate(backendIndex int) (int64, int64, float64) {
	cb.metricsCollector.mu.Lock()
	defer cb.metricsCollector.mu.Unlock()

	hits := cb.metricsCollector.backendHits[backendIndex]
	total := cb.metricsCollector.successfulLookups

	if total == 0 {
		return 0, 0, 0
	}

	hitRate := float64(hits) / float64(total)
	return hits, total, hitRate
}
