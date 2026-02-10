package io

import (
	"math"
	"math/rand"
	"time"
)

// rng is a per-package random number generator seeded at init time
// Using a separate RNG avoids contention on the global rand source and
// ensures different sequences across restarts
var rng *rand.Rand

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
	// Use per-package RNG to avoid contention and ensure non-deterministic behavior
	jitterFraction := config.JitterPercentage / 100.0
	maxJitter := baseBackoff * jitterFraction
	jitter := (rng.Float64()*2 - 1) * maxJitter // Random in [-maxJitter, +maxJitter]
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
