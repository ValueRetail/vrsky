package io

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// rng is a per-package random number generator seeded at init time
// Using a separate RNG avoids the use of global state and
// ensures different sequences across restarts.
// Access is protected by rngMu to prevent data races from concurrent calls.
var (
	rng   *rand.Rand
	rngMu sync.Mutex // Protects rng.Float64() calls for thread-safe concurrent access
)

func init() {
	// Seed the RNG with current time for non-deterministic jitter
	rng = rand.New(rand.NewSource(time.Now().UnixNano()))
}

// BackoffConfig holds exponential backoff configuration
type BackoffConfig struct {
	InitialDuration  time.Duration
	MaxDuration      time.Duration
	Multiplier       float64
	JitterPercentage float64 // ±jitter around calculated backoff (e.g., 10 = ±10%)
}

// DefaultBackoffConfig returns recommended backoff settings
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialDuration:  1 * time.Second,
		MaxDuration:      30 * time.Second,
		Multiplier:       1.5,
		JitterPercentage: 10,
	}
}

// CalculateBackoff computes exponential backoff duration for attempt N
// attempt is 1-indexed (first retry = attempt 1, second = attempt 2, etc.)
// Returns: min(maxDuration, initialDuration * multiplier^(attempt-1)) ± jitter
func CalculateBackoff(attempt int, config BackoffConfig) time.Duration {
	if attempt <= 0 {
		return 0
	}

	// Prevent integer overflow by capping exponent
	// At attempt 40, 1.5^40 ≈ 1.4e8, which is huge; cap at 35 to be safe
	if attempt > 35 {
		return config.MaxDuration
	}

	// Calculate base backoff: initialDuration * multiplier^(attempt-1)
	exponent := float64(attempt - 1)
	baseBackoff := float64(config.InitialDuration) * math.Pow(config.Multiplier, exponent)

	// Cap at max duration
	if baseBackoff > float64(config.MaxDuration) {
		baseBackoff = float64(config.MaxDuration)
	}

	// Add jitter: ±JitterPercentage around baseBackoff
	// Use per-package RNG with mutex protection for thread-safe concurrent access
	jitterFraction := config.JitterPercentage / 100.0
	maxJitter := baseBackoff * jitterFraction
	
	rngMu.Lock()
	jitter := (rng.Float64()*2 - 1) * maxJitter // Random in [-maxJitter, +maxJitter]
	rngMu.Unlock()
	
	finalBackoff := baseBackoff + jitter

	// Ensure we don't go negative
	if finalBackoff < 0 {
		finalBackoff = 0
	}

	return time.Duration(finalBackoff)
}

// CalculateBackoffDefault uses default backoff config
func CalculateBackoffDefault(attempt int) time.Duration {
	return CalculateBackoff(attempt, DefaultBackoffConfig())
}
