package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/nats-io/nats.go"
)

// APIConsumerService manages API consumer pipelines
type APIConsumerService struct {
	db     *sql.DB
	nc     *nats.Conn
	pub    *messaging.Publisher // JetStream publisher for data-flow (#70)
	logger *slog.Logger
	config *Config

	// Active pipelines: connectionId → cancel function
	activePipelines map[string]context.CancelFunc
	mu              sync.RWMutex

	// Subscriptions
	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// NewAPIConsumerService creates a new API Consumer service
func NewAPIConsumerService(db *sql.DB, nc *nats.Conn, logger *slog.Logger, config *Config) *APIConsumerService {
	js, err := nc.JetStream()
	if err != nil {
		logger.Error("Failed to get JetStream context", "error", err)
	}
	var pub *messaging.Publisher
	if js != nil {
		pub = messaging.NewPublisher(js)
	}
	return &APIConsumerService{
		db:              db,
		nc:              nc,
		pub:             pub,
		logger:          logger,
		config:          config,
		activePipelines: make(map[string]context.CancelFunc),
	}
}

// Start initializes the service and subscribes to NATS commands
func (s *APIConsumerService) Start(ctx context.Context) error {
	s.logger.Info("Starting API Consumer Service subscriptions")

	// Subscribe to start commands
	// Pattern: vrsky.commands.*.connection.start (tenant ID is the wildcard)
	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to start commands: %w", err)
	}
	s.startSub = startSub

	// Subscribe to stop commands
	// Pattern: vrsky.commands.*.connection.stop (tenant ID is the wildcard)
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

// Stop gracefully shuts down all active pipelines
func (s *APIConsumerService) Stop(ctx context.Context) error {
	s.logger.Info("Stopping API Consumer Service")

	// Unsubscribe from NATS topics
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	// Cancel all active pipelines
	s.mu.Lock()
	for connId, cancel := range s.activePipelines {
		s.logger.Info("Stopping pipeline", "connection_id", connId)
		cancel()
	}
	s.activePipelines = make(map[string]context.CancelFunc)
	s.mu.Unlock()

	// Wait a moment for graceful shutdown
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
func (s *APIConsumerService) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received start command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	// Check if already running
	s.mu.RLock()
	_, exists := s.activePipelines[cmd.ConnectionID]
	s.mu.RUnlock()

	if exists {
		s.logger.Warn("Pipeline already running", "connection_id", cmd.ConnectionID)
		return
	}

	// Fetch connection config from database
	conn, err := s.getConnection(cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		s.logger.Error("Failed to fetch connection", "error", err, "connection_id", cmd.ConnectionID)
		return
	}

	// Extract API Consumer node config
	apiConfig, err := s.extractAPIConsumerConfig(conn)
	if err != nil {
		s.logger.Error("Failed to extract API Consumer config", "error", err, "connection_id", cmd.ConnectionID)
		return
	}

	// Create context with cancellation for this pipeline
	ctx, cancel := context.WithCancel(context.Background())

	// Store the cancel function
	s.mu.Lock()
	s.activePipelines[cmd.ConnectionID] = cancel
	s.mu.Unlock()

	// Update connection status to running
	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	// Start polling in a goroutine
	go s.pollConnection(ctx, cmd.ConnectionID, cmd.TenantID, apiConfig)
}

// handleStopCommand processes a stop command from NATS
func (s *APIConsumerService) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received stop command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	// Find and cancel the pipeline
	s.mu.Lock()
	cancel, exists := s.activePipelines[cmd.ConnectionID]
	if exists {
		cancel()
		delete(s.activePipelines, cmd.ConnectionID)
	}
	s.mu.Unlock()

	if !exists {
		s.logger.Warn("Pipeline not running", "connection_id", cmd.ConnectionID)
		return
	}

	// Update connection status to stopped
	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	s.logger.Info("Pipeline stopped", "connection_id", cmd.ConnectionID)
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
func (s *APIConsumerService) getConnection(connectionID, tenantID string) (*Connection, error) {
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

// APIConsumerConfig represents the API Consumer node configuration
type APIConsumerConfig struct {
	BaseURL             string        `json:"base_url"`
	Endpoints           []APIEndpoint `json:"endpoints"`
	PollIntervalSeconds int           `json:"poll_interval_seconds"`
	OneTimeOnly         bool          `json:"one_time_only"` // If true, retrieve data once and stop (no polling)
}

// APIEndpoint represents a single API endpoint configuration
type APIEndpoint struct {
	Path      string `json:"path"`
	Params    string `json:"params"`     // Query parameters (e.g., "lat=59.9&lon=10.7")
	AuthType  string `json:"auth_type"`  // "none", "bearer", "api_key"
	AuthValue string `json:"auth_value"` // Token or API key (may be encrypted)
}

// Node represents a pipeline node
type Node struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

// NodeConfig wraps the type-specific configuration
// The config structure is: {"api": {...}} or {"file": {...}}, etc.
type NodeConfig struct {
	Type string             `json:"type"`
	API  *APIConsumerConfig `json:"api"`
}

// extractAPIConsumerConfig extracts API Consumer config from connection nodes
func (s *APIConsumerService) extractAPIConsumerConfig(conn *Connection) (*APIConsumerConfig, error) {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return nil, fmt.Errorf("failed to parse nodes: %w", err)
	}

	// Find the consumer node with API config
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}

		// Parse the node config
		var nodeConfig NodeConfig
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			s.logger.Warn("Failed to parse node config", "node_id", node.ID, "error", err)
			continue
		}

		// Only pick up nodes explicitly typed as "api" — stale `api` blobs from
		// other consumer types (e.g. http/webhook) must be ignored.
		if nodeConfig.Type != "api" || nodeConfig.API == nil {
			s.logger.Debug("Node is not an API consumer", "node_id", node.ID, "type", nodeConfig.Type)
			continue
		}

		// Use the parsed API config directly
		apiConfig := nodeConfig.API

		// Default to a single "/" endpoint if none configured
		if len(apiConfig.Endpoints) == 0 {
			apiConfig.Endpoints = []APIEndpoint{{Path: "/", AuthType: "none"}}
		}

		// Set default poll interval if not specified
		if apiConfig.PollIntervalSeconds <= 0 {
			apiConfig.PollIntervalSeconds = int(s.config.DefaultPollInterval.Seconds())
		}

		// Decrypt auth tokens if needed
		for i := range apiConfig.Endpoints {
			if apiConfig.Endpoints[i].AuthValue != "" && s.config.EncryptionKey != "" {
				decrypted, err := DecryptToken(apiConfig.Endpoints[i].AuthValue, s.config.EncryptionKey)
				if err != nil {
					s.logger.Warn("Failed to decrypt auth token, using as-is", "error", err)
				} else {
					apiConfig.Endpoints[i].AuthValue = decrypted
				}
			}
		}

		return apiConfig, nil
	}

	return nil, fmt.Errorf("no API consumer node found in connection")
}

// updateConnectionStatus updates the connection status in the database
func (s *APIConsumerService) updateConnectionStatus(connectionID, tenantID, status string) error {
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
