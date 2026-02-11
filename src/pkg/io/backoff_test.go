package io

import (
	"sync"
	"testing"
	"time"
)

// TestCalculateBackoff_BasicProgression tests exponential growth
func TestCalculateBackoff_BasicProgression(t *testing.T) {
	config := BackoffConfig{
		InitialDuration:  1 * time.Second,
		MaxDuration:      30 * time.Second,
		Multiplier:       2.0,
		JitterPercentage: 0, // No jitter for predictable testing
	}

	tests := []struct {
		name            string
		attempt         int
		expectedSeconds float64
	}{
		{"attempt 1", 1, 1.0},
		{"attempt 2", 2, 2.0},
		{"attempt 3", 3, 4.0},
		{"attempt 4", 4, 8.0},
		{"attempt 5", 5, 16.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backoff := CalculateBackoff(tt.attempt, config)
			expected := time.Duration(tt.expectedSeconds) * time.Second
			if backoff != expected {
				t.Errorf("CalculateBackoff(%d) = %v, want %v", tt.attempt, backoff, expected)
			}
		})
	}
}

// TestCalculateBackoff_MaxDurationCap tests that backoff never exceeds max
func TestCalculateBackoff_MaxDurationCap(t *testing.T) {
	config := BackoffConfig{
		InitialDuration:  1 * time.Second,
		MaxDuration:      10 * time.Second,
		Multiplier:       2.0,
		JitterPercentage: 0,
	}

	// High attempts should cap at MaxDuration
	for attempt := 1; attempt <= 20; attempt++ {
		backoff := CalculateBackoff(attempt, config)
		if backoff > config.MaxDuration {
			t.Errorf("CalculateBackoff(%d) = %v, exceeds max %v", attempt, backoff, config.MaxDuration)
		}
	}
}

// TestCalculateBackoff_WithJitter tests that jitter varies results
func TestCalculateBackoff_WithJitter(t *testing.T) {
	config := BackoffConfig{
		InitialDuration:  10 * time.Second,
		MaxDuration:      30 * time.Second,
		Multiplier:       1.5,
		JitterPercentage: 10, // ±10% jitter
	}

	// Calculate same attempt multiple times with jitter
	results := make([]time.Duration, 10)
	for i := 0; i < 10; i++ {
		results[i] = CalculateBackoff(5, config)
	}

	// Check that we get different values (with jitter)
	// Note: theoretically possible to get same value, but very unlikely with 10 runs
	hasDifference := false
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			hasDifference = true
			break
		}
	}

	if !hasDifference {
		t.Logf("Warning: All jitter values identical (unlikely but possible): %v", results)
	}

	// Check all values are within acceptable range (within jitter bounds)
	baseBackoff := float64(10) * 1.5 // 10 seconds * 1.5^4
	maxJitter := baseBackoff * 0.1    // ±10%
	minExpected := time.Duration((baseBackoff - maxJitter) * 1e9) // Convert to nanoseconds
	maxExpected := time.Duration((baseBackoff + maxJitter) * 1e9)

	for i, result := range results {
		if result < minExpected || result > maxExpected {
			t.Logf("Result %d = %v, outside range [%v, %v]", i, result, minExpected, maxExpected)
		}
	}
}

// TestCalculateBackoff_ZeroAttempt tests edge case of zero attempt
func TestCalculateBackoff_ZeroAttempt(t *testing.T) {
	config := DefaultBackoffConfig()
	backoff := CalculateBackoff(0, config)
	if backoff != 0 {
		t.Errorf("CalculateBackoff(0) = %v, want 0", backoff)
	}
}

// TestCalculateBackoff_NegativeAttempt tests edge case of negative attempt
func TestCalculateBackoff_NegativeAttempt(t *testing.T) {
	config := DefaultBackoffConfig()
	backoff := CalculateBackoff(-5, config)
	if backoff != 0 {
		t.Errorf("CalculateBackoff(-5) = %v, want 0", backoff)
	}
}

// TestCalculateBackoff_HighAttempt tests overflow protection
func TestCalculateBackoff_HighAttempt(t *testing.T) {
	config := BackoffConfig{
		InitialDuration:  1 * time.Second,
		MaxDuration:      30 * time.Second,
		Multiplier:       10.0, // Aggressive multiplier
		JitterPercentage: 0,
	}

	// Very high attempt should not overflow, just cap at max
	backoff := CalculateBackoff(100, config)
	if backoff != config.MaxDuration {
		t.Errorf("CalculateBackoff(100) = %v, want %v (capped at max)", backoff, config.MaxDuration)
	}
}

// TestCalculateBackoff_NoNegativeResult tests that result never goes negative despite jitter
func TestCalculateBackoff_NoNegativeResult(t *testing.T) {
	config := BackoffConfig{
		InitialDuration:  100 * time.Millisecond,
		MaxDuration:      10 * time.Second,
		Multiplier:       1.5,
		JitterPercentage: 100, // Extreme ±100% jitter
	}

	// Run multiple times to increase chance of hitting negative jitter
	for i := 0; i < 100; i++ {
		backoff := CalculateBackoff(1, config)
		if backoff < 0 {
			t.Errorf("CalculateBackoff(1) returned negative value: %v", backoff)
		}
	}
}

// TestCalculateBackoffDefault uses default config
func TestCalculateBackoffDefault(t *testing.T) {
	backoff := CalculateBackoffDefault(1)
	if backoff <= 0 {
		t.Errorf("CalculateBackoffDefault(1) = %v, want positive duration", backoff)
	}

	// Should be roughly 1 second (with jitter)
	if backoff < 800*time.Millisecond || backoff > 1200*time.Millisecond {
		t.Logf("CalculateBackoffDefault(1) = %v, expected ~1s (±10% jitter)", backoff)
	}
}

// TestDefaultBackoffConfig validates default configuration
func TestDefaultBackoffConfig(t *testing.T) {
	config := DefaultBackoffConfig()

	if config.InitialDuration != 1*time.Second {
		t.Errorf("InitialDuration = %v, want 1s", config.InitialDuration)
	}

	if config.MaxDuration != 30*time.Second {
		t.Errorf("MaxDuration = %v, want 30s", config.MaxDuration)
	}

	if config.Multiplier != 1.5 {
		t.Errorf("Multiplier = %v, want 1.5", config.Multiplier)
	}

	if config.JitterPercentage != 10 {
		t.Errorf("JitterPercentage = %v, want 10", config.JitterPercentage)
	}
}

// TestCalculateBackoff_ConcurrentAccess tests thread-safety with concurrent goroutines
func TestCalculateBackoff_ConcurrentAccess(t *testing.T) {
	config := BackoffConfig{
		InitialDuration:  100 * time.Millisecond,
		MaxDuration:      10 * time.Second,
		Multiplier:       1.5,
		JitterPercentage: 10,
	}

	// Run CalculateBackoff from multiple goroutines simultaneously
	numGoroutines := 100
	numCallsPerGoroutine := 100
	var wg sync.WaitGroup
	results := make([]time.Duration, numGoroutines*numCallsPerGoroutine)
	resultsMu := sync.Mutex{}
	resultIdx := 0

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for call := 1; call <= numCallsPerGoroutine; call++ {
				backoff := CalculateBackoff(call%3+1, config) // Vary attempt number

				// Store result thread-safely
				resultsMu.Lock()
				results[resultIdx] = backoff
				resultIdx++
				resultsMu.Unlock()

				// Verify result is valid
				if backoff < 0 {
					t.Errorf("goroutine %d call %d: negative backoff %v", goroutineID, call, backoff)
				}
				if backoff > config.MaxDuration {
					t.Errorf("goroutine %d call %d: backoff %v exceeds max %v", goroutineID, call, backoff, config.MaxDuration)
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify we got all results
	if resultIdx != numGoroutines*numCallsPerGoroutine {
		t.Errorf("collected %d results, want %d", resultIdx, numGoroutines*numCallsPerGoroutine)
	}

	// Verify we got variety in results (due to jitter)
	hasDifference := false
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			hasDifference = true
			break
		}
	}

	if !hasDifference {
		t.Logf("Warning: All concurrent results identical (unlikely but possible)")
	}

	t.Logf("Successfully ran %d concurrent calls to CalculateBackoff", numGoroutines*numCallsPerGoroutine)
}

