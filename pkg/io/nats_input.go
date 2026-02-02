package io

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// NATSInputConfig defines the configuration for NATS Input
type NATSInputConfig struct {
	URL     string `json:"url"`              // NATS server URL
	Topic   string `json:"topic"`            // Topic pattern to subscribe to
	Timeout int    `json:"timeout,omitempty"` // Connection timeout in seconds (default: 30)
}

// NATSInput implements the Input interface for NATS subscriptions
type NATSInput struct {
	config      NATSInputConfig
	conn        *nats.Conn
	sub         *nats.Subscription
	msgChan     chan *nats.Msg
	mu          sync.RWMutex
	isConnected bool
}

// NewNATSInput creates a new NATS input from JSON configuration
func NewNATSInput(configJSON json.RawMessage) (*NATSInput, error) {
	config := NATSInputConfig{
		Timeout: 30, // Default timeout
	}

	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, fmt.Errorf("failed to parse NATS input config: %w", err)
	}

	if config.URL == "" {
		return nil, fmt.Errorf("NATS URL is required")
	}
	if config.Topic == "" {
		return nil, fmt.Errorf("NATS topic is required")
	}

	return &NATSInput{
		config:  config,
		msgChan: make(chan *nats.Msg, 100),
	}, nil
}

// Read retrieves the next message from NATS and wraps it in an Envelope
func (n *NATSInput) Read(ctx context.Context) (*envelope.Envelope, error) {
	n.mu.Lock()
	if !n.isConnected {
		n.mu.Unlock()
		return nil, fmt.Errorf("NATS not connected")
	}
	n.mu.Unlock()

	select {
	case msg := <-n.msgChan:
		env := envelope.New()
		env.ID = uuid.New().String()
		env.Payload = msg.Data
		env.ContentType = "application/octet-stream"
		env.Source = "nats"
		env.StepHistory = append(env.StepHistory, "nats-input:"+msg.Subject)

		slog.Info("Received message from NATS",
			"id", env.ID,
			"subject", msg.Subject,
			"size", len(msg.Data))

		return env, nil

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close gracefully shuts down the NATS subscription and connection
func (n *NATSInput) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	close(n.msgChan)

	if n.sub != nil {
		if err := n.sub.Unsubscribe(); err != nil {
			slog.Error("Failed to unsubscribe from NATS", "error", err)
		}
	}

	if n.conn != nil {
		n.conn.Close()
	}

	n.isConnected = false
	return nil
}

// Start connects to NATS and subscribes to the topic pattern
func (n *NATSInput) Start(ctx context.Context) error {
	timeout := time.Duration(n.config.Timeout) * time.Second

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := []nats.Option{
		nats.Name("VRSky-NATS-Input"),
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

	// Subscribe to the topic pattern (supports wildcards like "test.1.*")
	sub, err := conn.Subscribe(n.config.Topic, func(msg *nats.Msg) {
		select {
		case n.msgChan <- msg:
		case <-ctx.Done():
			return
		}
	})
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to subscribe to topic %s: %w", n.config.Topic, err)
	}

	n.mu.Lock()
	n.conn = conn
	n.sub = sub
	n.isConnected = true
	n.mu.Unlock()

	slog.Info("Connected to NATS",
		"url", n.config.URL,
		"topic", n.config.Topic)

	return nil
}
