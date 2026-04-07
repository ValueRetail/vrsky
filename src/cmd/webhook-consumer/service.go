package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// WebhookConsumerService manages webhook consumer pipelines
type WebhookConsumerService struct {
	db     *sql.DB
	nc     *nats.Conn
	logger *slog.Logger
	config *Config
	server *WebhookServer

	// Active connections: connectionId → connection info
	activeConnections map[string]*ActiveConnection
	mu                sync.RWMutex

	// Subscriptions
	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// ActiveConnection tracks a registered webhook connection
type ActiveConnection struct {
	ConnectionID string
	TenantID     string
	Cancel       context.CancelFunc
}

// NewWebhookConsumerService creates a new Webhook Consumer service
func NewWebhookConsumerService(db *sql.DB, nc *nats.Conn, logger *slog.Logger, config *Config) *WebhookConsumerService {
	return &WebhookConsumerService{
		db:                db,
		nc:                nc,
		logger:            logger,
		config:            config,
		activeConnections: make(map[string]*ActiveConnection),
	}
}

// Start initializes the HTTP server and subscribes to NATS commands
func (s *WebhookConsumerService) Start(ctx context.Context) error {
	s.logger.Info("Starting Webhook Consumer Service")

	// Start the shared HTTP server
	s.server = NewWebhookServer(s.config.WebhookPort, s, s.logger)
	if err := s.server.Start(); err != nil {
		return fmt.Errorf("failed to start webhook server: %w", err)
	}

	// Subscribe to start commands
	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to start commands: %w", err)
	}
	s.startSub = startSub

	// Subscribe to stop commands
	stopSub, err := s.nc.Subscribe("vrsky.commands.*.connection.stop", s.handleStopCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to stop commands: %w", err)
	}
	s.stopSub = stopSub

	s.logger.Info("Subscribed to NATS command topics",
		"start_topic", "vrsky.commands.*.connection.start",
		"stop_topic", "vrsky.commands.*.connection.stop")

	return nil
}

// Stop gracefully shuts down the service
func (s *WebhookConsumerService) Stop(ctx context.Context) error {
	s.logger.Info("Stopping Webhook Consumer Service")

	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	// Cancel all active connections
	s.mu.Lock()
	for connId, ac := range s.activeConnections {
		s.logger.Info("Unregistering webhook", "connection_id", connId)
		ac.Cancel()
	}
	s.activeConnections = make(map[string]*ActiveConnection)
	s.mu.Unlock()

	// Stop HTTP server
	if s.server != nil {
		s.server.Stop()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}

// CommandMessage represents a start/stop command from NATS
type CommandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

// handleStartCommand processes a start command from NATS
func (s *WebhookConsumerService) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received start command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	// Check if already active
	s.mu.RLock()
	_, exists := s.activeConnections[cmd.ConnectionID]
	s.mu.RUnlock()

	if exists {
		s.logger.Warn("Webhook already registered", "connection_id", cmd.ConnectionID)
		return
	}

	// Fetch connection to verify it's a webhook consumer
	conn, err := s.getConnection(cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		s.logger.Error("Failed to fetch connection", "error", err, "connection_id", cmd.ConnectionID)
		return
	}

	// Check if this connection has a webhook consumer node
	if !s.hasWebhookConsumer(conn) {
		s.logger.Debug("Not a webhook consumer, ignoring", "connection_id", cmd.ConnectionID)
		return
	}

	// Register the webhook handler
	_, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.activeConnections[cmd.ConnectionID] = &ActiveConnection{
		ConnectionID: cmd.ConnectionID,
		TenantID:     cmd.TenantID,
		Cancel:       cancel,
	}
	s.mu.Unlock()

	// Update connection status
	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	s.logger.Info("Webhook registered",
		"connection_id", cmd.ConnectionID,
		"url", fmt.Sprintf("http://localhost:%s/webhook/%s", s.config.WebhookPort, cmd.ConnectionID))
}

// handleStopCommand processes a stop command from NATS
func (s *WebhookConsumerService) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received stop command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	s.mu.Lock()
	ac, exists := s.activeConnections[cmd.ConnectionID]
	if exists {
		ac.Cancel()
		delete(s.activeConnections, cmd.ConnectionID)
	}
	s.mu.Unlock()

	if !exists {
		s.logger.Warn("Webhook not registered", "connection_id", cmd.ConnectionID)
		return
	}

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	s.logger.Info("Webhook unregistered", "connection_id", cmd.ConnectionID)
}

// getActiveConnection returns the active connection info for a given connection ID
func (s *WebhookConsumerService) getActiveConnection(connectionID string) *ActiveConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeConnections[connectionID]
}

// Connection represents a pipeline connection from the database
type Connection struct {
	ID       string
	TenantID string
	Name     string
	Nodes    json.RawMessage
	Edges    json.RawMessage
}

// getConnection fetches a connection from the database
func (s *WebhookConsumerService) getConnection(connectionID, tenantID string) (*Connection, error) {
	query := `
		SELECT id, tenant_id, name, nodes, edges
		FROM connections
		WHERE id = $1 AND tenant_id = $2
	`

	var conn Connection
	err := s.db.QueryRow(query, connectionID, tenantID).Scan(
		&conn.ID,
		&conn.TenantID,
		&conn.Name,
		&conn.Nodes,
		&conn.Edges,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("connection not found: %s", connectionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query connection: %w", err)
	}

	return &conn, nil
}

// Node represents a pipeline node
type Node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// hasWebhookConsumer checks if the connection has an HTTP webhook consumer node
func (s *WebhookConsumerService) hasWebhookConsumer(conn *Connection) bool {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		s.logger.Warn("Failed to parse nodes", "error", err)
		return false
	}

	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}

		// Check config.type == "http"
		var config map[string]json.RawMessage
		if err := json.Unmarshal(node.Config, &config); err != nil {
			continue
		}

		if typeRaw, ok := config["type"]; ok {
			var configType string
			if json.Unmarshal(typeRaw, &configType) == nil && configType == "http" {
				return true
			}
		}
	}

	return false
}

// updateConnectionStatus updates the connection status in the database
func (s *WebhookConsumerService) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `
			UPDATE connections
			SET status = $1, started_at = NOW(), updated_at = NOW()
			WHERE id = $2 AND tenant_id = $3
		`
	} else {
		query = `
			UPDATE connections
			SET status = $1, stopped_at = NOW(), updated_at = NOW()
			WHERE id = $2 AND tenant_id = $3
		`
	}

	_, err := s.db.Exec(query, status, connectionID, tenantID)
	if err != nil {
		return fmt.Errorf("failed to update connection status: %w", err)
	}

	s.logger.Info("Updated connection status", "connection_id", connectionID, "status", status)
	return nil
}
