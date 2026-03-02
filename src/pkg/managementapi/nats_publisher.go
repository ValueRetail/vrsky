package managementapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSPublisher handles publishing commands to NATS for pipeline components
type NATSPublisher struct {
	nc     *nats.Conn
	logger *log.Logger
}

// NewNATSPublisher creates a new NATS publisher
func NewNATSPublisher(nc *nats.Conn, logger *log.Logger) *NATSPublisher {
	if logger == nil {
		logger = log.New(io.Discard, "", 0) // Silence logger if not provided
	}
	return &NATSPublisher{
		nc:     nc,
		logger: logger,
	}
}

// ConnectionCommand represents a command to control a connection
type ConnectionCommand struct {
	Type         string      `json:"type"` // "create", "start", "stop", "delete"
	ConnectionID string      `json:"connection_id"`
	TenantID     string      `json:"tenant_id"`
	Connection   *Connection `json:"connection,omitempty"`
	Timestamp    time.Time   `json:"timestamp"`
}

// TestMessageCommand represents a test data message to inject into the pipeline
type TestMessageCommand struct {
	ConnectionID string      `json:"connection_id"`
	TenantID     string      `json:"tenant_id"`
	Payload      interface{} `json:"payload"` // JSON payload
	Timestamp    time.Time   `json:"timestamp"`
}

// MetricsPublish represents metrics published by pipeline components
type MetricsPublish struct {
	ConnectionID    string                 `json:"connection_id"`
	TenantID        string                 `json:"tenant_id"`
	ComponentType   string                 `json:"component_type"` // consumer, converter, filter, producer
	MessagesIn      int64                  `json:"messages_in"`
	MessagesOut     int64                  `json:"messages_out"`
	ErrorCount      int64                  `json:"error_count"`
	LastMessageTime time.Time              `json:"last_message_time"`
	AvgLatencyMs    float64                `json:"avg_latency_ms"`
	Details         map[string]interface{} `json:"details,omitempty"`
	Timestamp       time.Time              `json:"timestamp"`
}

// PublishConnectionCreate publishes a connection creation command
// Subject: vrsky.commands.{tenantID}.connection.create
func (p *NATSPublisher) PublishConnectionCreate(ctx context.Context, conn *Connection) error {
	cmd := ConnectionCommand{
		Type:         "create",
		ConnectionID: conn.ID,
		TenantID:     conn.TenantID,
		Connection:   conn,
		Timestamp:    time.Now().UTC(),
	}

	return p.publishCommand(ctx, conn.TenantID, "create", cmd)
}

// PublishConnectionStart publishes a connection start command
// Subject: vrsky.commands.{tenantID}.connection.start
func (p *NATSPublisher) PublishConnectionStart(ctx context.Context, connID, tenantID string) error {
	cmd := ConnectionCommand{
		Type:         "start",
		ConnectionID: connID,
		TenantID:     tenantID,
		Timestamp:    time.Now().UTC(),
	}

	return p.publishCommand(ctx, tenantID, "start", cmd)
}

// PublishConnectionStop publishes a connection stop command
// Subject: vrsky.commands.{tenantID}.connection.stop
func (p *NATSPublisher) PublishConnectionStop(ctx context.Context, connID, tenantID string) error {
	cmd := ConnectionCommand{
		Type:         "stop",
		ConnectionID: connID,
		TenantID:     tenantID,
		Timestamp:    time.Now().UTC(),
	}

	return p.publishCommand(ctx, tenantID, "stop", cmd)
}

// PublishConnectionDelete publishes a connection deletion command
// Subject: vrsky.commands.{tenantID}.connection.delete
func (p *NATSPublisher) PublishConnectionDelete(ctx context.Context, connID, tenantID string) error {
	cmd := ConnectionCommand{
		Type:         "delete",
		ConnectionID: connID,
		TenantID:     tenantID,
		Timestamp:    time.Now().UTC(),
	}

	return p.publishCommand(ctx, tenantID, "delete", cmd)
}

// PublishTestMessage publishes a test message to the pipeline
// Subject: vrsky.test.{tenantID}.{connectionID}
func (p *NATSPublisher) PublishTestMessage(ctx context.Context, connID, tenantID string, payload interface{}) error {
	msg := TestMessageCommand{
		ConnectionID: connID,
		TenantID:     tenantID,
		Payload:      payload,
		Timestamp:    time.Now().UTC(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		p.logger.Printf("Failed to marshal test message: %v", err)
		return fmt.Errorf("failed to marshal test message: %w", err)
	}

	subject := fmt.Sprintf("vrsky.test.%s.%s", tenantID, connID)

	// Publish with context timeout
	done := make(chan error, 1)
	go func() {
		done <- p.nc.Publish(subject, data)
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("publish context cancelled")
	case err := <-done:
		if err != nil {
			p.logger.Printf("Failed to publish test message to %s: %v", subject, err)
			return fmt.Errorf("failed to publish test message: %w", err)
		}
		p.logger.Printf("Published test message to %s", subject)
		return nil
	}
}

// publishCommand is a helper to publish connection commands with retry logic
func (p *NATSPublisher) publishCommand(ctx context.Context, tenantID, cmdType string, cmd ConnectionCommand) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		p.logger.Printf("Failed to marshal command: %v", err)
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	subject := fmt.Sprintf("vrsky.commands.%s.connection.%s", tenantID, cmdType)

	// Retry logic with exponential backoff
	const maxRetries = 3
	const initialBackoff = 100 * time.Millisecond
	const publishTimeout = 5 * time.Second

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Check context before attempt
		select {
		case <-ctx.Done():
			return fmt.Errorf("publish context cancelled")
		default:
		}

		done := make(chan error, 1)
		go func() {
			done <- p.nc.Publish(subject, data)
		}()

		// Create timer for publish timeout
		timer := time.NewTimer(publishTimeout)

		// Wait for publish with timeout
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("publish context cancelled")
		case err := <-done:
			if !timer.Stop() {
				<-timer.C
			}
			if err == nil {
				p.logger.Printf("Published %s command to %s", cmdType, subject)
				return nil
			}

			lastErr = err
			if attempt < maxRetries-1 {
				backoff := initialBackoff * time.Duration(1<<uint(attempt))
				p.logger.Printf("Publish attempt %d failed: %v, retrying in %v", attempt+1, err, backoff)
				time.Sleep(backoff)
			}
		case <-timer.C:
			lastErr = fmt.Errorf("publish timeout")
			if attempt < maxRetries-1 {
				p.logger.Printf("Publish attempt %d timeout, retrying", attempt+1)
				time.Sleep(initialBackoff * time.Duration(1<<uint(attempt)))
			}
		}
	}

	p.logger.Printf("Failed to publish %s command after %d attempts: %v", cmdType, maxRetries, lastErr)
	return fmt.Errorf("failed to publish command after %d attempts: %w", maxRetries, lastErr)
}

// HealthCheck checks if the NATS connection is healthy
func (p *NATSPublisher) HealthCheck() error {
	if !p.nc.IsConnected() {
		return fmt.Errorf("NATS connection is not active")
	}
	return nil
}
