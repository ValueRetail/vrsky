package io

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// NATSOutputConfig defines the configuration for NATS Output
type NATSOutputConfig struct {
	URL     string `json:"url"`               // NATS server URL
	Subject string `json:"subject"`           // Subject to publish to
	Timeout int    `json:"timeout,omitempty"` // Connection timeout in seconds (default: 30)
}

// NATSOutput implements the Output interface for NATS publishing
type NATSOutput struct {
	config      NATSOutputConfig
	conn        *nats.Conn
	mu          sync.RWMutex
	isConnected bool
}

// NewNATSOutput creates a new NATS output from JSON configuration
func NewNATSOutput(configJSON json.RawMessage) (*NATSOutput, error) {
	config := NATSOutputConfig{
		Timeout: 30, // Default timeout
	}

	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, fmt.Errorf("failed to parse NATS output config: %w", err)
	}

	if config.URL == "" {
		return nil, fmt.Errorf("NATS URL is required")
	}
	if config.Subject == "" {
		return nil, fmt.Errorf("NATS subject is required")
	}

	return &NATSOutput{
		config: config,
	}, nil
}

// Start connects to the NATS server
func (n *NATSOutput) Start(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.isConnected {
		return fmt.Errorf("NATS output already connected")
	}

	timeout := time.Duration(n.config.Timeout) * time.Second
	opts := []nats.Option{
		nats.Timeout(timeout),
		nats.Name("VRSky-NATS-Output"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1), // Infinite reconnect attempts
		nats.DisconnectHandler(func(nc *nats.Conn) {
			slog.Warn("NATS disconnected", "url", n.config.URL)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			slog.Info("NATS reconnected", "url", n.config.URL)
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			slog.Info("NATS connection closed")
		}),
	}

	conn, err := nats.Connect(n.config.URL, opts...)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS at %s: %w", n.config.URL, err)
	}

	n.conn = conn
	n.isConnected = true

	slog.Info("Connected to NATS for output",
		"url", n.config.URL,
		"subject", n.config.Subject)

	return nil
}

// Write publishes an envelope to the NATS subject
func (n *NATSOutput) Write(ctx context.Context, env *envelope.Envelope) error {
	n.mu.RLock()
	if !n.isConnected || n.conn == nil {
		n.mu.RUnlock()
		return fmt.Errorf("NATS not connected")
	}
	conn := n.conn
	n.mu.RUnlock()

	// Serialize envelope to JSON
	envJSON, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}

	slog.Debug("Publishing to NATS",
		"subject", n.config.Subject,
		"message_id", env.ID,
		"size", len(envJSON))

	// Create NATS message with headers
	msg := &nats.Msg{
		Subject: n.config.Subject,
		Data:    envJSON,
	}

	// Add headers (X-Message-ID for tracking)
	msg.Reply = "" // No reply expected

	if err := conn.PublishMsg(msg); err != nil {
		slog.Error("Failed to publish to NATS",
			"subject", n.config.Subject,
			"message_id", env.ID,
			"error", err)
		return fmt.Errorf("failed to publish to NATS subject %s: %w", n.config.Subject, err)
	}

	// Per-message logging stays at Debug: this is a data-plane hot path, and an
	// Info line per message dominates CPU at real rates.
	slog.Debug("Message published to NATS",
		"subject", n.config.Subject,
		"message_id", env.ID)

	// NB: deliberately no per-message conn.Flush(). Flush is a full round-trip
	// to the server, so flushing per publish serialises the hot path on network
	// latency (measured: it capped end-to-end delivery at a few hundred msg/s
	// while the ingress accepted thousands). The NATS client buffers and flushes
	// asynchronously, and Close() flushes on the way out; core NATS gives no
	// delivery ack either way, so the per-message flush bought no guarantee.
	return nil
}

// Close gracefully shuts down the NATS connection
func (n *NATSOutput) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.conn != nil {
		n.conn.Close()
	}

	n.isConnected = false
	slog.Info("NATS output closed")
	return nil
}
