package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/nats-io/nats.go"
)

// WebhookConsumerService manages webhook consumer pipelines
type WebhookConsumerService struct {
	db     *sql.DB
	nc     *nats.Conn
	pub    *messaging.Publisher // JetStream publisher for data-flow messages (#70)
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

	// Signature, when non-nil, makes the webhook reject any request whose
	// HMAC signature header does not match cfg.Secret. Populated at start
	// time from the connection's http.signature config (#67 / Phase 1B).
	Signature *signatureConfig
}

// NewWebhookConsumerService creates a new Webhook Consumer service.
// A JetStream context is derived from nc for data-flow publishes;
// command-channel subscriptions stay on core NATS.
func NewWebhookConsumerService(db *sql.DB, nc *nats.Conn, logger *slog.Logger, config *Config) *WebhookConsumerService {
	js, err := nc.JetStream()
	if err != nil {
		// JS is mandatory for data-flow delivery; surface fast.
		logger.Error("Failed to get JetStream context", "error", err)
		js = nil
	}
	var pub *messaging.Publisher
	if js != nil {
		pub = messaging.NewPublisher(js)
	}
	return &WebhookConsumerService{
		db:                db,
		nc:                nc,
		pub:               pub,
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

	// Extract optional HMAC signature config. resolveSecretsInNodes has
	// already turned <field>_secret_id refs into plaintext, so the secret
	// is sitting in node.config.http.signature.secret.
	sig := s.extractSignature(conn)

	s.mu.Lock()
	s.activeConnections[cmd.ConnectionID] = &ActiveConnection{
		ConnectionID: cmd.ConnectionID,
		TenantID:     cmd.TenantID,
		Cancel:       cancel,
		Signature:    sig,
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

	// Resolve any *_secret_id references in node configs to plaintext.
	resolved, rerr := s.resolveSecretsInNodes(conn.Nodes, tenantID)
	if rerr != nil {
		return nil, fmt.Errorf("resolve secrets: %w", rerr)
	}
	conn.Nodes = resolved
	return &conn, nil
}

// resolveSecretsInNodes parses nodes[], runs the resolver on each config,
// re-marshals. Returns the original bytes on parse failure (workers will
// surface a clearer error downstream when they fail to read config).
func (s *WebhookConsumerService) resolveSecretsInNodes(nodesJSON json.RawMessage, tenantID string) (json.RawMessage, error) {
	if len(nodesJSON) == 0 {
		return nodesJSON, nil
	}
	var nodes []map[string]any
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nodesJSON, err
	}
	reader := crypto.NewSQLSecretReader(s.db)
	for _, n := range nodes {
		cfg, ok := n["config"].(map[string]any)
		if !ok {
			continue
		}
		if err := crypto.ResolveSecrets(context.Background(), reader, tenantID, cfg); err != nil {
			return nodesJSON, err
		}
	}
	return json.Marshal(nodes)
}

// Node represents a pipeline node
type Node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// extractSignature returns the HMAC verification config for a webhook
// connection, or nil if the connection has no signature block configured.
// Reads from nodes[].config.http.signature; the shared secret has already
// been resolved to plaintext by resolveSecretsInNodes (see #66).
func (s *WebhookConsumerService) extractSignature(conn *Connection) *signatureConfig {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return nil
	}
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		var cfg struct {
			Type string `json:"type"`
			HTTP struct {
				Signature *struct {
					Header    string `json:"header"`
					Algorithm string `json:"algorithm"`
					Encoding  string `json:"encoding"`
					Prefix    string `json:"prefix"`
					Secret    string `json:"secret"` // resolved by ResolveSecrets
				} `json:"signature"`
			} `json:"http"`
		}
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			continue
		}
		if cfg.Type != "http" || cfg.HTTP.Signature == nil {
			continue
		}
		sig := cfg.HTTP.Signature
		if sig.Header == "" || sig.Secret == "" {
			s.logger.Warn("Webhook signature block present but incomplete; skipping verification",
				"connection_id", conn.ID,
				"has_header", sig.Header != "",
				"has_secret", sig.Secret != "")
			return nil
		}
		return &signatureConfig{
			Header:    sig.Header,
			Algorithm: sig.Algorithm,
			Encoding:  sig.Encoding,
			Prefix:    sig.Prefix,
			Secret:    sig.Secret,
		}
	}
	return nil
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
