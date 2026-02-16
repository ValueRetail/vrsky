package filter

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// RejectionHandler manages rejected messages
type RejectionHandler struct {
	logger         *slog.Logger
	natsConn       *nats.Conn
	rejectionTopic string
	dlqTopic       string
	maxRetries     int
	backoffConfig  BackoffConfig
}

// NewRejectionHandler creates a new rejection handler
func NewRejectionHandler(
	natsConn *nats.Conn,
	rejectionTopic string,
	dlqTopic string,
	logger *slog.Logger,
) *RejectionHandler {
	if logger == nil {
		logger = slog.Default()
	}

	return &RejectionHandler{
		logger:         logger,
		natsConn:       natsConn,
		rejectionTopic: rejectionTopic,
		dlqTopic:       dlqTopic,
		maxRetries:     3,
		backoffConfig:  DefaultBackoffConfig(),
	}
}

// HandleRejection handles a rejected message
// It publishes to the rejection topic with retry logic
func (rh *RejectionHandler) HandleRejection(
	ctx context.Context,
	env *envelope.Envelope,
	reason string,
	ruleID string,
) error {
	// Add rejection metadata to envelope
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}
	env.Metadata["rejected_at"] = time.Now()
	env.Metadata["rejection_reason"] = reason
	env.Metadata["rejected_by_rule"] = ruleID

	// Publish with retry logic
	return rh.publishWithRetry(ctx, env)
}

// publishWithRetry publishes a message with exponential backoff retry
func (rh *RejectionHandler) publishWithRetry(ctx context.Context, env *envelope.Envelope) error {
	data, err := envelope.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < rh.maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Try to publish
		err := rh.natsConn.Publish(rh.rejectionTopic, data)
		if err == nil {
			return nil
		}

		lastErr = err
		rh.logger.WarnContext(ctx, "Failed to publish rejection",
			"envelope_id", env.ID,
			"attempt", attempt+1,
			"error", err,
		)

		// Wait before retry
		if attempt < rh.maxRetries-1 {
			backoffDelay := rh.backoffConfig.CalculateBackoffDelay(attempt)
			select {
			case <-time.After(backoffDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// All retries failed - try DLQ if different from rejection topic
	if rh.dlqTopic != rh.rejectionTopic {
		rh.logger.ErrorContext(ctx, "Failed to publish to rejection topic, trying DLQ",
			"envelope_id", env.ID,
			"error", lastErr,
		)

		dlqErr := rh.natsConn.Publish(rh.dlqTopic, data)
		if dlqErr != nil {
			return fmt.Errorf("failed to publish to both rejection and dlq: rejection=%w, dlq=%w", lastErr, dlqErr)
		}
		return nil
	}

	return fmt.Errorf("failed to publish rejection after %d attempts: %w", rh.maxRetries, lastErr)
}

// DeadLetterQueue handles dead letter queue operations
type DeadLetterQueue struct {
	logger        *slog.Logger
	natsConn      *nats.Conn
	dlqTopic      string
	maxRetries    int
	backoffConfig BackoffConfig
}

// NewDeadLetterQueue creates a new DLQ handler
func NewDeadLetterQueue(
	natsConn *nats.Conn,
	dlqTopic string,
	logger *slog.Logger,
) *DeadLetterQueue {
	if logger == nil {
		logger = slog.Default()
	}

	return &DeadLetterQueue{
		logger:        logger,
		natsConn:      natsConn,
		dlqTopic:      dlqTopic,
		maxRetries:    3,
		backoffConfig: DefaultBackoffConfig(),
	}
}

// PublishMessage publishes a message to the DLQ
func (dlq *DeadLetterQueue) PublishMessage(ctx context.Context, env *envelope.Envelope, reason string) error {
	// Mark as dead lettered
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}
	env.Metadata["dlq_timestamp"] = time.Now()
	env.Metadata["dlq_reason"] = reason
	env.Metadata["retry_count"] = env.RetryCount

	data, err := envelope.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	// Publish to DLQ
	if err := dlq.natsConn.Publish(dlq.dlqTopic, data); err != nil {
		return fmt.Errorf("publish to dlq: %w", err)
	}

	dlq.logger.InfoContext(ctx, "Message published to DLQ",
		"envelope_id", env.ID,
		"reason", reason,
		"retry_count", env.RetryCount,
	)

	return nil
}

// ErrorRecovery handles error recovery and routing
type ErrorRecovery struct {
	logger           *slog.Logger
	rejectionHandler *RejectionHandler
	dlq              *DeadLetterQueue
	backoffConfig    BackoffConfig
}

// NewErrorRecovery creates a new error recovery handler
func NewErrorRecovery(
	rejectionHandler *RejectionHandler,
	dlq *DeadLetterQueue,
	logger *slog.Logger,
) *ErrorRecovery {
	if logger == nil {
		logger = slog.Default()
	}

	return &ErrorRecovery{
		logger:           logger,
		rejectionHandler: rejectionHandler,
		dlq:              dlq,
		backoffConfig:    DefaultBackoffConfig(),
	}
}

// HandleProcessingError handles errors during message processing
func (er *ErrorRecovery) HandleProcessingError(
	ctx context.Context,
	env *envelope.Envelope,
	err error,
) error {
	env.LastError = err.Error()
	env.RetryCount++

	// Check if we should retry or send to DLQ
	if env.RetryCount < 3 {
		er.logger.WarnContext(ctx, "Processing error, will retry",
			"envelope_id", env.ID,
			"retry_count", env.RetryCount,
			"error", err,
		)
		return nil
	}

	// Max retries exceeded - send to DLQ
	er.logger.ErrorContext(ctx, "Max retries exceeded, sending to DLQ",
		"envelope_id", env.ID,
		"error", err,
	)

	return er.dlq.PublishMessage(ctx, env, fmt.Sprintf("Processing error: %v", err))
}

// HandleParsingError handles JSON/XML parsing errors
func (er *ErrorRecovery) HandleParsingError(
	ctx context.Context,
	env *envelope.Envelope,
	err error,
) error {
	reason := fmt.Sprintf("Parsing error: %v", err)
	er.logger.WarnContext(ctx, "Parsing error",
		"envelope_id", env.ID,
		"content_type", env.ContentType,
		"error", err,
	)

	return er.rejectionHandler.HandleRejection(ctx, env, reason, "parsing_error")
}

// HandleValidationError handles schema/content validation errors
func (er *ErrorRecovery) HandleValidationError(
	ctx context.Context,
	env *envelope.Envelope,
	schemaID string,
	err error,
) error {
	reason := fmt.Sprintf("Validation error (schema=%s): %v", schemaID, err)
	er.logger.WarnContext(ctx, "Validation error",
		"envelope_id", env.ID,
		"schema_id", schemaID,
		"error", err,
	)

	return er.rejectionHandler.HandleRejection(ctx, env, reason, schemaID)
}
