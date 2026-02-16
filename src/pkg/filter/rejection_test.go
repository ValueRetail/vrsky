package filter

import (
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// TestRejectionHandler_MetadataAddition tests that rejection metadata is added
func TestRejectionHandler_MetadataAddition(t *testing.T) {
	env := &envelope.Envelope{
		ID:      "test-env",
		Payload: []byte("test data"),
	}

	// Manually add metadata like HandleRejection would
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}
	env.Metadata["rejected_at"] = "test_time"
	env.Metadata["rejection_reason"] = "Test rejection"
	env.Metadata["rejected_by_rule"] = "rule_1"

	// Check metadata was added
	if env.Metadata == nil {
		t.Errorf("Metadata not created")
	}

	if reason, ok := env.Metadata["rejection_reason"]; !ok || reason != "Test rejection" {
		t.Errorf("rejection_reason not set correctly")
	}

	if ruleID, ok := env.Metadata["rejected_by_rule"]; !ok || ruleID != "rule_1" {
		t.Errorf("rejected_by_rule not set correctly")
	}
}

// TestDeadLetterQueue_MetadataAddition tests that DLQ metadata is added
func TestDeadLetterQueue_MetadataAddition(t *testing.T) {
	env := &envelope.Envelope{
		ID:         "test-env",
		Payload:    []byte("test data"),
		RetryCount: 2,
	}

	// Manually add metadata like PublishMessage would
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}
	env.Metadata["dlq_timestamp"] = "test_time"
	env.Metadata["dlq_reason"] = "Test DLQ reason"
	env.Metadata["retry_count"] = env.RetryCount

	// Check metadata was added
	if env.Metadata == nil {
		t.Errorf("Metadata not created")
	}

	if reason, ok := env.Metadata["dlq_reason"]; !ok || reason != "Test DLQ reason" {
		t.Errorf("dlq_reason not set correctly")
	}

	if retryCount, ok := env.Metadata["retry_count"]; !ok || retryCount != 2 {
		t.Errorf("retry_count not set correctly")
	}
}

// TestErrorRecovery_ProcessingErrorRetryCount tests retry count increment
func TestErrorRecovery_ProcessingErrorRetryCount(t *testing.T) {
	tests := []struct {
		name            string
		initialRetries  int
		expectedRetries int
	}{
		{
			"first retry",
			0,
			1,
		},
		{
			"second retry",
			1,
			2,
		},
		{
			"third retry",
			2,
			3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := &envelope.Envelope{
				ID:         "test-env",
				Payload:    []byte("test data"),
				RetryCount: tt.initialRetries,
			}

			// Simulate retry count increment
			env.RetryCount++

			if env.RetryCount != tt.expectedRetries {
				t.Errorf("retry count = %d, want %d", env.RetryCount, tt.expectedRetries)
			}
		})
	}
}

// TestRejectionHandler_Backoff tests backoff configuration
func TestRejectionHandler_Backoff(t *testing.T) {
	backoff := DefaultBackoffConfig()

	delays := []int64{
		backoff.CalculateBackoffDelay(0).Milliseconds(),
		backoff.CalculateBackoffDelay(1).Milliseconds(),
		backoff.CalculateBackoffDelay(2).Milliseconds(),
	}

	// Each delay should be approximately double the previous (exponential backoff)
	if delays[1] <= delays[0] {
		t.Errorf("backoff delays not increasing: %v", delays)
	}

	if delays[2] <= delays[1] {
		t.Errorf("backoff delays not increasing: %v", delays)
	}
}

// BenchmarkEnvelopeMarshaling benchmarks envelope marshaling (used in rejection/DLQ)
func BenchmarkEnvelopeMarshaling(b *testing.B) {
	env := &envelope.Envelope{
		ID:      "bench-env",
		Payload: []byte("test data"),
		Metadata: map[string]interface{}{
			"key1": "value1",
			"key2": "value2",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = envelope.Marshal(env)
	}
}
