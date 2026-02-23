package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/nats-io/nats.go"
)

// slogLoggerAdapter adapts slog.Logger to our Logger interface
type slogLoggerAdapter struct {
	logger *slog.Logger
}

func (a *slogLoggerAdapter) InfoContext(ctx context.Context, msg string, args ...interface{}) {
	a.logger.InfoContext(ctx, msg, args...)
}

func (a *slogLoggerAdapter) WarnContext(ctx context.Context, msg string, args ...interface{}) {
	a.logger.WarnContext(ctx, msg, args...)
}

func (a *slogLoggerAdapter) ErrorContext(ctx context.Context, msg string, args ...interface{}) {
	a.logger.ErrorContext(ctx, msg, args...)
}

func (a *slogLoggerAdapter) Warn(msg string) {
	a.logger.Warn(msg)
}

func (a *slogLoggerAdapter) Error(msg string) {
	a.logger.Error(msg)
}

// ConverterImpl is the converter component implementation
type ConverterImpl struct {
	config   *ConverterConfig
	natsConn *nats.Conn
	logger   *slog.Logger
	metrics  *Metrics

	// Phase 2F: Rule engine components
	fieldMapper         *FieldMapper
	expressionEvaluator *ExpressionEvaluator
	functionRegistry    *FunctionRegistry
	ruleEngine          *RuleEngine

	mu           sync.RWMutex
	closed       bool
	ctx          context.Context
	cancel       context.CancelFunc
	subscription *nats.Subscription

	wg sync.WaitGroup
}

// NewConverter creates a new converter instance
// It loads configuration from config service (fail-fast if unreachable)
// and validates all dependencies
func NewConverter(
	ctx context.Context,
	converterID string,
	tenantID string,
	natsConn *nats.Conn,
	logger *slog.Logger,
) (*ConverterImpl, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	if natsConn == nil {
		return nil, fmt.Errorf("nats connection is required")
	}

	// Load config from config service (fail-fast)
	logger.InfoContext(ctx, "Loading converter config", "converter_id", converterID, "tenant_id", tenantID)
	config, err := LoadConfig(ctx, tenantID, converterID, logger)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Initialize metrics
	metrics, err := NewMetrics(converterID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("initialize metrics: %w", err)
	}

	// Create component context
	componentCtx, cancel := context.WithCancel(context.Background())

	// Initialize rule engine components (Phase 2F)
	// Wrap slog.Logger in our Logger interface
	loggerAdapter := &slogLoggerAdapter{logger: logger}

	fieldMapper := NewFieldMapper(componentCtx, loggerAdapter)
	functionRegistry := NewFunctionRegistry(componentCtx, loggerAdapter)
	expressionEvaluator := NewExpressionEvaluator(componentCtx, loggerAdapter, functionRegistry)
	ruleEngine := NewRuleEngine(componentCtx, loggerAdapter, fieldMapper, expressionEvaluator, functionRegistry)

	converter := &ConverterImpl{
		config:              config,
		natsConn:            natsConn,
		logger:              logger,
		metrics:             metrics,
		fieldMapper:         fieldMapper,
		expressionEvaluator: expressionEvaluator,
		functionRegistry:    functionRegistry,
		ruleEngine:          ruleEngine,
		closed:              false,
		ctx:                 componentCtx,
		cancel:              cancel,
	}

	logger.InfoContext(ctx, "Converter created successfully",
		"converter_id", config.ConverterID,
		"tenant_id", config.TenantID,
		"input_topic", config.InputTopic,
		"output_topic", config.OutputTopic,
		"error_topic", config.ErrorTopic,
	)

	return converter, nil
}

// Name returns the converter ID
func (c *ConverterImpl) Name() string {
	return c.config.ConverterID
}

// Type returns the component type
func (c *ConverterImpl) Type() string {
	return string(component.TypeConverter)
}

// Version returns the component version
func (c *ConverterImpl) Version() string {
	return "1.0.0"
}

// Start subscribes to the input topic and starts the message handler
func (c *ConverterImpl) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("converter already closed")
	}

	c.logger.InfoContext(ctx, "Starting converter", "input_topic", c.config.InputTopic)

	// Subscribe to input topic
	sub, err := c.natsConn.Subscribe(c.config.InputTopic, c.handleMessage)
	if err != nil {
		return fmt.Errorf("subscribe to input topic: %w", err)
	}

	c.subscription = sub

	// Start message handler goroutine
	c.wg.Add(1)
	go c.messageHandlerLoop()

	c.logger.InfoContext(ctx, "Converter started successfully", "input_topic", c.config.InputTopic)
	return nil
}

// Stop gracefully shuts down the converter (30s timeout)
func (c *ConverterImpl) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil // Already closed
	}

	c.logger.InfoContext(ctx, "Stopping converter")

	c.closed = true

	// Unsubscribe from NATS
	if c.subscription != nil {
		if err := c.subscription.Unsubscribe(); err != nil {
			c.logger.WarnContext(ctx, "Error unsubscribing", "error", err)
		}
	}

	// Cancel context to signal handler loop to stop
	c.cancel()

	// Wait for goroutines with 30s timeout
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	timeout := 30 * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		c.logger.InfoContext(ctx, "Converter stopped successfully")
		return nil
	case <-timer.C:
		return fmt.Errorf("converter stop timeout after %s", timeout)
	}
}

// Health returns the current health status
func (c *ConverterImpl) Health() component.HealthStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return component.HealthUnhealthy
	}

	// Check NATS connection
	if c.natsConn == nil || c.natsConn.IsClosed() {
		return component.HealthUnhealthy
	}

	if !c.natsConn.IsConnected() {
		return component.HealthUnhealthy
	}

	return component.HealthHealthy
}

// ProcessMessage processes a single message with rule-based transformations
// In Phase 2F, applies configured transformation rules to the message payload
// If no transformations are defined, returns the envelope unchanged for backward compatibility
func (c *ConverterImpl) ProcessMessage(ctx context.Context, env *envelope.Envelope) (*envelope.Envelope, error) {
	start := time.Now()
	c.metrics.RecordMessageReceived()

	// Phase 2F: Rule-based transformation
	// If transformations are defined, apply them to the payload
	if len(c.config.Transformations) > 0 && env.Payload != nil {
		c.logger.DebugContext(ctx, "Processing message with transformations",
			"envelope_id", env.ID,
			"tenant_id", env.TenantID,
			"num_transformations", len(c.config.Transformations),
		)

		// Execute transformations via rule engine
		transformed, err := c.ruleEngine.ExecuteTransformations(env.Payload, c.config.Transformations)
		if err != nil {
			c.logger.ErrorContext(ctx, "Transformation failed",
				"envelope_id", env.ID,
				"error", err.Error(),
			)
			duration := time.Since(start)
			c.metrics.RecordTransformationDuration(duration)
			c.metrics.RecordMessageFailed("transformation")
			return env, err
		}

		// Convert transformed map to JSON and update envelope payload
		transformedJSON, err := json.Marshal(transformed)
		if err != nil {
			c.logger.ErrorContext(ctx, "Failed to marshal transformed output",
				"envelope_id", env.ID,
				"error", err.Error(),
			)
			duration := time.Since(start)
			c.metrics.RecordTransformationDuration(duration)
			c.metrics.RecordMessageFailed("marshal")
			return env, err
		}

		// Update envelope with transformed payload
		env.Payload = transformedJSON
		env.PayloadSize = int64(len(transformedJSON))

		c.logger.DebugContext(ctx, "Message transformed successfully",
			"envelope_id", env.ID,
			"output_fields", len(transformed),
		)
	} else {
		// No transformations defined - pass-through for backward compatibility
		c.logger.DebugContext(ctx, "Processing message (no transformations, pass-through)",
			"envelope_id", env.ID,
			"tenant_id", env.TenantID,
			"content_type", env.ContentType,
		)
	}

	// Record transformation duration
	duration := time.Since(start)
	c.metrics.RecordTransformationDuration(duration)
	c.metrics.RecordMessageSucceeded()

	return env, nil
}

// handleMessage is the NATS message callback
func (c *ConverterImpl) handleMessage(msg *nats.Msg) {
	// Recover from panics
	defer func() {
		if r := recover(); r != nil {
			c.logger.ErrorContext(c.ctx, "Panic in message handler",
				"error", fmt.Sprintf("%v", r),
				"subject", msg.Subject,
			)
		}
	}()

	// Unmarshal envelope
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.logger.ErrorContext(c.ctx, "Failed to unmarshal envelope",
			"error", err,
			"subject", msg.Subject,
		)
		c.metrics.RecordMessageFailed("unmarshal")
		return
	}

	c.logger.DebugContext(c.ctx, "Received message", "envelope_id", env.ID)

	// Process with retries
	err := c.executeWithRetry(func() error {
		return c.processMessageWithPublish(&env)
	})

	if err != nil {
		c.logger.ErrorContext(c.ctx, "Message processing failed after retries",
			"envelope_id", env.ID,
			"error", err,
		)
		c.metrics.RecordMessageFailed("exhausted_retries")

		// Publish to error topic
		c.publishToErrorTopic(&env, err)
	}
}

// processMessageWithPublish processes the message and publishes result
func (c *ConverterImpl) processMessageWithPublish(env *envelope.Envelope) error {
	// Process message
	result, err := c.ProcessMessage(c.ctx, env)
	if err != nil {
		return fmt.Errorf("process message: %w", err)
	}

	// Marshal result
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	// Publish to output topic
	if err := c.natsConn.Publish(c.config.OutputTopic, data); err != nil {
		return fmt.Errorf("publish to output topic: %w", err)
	}

	c.logger.DebugContext(c.ctx, "Message published to output topic",
		"envelope_id", env.ID,
		"output_topic", c.config.OutputTopic,
	)

	return nil
}

// publishToErrorTopic publishes a failed message to the error topic
func (c *ConverterImpl) publishToErrorTopic(env *envelope.Envelope, err error) {
	// Add error metadata
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}
	env.Metadata["error"] = err.Error()
	env.Metadata["error_topic_timestamp"] = time.Now().Unix()

	// Marshal
	data, err := json.Marshal(env)
	if err != nil {
		c.logger.ErrorContext(c.ctx, "Failed to marshal envelope for error topic",
			"envelope_id", env.ID,
			"error", err,
		)
		return
	}

	// Publish to error topic
	if err := c.natsConn.Publish(c.config.ErrorTopic, data); err != nil {
		c.logger.ErrorContext(c.ctx, "Failed to publish to error topic",
			"envelope_id", env.ID,
			"error_topic", c.config.ErrorTopic,
			"error", err,
		)
		return
	}

	c.logger.DebugContext(c.ctx, "Message published to error topic",
		"envelope_id", env.ID,
		"error_topic", c.config.ErrorTopic,
	)
}

// executeWithRetry executes a function with exponential backoff retry
// Attempts: 1 immediate, 2 after 1s, 3 after 2s (doubling each time)
func (c *ConverterImpl) executeWithRetry(fn func() error) error {
	maxAttempts := c.config.MaxRetries
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Wait before retry (except first attempt)
		if attempt > 1 {
			backoffMs := int(math.Pow(2, float64(attempt-2))) * 1000 // 1s, 2s, 4s...
			backoff := time.Duration(backoffMs) * time.Millisecond
			time.Sleep(backoff)
		}

		c.metrics.RecordRetryAttempt(attempt)

		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err
		c.logger.WarnContext(c.ctx, "Attempt failed",
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"error", err,
		)
	}

	return fmt.Errorf("failed after %d attempts: %w", maxAttempts, lastErr)
}

// messageHandlerLoop runs the message processing loop
func (c *ConverterImpl) messageHandlerLoop() {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			c.logger.ErrorContext(c.ctx, "Panic in message handler loop",
				"error", fmt.Sprintf("%v", r),
			)
		}
	}()

	// Loop until context cancelled
	<-c.ctx.Done()
	c.logger.InfoContext(c.ctx, "Message handler loop stopping")
}
