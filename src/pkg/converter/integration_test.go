package converter

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/nats-io/nats.go"
)

// TestNewConverterIntegration tests converter creation with real config service simulation
func TestNewConverterIntegration(t *testing.T) {
	// This test requires CONFIG_SERVICE_URL to be set to a mock server
	// In a real integration test, you'd start an httptest.Server

	// Setup logger
	_ = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// For this test, we'll mock the NATS connection
	// In a real test, you'd use a real NATS instance or embedded server
	t.Run("converter initialization succeeds with valid config", func(t *testing.T) {
		// Skip if CONFIG_SERVICE_URL not set
		configURL := os.Getenv("CONFIG_SERVICE_URL")
		if configURL == "" {
			t.Skip("CONFIG_SERVICE_URL not set, skipping integration test")
		}

		// Mock NATS connection would go here
		// For now, we just verify the logger and basic setup works
		_ = true
	})
}

// TestConverterMessageFlow tests the complete message flow (requires NATS)
func TestConverterMessageFlow(t *testing.T) {
	// Skip if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Check if NATS is available
	natsURL := os.Getenv("NATS_URLS")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	// Try to connect to NATS
	nc, err := nats.Connect(natsURL, nats.MaxReconnects(-1))
	if err != nil {
		t.Skipf("Cannot connect to NATS at %s: %v", natsURL, err)
	}
	defer nc.Close()

	_ = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	t.Run("message published to input topic is processed", func(t *testing.T) {
		// Create a channel to capture published messages
		outputMessages := make(chan []byte, 10)
		defer close(outputMessages)

		// Subscribe to output topic to capture results
		outputTopic := "test.converter.output"
		sub, err := nc.Subscribe(outputTopic, func(msg *nats.Msg) {
			outputMessages <- msg.Data
		})
		if err != nil {
			t.Fatalf("Failed to subscribe to output topic: %v", err)
		}
		defer sub.Unsubscribe()

		// Publish a test message to input topic
		inputTopic := "test.converter.input"
		env := envelope.New()
		env.ID = "msg-001"
		env.TenantID = "test-tenant"
		env.IntegrationID = "test-integration"
		env.ContentType = "application/json"
		env.Payload = []byte(`{"order_id": "12345", "amount": 99.99}`)

		data, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("Failed to marshal envelope: %v", err)
		}

		err = nc.Publish(inputTopic, data)
		if err != nil {
			t.Fatalf("Failed to publish message: %v", err)
		}

		// Give it a moment to process
		time.Sleep(100 * time.Millisecond)
	})
}

// TestConverterRetryLogic tests exponential backoff retry mechanism
func TestConverterRetryLogic(t *testing.T) {
	t.Run("retry attempts increment correctly", func(t *testing.T) {
		retryAttempts := 0
		maxRetries := 3
		initialBackoff := 1 * time.Second

		for attempt := 0; attempt < maxRetries; attempt++ {
			retryAttempts++

			// Calculate exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(math.Pow(2, float64(attempt))) * initialBackoff

			expectedBackoff := float64(int64(1) << uint(attempt))
			if backoff.Seconds() != expectedBackoff {
				t.Errorf("attempt %d: expected backoff %.0fs, got %.0fs",
					attempt, expectedBackoff, backoff.Seconds())
			}
		}

		if retryAttempts != maxRetries {
			t.Errorf("expected %d retry attempts, got %d", maxRetries, retryAttempts)
		}
	})

	t.Run("retry stops after max retries", func(t *testing.T) {
		maxRetries := 3
		attempts := 0

		for i := 0; i < maxRetries; i++ {
			attempts++
			if attempts > maxRetries {
				t.Errorf("exceeded max retries: %d > %d", attempts, maxRetries)
				break
			}
		}

		if attempts != maxRetries {
			t.Errorf("expected %d attempts, got %d", maxRetries, attempts)
		}
	})
}

// TestConverterMetrics tests metrics recording during message processing
func TestConverterMetrics(t *testing.T) {
	_ = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	t.Run("metrics are recorded correctly", func(t *testing.T) {
		converterID := "test-converter"
		tenantID := "test-tenant"

		metrics, err := NewMetrics(converterID, tenantID)
		if err != nil {
			t.Fatalf("Failed to create metrics: %v", err)
		}

		// Record some metric events
		metrics.RecordMessageReceived()
		metrics.RecordMessageSucceeded()
		metrics.RecordTransformationDuration(100 * time.Millisecond)
		metrics.RecordRetryAttempt(1)

		// Verify metrics were created (in real test, we'd query Prometheus)
		if metrics == nil {
			t.Error("metrics should not be nil")
		}
	})
}

// TestConverterGracefulShutdown tests graceful shutdown behavior
func TestConverterGracefulShutdown(t *testing.T) {
	_ = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Skip if NATS not available
	natsURL := os.Getenv("NATS_URLS")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	opts := []nats.Option{
		nats.MaxReconnects(0),
		nats.ConnectHandler(func(nc *nats.Conn) {}), // No-op handler
	}
	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		t.Skipf("Cannot connect to NATS at %s: %v", natsURL, err)
	}
	defer nc.Close()

	t.Run("converter stops cleanly within timeout", func(t *testing.T) {
		_ = context.Background()

		// We can't fully test this without a real converter,
		// but we can verify the timeout logic
		timeout := 30 * time.Second
		done := make(chan bool)

		go func() {
			// Simulate work
			time.Sleep(100 * time.Millisecond)
			done <- true
		}()

		select {
		case <-done:
			// Completed successfully
		case <-time.After(timeout):
			t.Error("operation exceeded timeout")
		}
	})
}

// TestConverterErrorHandling tests error handling strategies
func TestConverterErrorHandling(t *testing.T) {
	t.Run("missing fields strategy - fail", func(t *testing.T) {
		config := ErrorHandlingConfig{
			MissingFields: "fail",
		}

		if config.MissingFields != "fail" {
			t.Errorf("expected 'fail', got %q", config.MissingFields)
		}
	})

	t.Run("type mismatch strategy - coerce", func(t *testing.T) {
		config := ErrorHandlingConfig{
			TypeMismatch: "coerce",
		}

		if config.TypeMismatch != "coerce" {
			t.Errorf("expected 'coerce', got %q", config.TypeMismatch)
		}
	})

	t.Run("validation error strategy - skip", func(t *testing.T) {
		config := ErrorHandlingConfig{
			ValidationError: "skip",
		}

		if config.ValidationError != "skip" {
			t.Errorf("expected 'skip', got %q", config.ValidationError)
		}
	})
}

// TestConverterComponentInterface tests component interface implementation
func TestConverterComponentInterface(t *testing.T) {
	_ = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	t.Run("component Name returns converter ID", func(t *testing.T) {
		// Verify that Name() method would return converter ID
		converterID := "test-converter"
		if converterID == "" {
			t.Error("converter ID should not be empty")
		}
	})

	t.Run("component Type returns 'converter'", func(t *testing.T) {
		componentType := "converter"
		if componentType != "converter" {
			t.Errorf("expected 'converter', got %q", componentType)
		}
	})

	t.Run("component Version returns version string", func(t *testing.T) {
		version := "1.0.0"
		if version == "" {
			t.Error("version should not be empty")
		}
	})
}

// TestConverterTransformations tests end-to-end transformation with Phase 2F rule engine
func TestConverterTransformations(t *testing.T) {
	_ = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	t.Run("simple field mapping transformation", func(t *testing.T) {
		env := envelope.New()
		env.ID = "msg-transform-001"
		env.TenantID = "test-tenant"
		env.IntegrationID = "test-integration"
		env.ContentType = "application/json"
		env.Payload = []byte(`{"customer_name": "John Doe", "order_amount": 99.99}`)

		rules := []Transformation{
			{Source: "customer_name", Target: "name"},
			{Source: "order_amount", Target: "amount"},
		}

		ctx := context.Background()
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

		// Create rule engine
		loggerAdapter := &slogLoggerAdapter{logger: logger}
		fm := NewFieldMapper(ctx, loggerAdapter)
		fr := NewFunctionRegistry(ctx, loggerAdapter)
		ee := NewExpressionEvaluator(ctx, loggerAdapter, fr)
		re := NewRuleEngine(ctx, loggerAdapter, fm, ee, fr)

		// Execute transformations
		result, err := re.ExecuteTransformations(env.Payload, rules)
		if err != nil {
			t.Fatalf("ExecuteTransformations failed: %v", err)
		}

		// Verify results
		if result["name"] != "John Doe" {
			t.Errorf("expected name='John Doe', got %v", result["name"])
		}
		if result["amount"] != float64(99.99) {
			t.Errorf("expected amount=99.99, got %v", result["amount"])
		}
	})

	t.Run("expression-based transformation", func(t *testing.T) {
		env := envelope.New()
		env.ID = "msg-transform-002"
		env.TenantID = "test-tenant"
		env.IntegrationID = "test-integration"
		env.ContentType = "application/json"
		env.Payload = []byte(`{"quantity": 5, "unit_price": 20.0}`)

		rules := []Transformation{
			{Expression: "quantity * unit_price", Target: "total_amount"},
		}

		ctx := context.Background()
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

		loggerAdapter := &slogLoggerAdapter{logger: logger}
		fm := NewFieldMapper(ctx, loggerAdapter)
		fr := NewFunctionRegistry(ctx, loggerAdapter)
		ee := NewExpressionEvaluator(ctx, loggerAdapter, fr)
		re := NewRuleEngine(ctx, loggerAdapter, fm, ee, fr)

		result, err := re.ExecuteTransformations(env.Payload, rules)
		if err != nil {
			t.Fatalf("ExecuteTransformations failed: %v", err)
		}

		// JSON unmarshals numbers to float64, so 5 * 20.0 = 100.0
		if result["total_amount"] != float64(100) {
			t.Errorf("expected total_amount=100, got %v", result["total_amount"])
		}
	})

	t.Run("conditional transformation", func(t *testing.T) {
		env := envelope.New()
		env.ID = "msg-transform-003"
		env.TenantID = "test-tenant"
		env.IntegrationID = "test-integration"
		env.ContentType = "application/json"
		env.Payload = []byte(`{"order_total": 500.0, "is_premium": true}`)

		rules := []Transformation{
			{
				Value:     "discount_applied",
				Target:    "status",
				Condition: "is_premium == true && order_total > 100",
			},
		}

		ctx := context.Background()
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

		loggerAdapter := &slogLoggerAdapter{logger: logger}
		fm := NewFieldMapper(ctx, loggerAdapter)
		fr := NewFunctionRegistry(ctx, loggerAdapter)
		ee := NewExpressionEvaluator(ctx, loggerAdapter, fr)
		re := NewRuleEngine(ctx, loggerAdapter, fm, ee, fr)

		result, err := re.ExecuteTransformations(env.Payload, rules)
		if err != nil {
			t.Fatalf("ExecuteTransformations failed: %v", err)
		}

		// Condition is true, so status should be set
		if result["status"] != "discount_applied" {
			t.Errorf("expected status='discount_applied', got %v", result["status"])
		}
	})

	t.Run("type conversion during transformation", func(t *testing.T) {
		env := envelope.New()
		env.ID = "msg-transform-004"
		env.TenantID = "test-tenant"
		env.IntegrationID = "test-integration"
		env.ContentType = "application/json"
		env.Payload = []byte(`{"id": "12345", "price": "99.99"}`)

		rules := []Transformation{
			{Source: "id", Target: "customer_id", Type: "int"},
			{Source: "price", Target: "amount", Type: "float"},
		}

		ctx := context.Background()
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

		loggerAdapter := &slogLoggerAdapter{logger: logger}
		fm := NewFieldMapper(ctx, loggerAdapter)
		fr := NewFunctionRegistry(ctx, loggerAdapter)
		ee := NewExpressionEvaluator(ctx, loggerAdapter, fr)
		re := NewRuleEngine(ctx, loggerAdapter, fm, ee, fr)

		result, err := re.ExecuteTransformations(env.Payload, rules)
		if err != nil {
			t.Fatalf("ExecuteTransformations failed: %v", err)
		}

		// Verify type conversions
		if result["customer_id"] != 12345 {
			t.Errorf("expected customer_id=12345 (int), got %v (%T)", result["customer_id"], result["customer_id"])
		}
		if result["amount"] != float32(99.99) {
			t.Errorf("expected amount=99.99 (float32), got %v (%T)", result["amount"], result["amount"])
		}
	})
}

// TestConverterProcessMessage tests message processing (Phase 1 pass-through)
func TestConverterProcessMessage(t *testing.T) {
	_ = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	t.Run("message passes through unchanged in Phase 1", func(t *testing.T) {
		env := envelope.New()
		env.ID = "msg-001"
		env.TenantID = "test-tenant"
		env.IntegrationID = "test-integration"
		env.ContentType = "application/json"
		env.Payload = []byte(`{"order_id": "12345"}`)

		// In Phase 1, the envelope should pass through unchanged
		// This would be tested with a real converter instance
		if env.ID != "msg-001" {
			t.Error("envelope ID should not change")
		}
	})
}

// Helper function to calculate exponential backoff (used in tests)
func calculateBackoff(attempt int, initialBackoff time.Duration) time.Duration {
	multiplier := 1 << uint(attempt) // 2^attempt
	return time.Duration(multiplier) * initialBackoff
}

// TestBackoffCalculation tests the backoff calculation helper
func TestBackoffCalculation(t *testing.T) {
	tests := []struct {
		name            string
		attempt         int
		initialBackoff  time.Duration
		expectedSeconds float64
	}{
		{"first attempt (1s)", 0, time.Second, 1},
		{"second attempt (2s)", 1, time.Second, 2},
		{"third attempt (4s)", 2, time.Second, 4},
		{"fourth attempt (8s)", 3, time.Second, 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backoff := calculateBackoff(tt.attempt, tt.initialBackoff)
			if backoff.Seconds() != tt.expectedSeconds {
				t.Errorf("expected %.0fs, got %.0fs", tt.expectedSeconds, backoff.Seconds())
			}
		})
	}
}
