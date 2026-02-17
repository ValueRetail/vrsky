package filter

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestFilterMetrics_Creation(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewFilterMetrics("test_filter", registry)

	if metrics == nil {
		t.Fatalf("NewFilterMetrics returned nil")
	}

	if metrics.receivedCounter == nil || metrics.acceptedCounter == nil || metrics.rejectedCounter == nil {
		t.Errorf("Metrics not properly initialized")
	}
}

func TestFilterMetrics_RecordOperations(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewFilterMetrics("test_filter", registry)

	// These should not panic
	metrics.RecordReceived()
	metrics.RecordAccepted()
	metrics.RecordRejected()
	metrics.RecordFailure()
	metrics.RecordProcessDuration(100 * time.Millisecond)
}

func TestBackoffConfig_DefaultBackoff(t *testing.T) {
	config := DefaultBackoffConfig()

	if config.InitialDelay == 0 {
		t.Errorf("InitialDelay should not be 0")
	}
	if config.MaxDelay == 0 {
		t.Errorf("MaxDelay should not be 0")
	}
	if config.Multiplier <= 1 {
		t.Errorf("Multiplier should be > 1")
	}
}

func TestBackoffConfig_CalculateDelay(t *testing.T) {
	config := BackoffConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		Multiplier:   2.0,
	}

	tests := []struct {
		name     string
		attempt  int
		expected time.Duration
	}{
		{"first attempt", 0, 100 * time.Millisecond},
		{"second attempt", 1, 200 * time.Millisecond},
		{"third attempt", 2, 400 * time.Millisecond},
		{"fourth attempt", 3, 800 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := config.CalculateBackoffDelay(tt.attempt)
			// Allow 1ms tolerance for floating point rounding
			if delay < tt.expected-1*time.Millisecond || delay > tt.expected+1*time.Millisecond {
				t.Errorf("CalculateBackoffDelay(%d) = %v, want %v", tt.attempt, delay, tt.expected)
			}
		})
	}
}

func TestBackoffConfig_MaxDelayEnforced(t *testing.T) {
	config := BackoffConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
		Multiplier:   2.0,
	}

	// After many retries, should not exceed max delay
	for i := 0; i < 20; i++ {
		delay := config.CalculateBackoffDelay(i)
		if delay > config.MaxDelay {
			t.Errorf("Delay %v exceeds max delay %v", delay, config.MaxDelay)
		}
	}
}
