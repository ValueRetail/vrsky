package io

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// DLQConfig holds Dead Letter Queue configuration
type DLQConfig struct {
	Enabled    bool   // Enable/disable DLQ publishing
	Subject    string // NATS subject for DLQ messages
	MaxRetries int    // Max attempts before sending to DLQ
}

// DefaultDLQConfig returns recommended DLQ settings
func DefaultDLQConfig() DLQConfig {
	return DLQConfig{
		Enabled:    true,
		Subject:    "postgres.dlq",
		MaxRetries: 3,
	}
}

// DLQMessage represents a message sent to Dead Letter Queue
type DLQMessage struct {
	// Original envelope that failed
	Envelope *envelope.Envelope `json:"envelope"`

	// Error information
	ErrorMessage string `json:"error_message"`
	ErrorType    string `json:"error_type"` // "parse", "execute", "connection", etc.
	Timestamp    time.Time `json:"timestamp"`

	// Retry information
	AttemptCount int    `json:"attempt_count"`
	MaxAttempts  int    `json:"max_attempts"`
	Source       string `json:"source"` // "consumer" or "producer"

	// Context
	Table     string `json:"table,omitempty"`      // Table being processed
	Operation string `json:"operation,omitempty"` // INSERT, UPDATE, DELETE
	LSN       uint64 `json:"lsn,omitempty"`       // PostgreSQL LSN (consumer only)
}

// DLQPublisher handles publishing to Dead Letter Queue
type DLQPublisher struct {
	natsConn *nats.Conn
	config   DLQConfig
	logger   *slog.Logger
}

// NewDLQPublisher creates a new DLQ publisher
func NewDLQPublisher(natsConn *nats.Conn, config DLQConfig, logger *slog.Logger) *DLQPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &DLQPublisher{
		natsConn: natsConn,
		config:   config,
		logger:   logger,
	}
}

// PublishConsumerError publishes a consumer error to DLQ
func (dlq *DLQPublisher) PublishConsumerError(
	env *envelope.Envelope,
	errType string,
	errMsg string,
	attempt int,
	table string,
	operation string,
	lsn uint64,
) error {
	if !dlq.config.Enabled || dlq.natsConn == nil {
		return nil // DLQ disabled or no NATS connection
	}

	msg := DLQMessage{
		Envelope:     env,
		ErrorMessage: errMsg,
		ErrorType:    errType,
		Timestamp:    time.Now(),
		AttemptCount: attempt,
		MaxAttempts:  dlq.config.MaxRetries,
		Source:       "consumer",
		Table:        table,
		Operation:    operation,
		LSN:          lsn,
	}

	return dlq.publish(&msg)
}

// PublishProducerError publishes a producer error to DLQ
func (dlq *DLQPublisher) PublishProducerError(
	env *envelope.Envelope,
	errType string,
	errMsg string,
	attempt int,
	table string,
	operation string,
) error {
	if !dlq.config.Enabled || dlq.natsConn == nil {
		return nil // DLQ disabled or no NATS connection
	}

	msg := DLQMessage{
		Envelope:     env,
		ErrorMessage: errMsg,
		ErrorType:    errType,
		Timestamp:    time.Now(),
		AttemptCount: attempt,
		MaxAttempts:  dlq.config.MaxRetries,
		Source:       "producer",
		Table:        table,
		Operation:    operation,
	}

	return dlq.publish(&msg)
}

// publish sends a DLQ message to NATS
func (dlq *DLQPublisher) publish(msg *DLQMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		dlq.logger.Error("Failed to marshal DLQ message", "error", err)
		return err
	}

	if err := dlq.natsConn.Publish(dlq.config.Subject, data); err != nil {
		dlq.logger.Error("Failed to publish to DLQ",
			"subject", dlq.config.Subject,
			"error", err,
			"source", msg.Source,
			"error_type", msg.ErrorType)
		return err
	}

	dlq.logger.Warn("Message sent to DLQ",
		"subject", dlq.config.Subject,
		"source", msg.Source,
		"error_type", msg.ErrorType,
		"attempt", msg.AttemptCount,
		"table", msg.Table,
		"operation", msg.Operation,
		"error", msg.ErrorMessage)

	return nil
}

// IsMaxRetriesExhausted checks if retry count has been exceeded
func (dlq *DLQPublisher) IsMaxRetriesExhausted(attempt int) bool {
	return attempt >= dlq.config.MaxRetries
}

// GetDLQStreamName returns the NATS JetStream stream name for DLQ
func (dlq *DLQPublisher) GetDLQStreamName() string {
	return fmt.Sprintf("DLQ_%s", dlq.config.Subject)
}
