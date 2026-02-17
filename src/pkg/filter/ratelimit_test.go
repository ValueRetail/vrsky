package filter

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestTimeWindowRateLimit_AllowedWithinLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                   "time_window_test",
		Priority:             1,
		Strategy:             "time_window",
		MaxMessagesPerWindow: 5,
		WindowDurationSeconds: 60,
		ExceedAction:         "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// Send 5 messages - all should be allowed
	for i := 0; i < 5; i++ {
		decision, err := rle.EvaluateRules(ctx, map[string]interface{}{"id": i}, nil)
		if err != nil {
			t.Fatalf("EvaluateRules failed: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("Message %d should be allowed, got Allowed=%v", i, decision.Allowed)
		}
	}
}

func TestTimeWindowRateLimit_RejectedExceedsLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                   "time_window_test",
		Priority:             1,
		Strategy:             "time_window",
		MaxMessagesPerWindow: 3,
		WindowDurationSeconds: 60,
		ExceedAction:         "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// Send 3 allowed messages
	for i := 0; i < 3; i++ {
		decision, err := rle.EvaluateRules(ctx, map[string]interface{}{"id": i}, nil)
		if err != nil {
			t.Fatalf("EvaluateRules failed: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("Message %d should be allowed", i)
		}
	}

	// 4th message should be rejected
	decision, err := rle.EvaluateRules(ctx, map[string]interface{}{"id": 4}, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("4th message should be rejected")
	}
	if decision.Action != "reject" {
		t.Errorf("Action should be 'reject', got '%s'", decision.Action)
	}
}

func TestTimeWindowRateLimit_WindowReset(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                   "time_window_test",
		Priority:             1,
		Strategy:             "time_window",
		MaxMessagesPerWindow: 2,
		WindowDurationSeconds: 1, // 1 second window for testing
		ExceedAction:         "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// Send 2 messages in first window
	for i := 0; i < 2; i++ {
		decision, err := rle.EvaluateRules(ctx, map[string]interface{}{"id": i}, nil)
		if err != nil {
			t.Fatalf("EvaluateRules failed: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("Message %d in first window should be allowed", i)
		}
	}

	// 3rd message in first window should be rejected
	decision, err := rle.EvaluateRules(ctx, map[string]interface{}{"id": 3}, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("3rd message in first window should be rejected")
	}

	// Wait for window to reset
	time.Sleep(1100 * time.Millisecond)

	// Next message should be allowed (new window)
	decision, err = rle.EvaluateRules(ctx, map[string]interface{}{"id": 4}, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("Message in new window should be allowed after reset")
	}
}

func TestTimeWindowRateLimit_EdgeCaseWindowBoundary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                   "time_window_test",
		Priority:             1,
		Strategy:             "time_window",
		MaxMessagesPerWindow: 1,
		WindowDurationSeconds: 1,
		ExceedAction:         "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// First message allowed
	decision, err := rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("First message should be allowed")
	}

	// Immediately second message rejected
	decision, err = rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("Second message should be rejected")
	}
}

func TestTimeWindowRateLimit_QueueAction(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                   "time_window_queue",
		Priority:             1,
		Strategy:             "time_window",
		MaxMessagesPerWindow: 2,
		WindowDurationSeconds: 60,
		ExceedAction:         "queue",
		QueueSize:            10,
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// Send 2 allowed messages
	for i := 0; i < 2; i++ {
		decision, err := rle.EvaluateRules(ctx, nil, nil)
		if err != nil {
			t.Fatalf("EvaluateRules failed: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("Message %d should be allowed", i)
		}
	}

	// 3rd message should queue
	decision, err := rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("3rd message should not be allowed (queued)")
	}
	if decision.Action != "queue" {
		t.Errorf("Action should be 'queue', got '%s'", decision.Action)
	}
}

func TestTimeWindowRateLimit_DropAction(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                   "time_window_drop",
		Priority:             1,
		Strategy:             "time_window",
		MaxMessagesPerWindow: 1,
		WindowDurationSeconds: 60,
		ExceedAction:         "drop",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// First message allowed
	decision, err := rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("First message should be allowed")
	}

	// Second message should drop
	decision, err = rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("Second message should not be allowed (dropped)")
	}
	if decision.Action != "drop" {
		t.Errorf("Action should be 'drop', got '%s'", decision.Action)
	}
}

func TestConcurrentRateLimit_AllowedWithinLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:            "concurrent_test",
		Priority:      1,
		Strategy:      "concurrent",
		MaxConcurrent: 3,
		ExceedAction:  "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// Send 3 messages - all should be allowed
	for i := 0; i < 3; i++ {
		decision, err := rle.EvaluateRules(ctx, nil, nil)
		if err != nil {
			t.Fatalf("EvaluateRules failed: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("Message %d should be allowed", i)
		}
	}
}

func TestConcurrentRateLimit_RejectedExceedsLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:            "concurrent_test",
		Priority:      1,
		Strategy:      "concurrent",
		MaxConcurrent: 2,
		ExceedAction:  "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// Send 2 allowed messages
	for i := 0; i < 2; i++ {
		decision, err := rle.EvaluateRules(ctx, nil, nil)
		if err != nil {
			t.Fatalf("EvaluateRules failed: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("Message %d should be allowed", i)
		}
	}

	// 3rd message should be rejected
	decision, err := rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("3rd message should be rejected")
	}
	if decision.Action != "reject" {
		t.Errorf("Action should be 'reject', got '%s'", decision.Action)
	}
}

func TestConcurrentRateLimit_DecrementOnComplete(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:            "concurrent_test",
		Priority:      1,
		Strategy:      "concurrent",
		MaxConcurrent: 1,
		ExceedAction:  "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// First message allowed
	decision, err := rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("First message should be allowed")
	}

	// 2nd message should be rejected (limit reached)
	decision, err = rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("2nd message should be rejected")
	}

	// After first message complete, 2nd should be allowed
	if err := rle.RecordMessageComplete("concurrent_test"); err != nil {
		t.Fatalf("RecordMessageComplete failed: %v", err)
	}

	decision, err = rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("After complete, next message should be allowed")
	}
}

func TestConcurrentRateLimit_MultipleRulesIndependent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule1 := &RateLimitRule{
		ID:            "rule_1",
		Priority:      1,
		Strategy:      "concurrent",
		MaxConcurrent: 1,
		ExceedAction:  "reject",
		Condition: &Condition{
			Operator: "==",
			Field:    "type",
			Value:    "A",
		},
	}

	rule2 := &RateLimitRule{
		ID:            "rule_2",
		Priority:      2,
		Strategy:      "concurrent",
		MaxConcurrent: 2,
		ExceedAction:  "reject",
		Condition: &Condition{
			Operator: "==",
			Field:    "type",
			Value:    "B",
		},
	}

	if err := rle.AddRule(rule1); err != nil {
		t.Fatalf("AddRule rule1 failed: %v", err)
	}
	if err := rle.AddRule(rule2); err != nil {
		t.Fatalf("AddRule rule2 failed: %v", err)
	}

	ctx := context.Background()

	// Send type A - allowed (rule1: 1/1)
	decision, err := rle.EvaluateRules(ctx, map[string]interface{}{"type": "A"}, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("Type A message 1 should be allowed")
	}

	// Send type B - allowed (rule2: 1/2)
	decision, err = rle.EvaluateRules(ctx, map[string]interface{}{"type": "B"}, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("Type B message 1 should be allowed")
	}

	// Send type B again - allowed (rule2: 2/2)
	decision, err = rle.EvaluateRules(ctx, map[string]interface{}{"type": "B"}, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("Type B message 2 should be allowed")
	}

	// Send type B again - rejected (rule2: exceeds)
	decision, err = rle.EvaluateRules(ctx, map[string]interface{}{"type": "B"}, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("Type B message 3 should be rejected")
	}
}

func TestTokenBucketRateLimit_TokenRefill(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                  "token_test",
		Priority:            1,
		Strategy:            "token_bucket",
		TokenBucketRate:     10, // 10 tokens/sec
		TokenBucketCapacity: 10,
		ExceedAction:        "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// Consume 10 tokens
	for i := 0; i < 10; i++ {
		decision, err := rle.EvaluateRules(ctx, nil, nil)
		if err != nil {
			t.Fatalf("EvaluateRules failed: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("Message %d should be allowed", i)
		}
	}

	// 11th message should be rejected (no tokens)
	decision, err := rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("11th message should be rejected (no tokens)")
	}

	// Wait 1 second = 10 new tokens
	time.Sleep(1 * time.Second)

	// Next message should be allowed
	decision, err = rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("After 1s refill, message should be allowed")
	}
}

func TestTokenBucketRateLimit_BurstCapacity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                  "token_burst",
		Priority:            1,
		Strategy:            "token_bucket",
		TokenBucketRate:     10, // 10 tokens/sec
		TokenBucketCapacity: 20, // Can burst to 20
		ExceedAction:        "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// Consume 20 tokens (burst capacity)
	for i := 0; i < 20; i++ {
		decision, err := rle.EvaluateRules(ctx, nil, nil)
		if err != nil {
			t.Fatalf("EvaluateRules failed: %v", err)
		}
		if !decision.Allowed {
			t.Errorf("Message %d should be allowed (within burst)", i)
		}
	}

	// 21st message should be rejected
	decision, err := rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("21st message should be rejected")
	}
}

func TestTokenBucketRateLimit_RejectedWhenEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	condEngine := NewConditionEngine()
	metrics := NewFilterMetrics("test", prometheus.NewRegistry())
	rle := NewRateLimitEngine(condEngine, metrics, logger)
	defer rle.Stop()

	rule := &RateLimitRule{
		ID:                  "token_empty",
		Priority:            1,
		Strategy:            "token_bucket",
		TokenBucketRate:     1, // 1 token/sec
		TokenBucketCapacity: 1,
		ExceedAction:        "reject",
	}

	if err := rle.AddRule(rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	ctx := context.Background()

	// First message allowed (consume 1 token)
	decision, err := rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if !decision.Allowed {
		t.Errorf("First message should be allowed")
	}

	// Second message rejected (no tokens, rate is 1/sec)
	decision, err = rle.EvaluateRules(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}
	if decision.Allowed {
		t.Errorf("Second message should be rejected (no tokens)")
	}
	if decision.Action != "reject" {
		t.Errorf("Action should be 'reject', got '%s'", decision.Action)
	}
}

func TestValidateRateLimitRule_MultipleStrategiesError(t *testing.T) {
	rule := &RateLimitRule{
		ID:                    "invalid",
		Strategy:              "time_window",
		MaxMessagesPerWindow:  10,
		WindowDurationSeconds: 60,
		MaxConcurrent:         5, // This makes it invalid - two strategies
		ExceedAction:          "reject",
	}

	if err := validateRateLimitRule(rule); err == nil {
		t.Errorf("Should return error for multiple strategies")
	}
}

func TestValidateRateLimitRule_NoStrategyError(t *testing.T) {
	rule := &RateLimitRule{
		ID:           "invalid",
		Strategy:     "time_window",
		ExceedAction: "reject",
		// No strategy fields set
	}

	if err := validateRateLimitRule(rule); err == nil {
		t.Errorf("Should return error for no strategy configured")
	}
}

func TestValidateRateLimitRule_InvalidStrategyError(t *testing.T) {
	rule := &RateLimitRule{
		ID:                   "invalid",
		Strategy:             "invalid_strategy",
		MaxMessagesPerWindow: 10,
		WindowDurationSeconds: 60,
		ExceedAction:         "reject",
	}

	if err := validateRateLimitRule(rule); err == nil {
		t.Errorf("Should return error for invalid strategy")
	}
}
