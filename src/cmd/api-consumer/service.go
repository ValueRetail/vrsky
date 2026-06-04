package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/oauthtoken"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/nats-io/nats.go"
)

// apiConsumer polls external HTTP APIs per active connection and publishes the
// responses into the pipeline. It is an SDK Consumer: the runner provides
// NATS/DB/health/lifecycle; this type implements Configure (wire deps + build
// the OAuth token client + register HTTP handlers), Run (subscribe to command
// subjects, block), and Stop (cancel pollers).
type apiConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc // injected by the runner; the one data-emit path
	logger  *slog.Logger

	pollTimeout         time.Duration
	defaultPollInterval time.Duration

	// OAuth token resolver (#75). Non-nil + Configured() when MGMT_API_URL +
	// OAUTH_TOKEN_SERVICE_SECRET are set; resolves access tokens for endpoints
	// with auth_type=oauth. Exported-for-tests via a field so a fake client can
	// be injected.
	oauthTokens *oauthtoken.Client

	// Active pipelines: connectionId → cancel function
	activePipelines map[string]context.CancelFunc
	mu              sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// Configure wires dependencies, builds the OAuth token client (if configured),
// and registers the /sample-data endpoint on the SDK auxiliary HTTP port
// (WORKER_HTTP_PORT, 9800). Called once before Run.
func (s *apiConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("api-consumer requires DATABASE_URL")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.activePipelines = make(map[string]context.CancelFunc)

	s.pollTimeout = envDuration("API_CONSUMER_POLL_TIMEOUT", 30*time.Second)
	s.defaultPollInterval = envDuration("API_CONSUMER_DEFAULT_POLL_INTERVAL", 60*time.Second)

	// OAuth token resolution (#75): wire the client when both env vars are set.
	if s.oauthTokens == nil {
		mgmtURL := os.Getenv("MGMT_API_URL")
		serviceToken := os.Getenv("OAUTH_TOKEN_SERVICE_SECRET")
		if mgmtURL != "" && serviceToken != "" {
			s.oauthTokens = oauthtoken.New(mgmtURL, serviceToken)
			s.logger.Info("OAuth token resolution enabled", "mgmt_api", mgmtURL)
		}
	}

	s.RegisterHTTPHandler("/sample-data/", s.handleSampleData())

	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until the runner
// cancels ctx. Per-connection polling is driven from the command handlers.
func (s *apiConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish
	s.logger.Info("Starting API Consumer Service")

	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to start commands: %w", err)
	}
	s.startSub = startSub

	stopSub, err := s.nc.Subscribe("vrsky.commands.*.connection.stop", s.handleStopCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to stop commands: %w", err)
	}
	s.stopSub = stopSub

	s.logger.Info("Subscribed to NATS command topics")
	<-ctx.Done()
	return nil
}

// Stop cancels all active pollers. The SDK runner calls this after Run returns;
// it also shuts down the aux HTTP server it owns.
func (s *apiConsumer) Stop(ctx context.Context) error {
	s.logger.Info("Stopping API Consumer Service")

	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	s.mu.Lock()
	for connId, cancel := range s.activePipelines {
		s.logger.Info("Stopping pipeline", "connection_id", connId)
		cancel()
	}
	s.activePipelines = make(map[string]context.CancelFunc)
	s.mu.Unlock()

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
func (s *apiConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received start command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	s.mu.RLock()
	_, exists := s.activePipelines[cmd.ConnectionID]
	s.mu.RUnlock()
	if exists {
		s.logger.Warn("Pipeline already running", "connection_id", cmd.ConnectionID)
		return
	}

	conn, err := s.getConnection(cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		s.logger.Error("Failed to fetch connection", "error", err, "connection_id", cmd.ConnectionID)
		return
	}

	// Fleet worker: every consumer receives every start command. A connection
	// without an API consumer node simply isn't ours — ignore it quietly
	// (matches db-consumer/file-consumer), don't log an error.
	apiConfig, ok := s.extractAPIConsumerConfig(conn)
	if !ok {
		s.logger.Debug("Not an API consumer, ignoring", "connection_id", cmd.ConnectionID)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.activePipelines[cmd.ConnectionID] = cancel
	s.mu.Unlock()

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	go s.pollConnection(ctx, cmd.ConnectionID, cmd.TenantID, apiConfig)
}

// handleStopCommand processes a stop command from NATS
func (s *apiConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received stop command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

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

// getConnection fetches a connection and resolves any *_secret_id references in
// its node configs to plaintext (e.g. an endpoint's auth_value_secret_id), so
// the typed config below sees a populated AuthValue.
func (s *apiConsumer) getConnection(connectionID, tenantID string) (*Connection, error) {
	var conn Connection
	err := s.db.QueryRow(
		`SELECT id, tenant_id, name, nodes, edges FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&conn.ID, &conn.TenantID, &conn.Name, &conn.Nodes, &conn.Edges)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("connection not found: %s", connectionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query connection: %w", err)
	}

	resolved, err := s.resolveSecretsInNodes(conn.Nodes, tenantID)
	if err != nil {
		return nil, fmt.Errorf("resolve secrets: %w", err)
	}
	conn.Nodes = resolved
	return &conn, nil
}

// resolveSecretsInNodes walks the nodes[] array, decodes each node.config, runs
// the secrets resolver (which recurses into the endpoints array and replaces
// auth_value_secret_id → auth_value), and re-marshals. Errors are fatal — we'd
// rather fail loud than poll with a missing credential.
func (s *apiConsumer) resolveSecretsInNodes(nodesJSON json.RawMessage, tenantID string) (json.RawMessage, error) {
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

// APIConsumerConfig represents the API Consumer node configuration
type APIConsumerConfig struct {
	BaseURL             string        `json:"base_url"`
	Endpoints           []APIEndpoint `json:"endpoints"`
	PollIntervalSeconds int           `json:"poll_interval_seconds"`
	OneTimeOnly         bool          `json:"one_time_only"` // If true, retrieve data once and stop (no polling)
}

// APIEndpoint represents a single API endpoint configuration
type APIEndpoint struct {
	Path         string `json:"path"`
	Params       string `json:"params"`         // Query parameters (e.g., "lat=59.9&lon=10.7")
	AuthType     string `json:"auth_type"`      // "none", "bearer", "api_key", "oauth"
	AuthValue    string `json:"auth_value"`     // Token or API key; resolved from auth_value_secret_id at deploy
	OAuthGrantID string `json:"oauth_grant_id"` // grant whose access token to inject (auth_type=oauth)
}

// Node represents a pipeline node
type Node struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

// NodeConfig wraps the type-specific configuration: {"type":"api","api":{...}}
type NodeConfig struct {
	Type string             `json:"type"`
	API  *APIConsumerConfig `json:"api"`
}

// extractAPIConsumerConfig extracts API Consumer config from connection nodes.
// Secrets (auth_value) are already resolved to plaintext by getConnection.
// Returns ok=false when the connection has no API consumer node — that is not
// an error for a fleet worker, just a connection that belongs to a different
// consumer.
func (s *apiConsumer) extractAPIConsumerConfig(conn *Connection) (*APIConsumerConfig, bool) {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		s.logger.Warn("Failed to parse nodes", "connection_id", conn.ID, "error", err)
		return nil, false
	}

	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}

		var nodeConfig NodeConfig
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			s.logger.Warn("Failed to parse node config", "node_id", node.ID, "error", err)
			continue
		}

		// Only pick up nodes explicitly typed as "api" — stale `api` blobs from
		// other consumer types (e.g. http/webhook) must be ignored.
		if nodeConfig.Type != "api" || nodeConfig.API == nil {
			continue
		}

		apiConfig := nodeConfig.API

		// Default to a single "/" endpoint if none configured
		if len(apiConfig.Endpoints) == 0 {
			apiConfig.Endpoints = []APIEndpoint{{Path: "/", AuthType: "none"}}
		}

		// Set default poll interval if not specified
		if apiConfig.PollIntervalSeconds <= 0 {
			apiConfig.PollIntervalSeconds = int(s.defaultPollInterval.Seconds())
		}

		return apiConfig, true
	}

	return nil, false
}

// updateConnectionStatus updates the connection status in the database
func (s *apiConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	if err != nil {
		return fmt.Errorf("failed to update connection status: %w", err)
	}
	s.logger.Info("Updated connection status", "connection_id", connectionID, "status", status)
	return nil
}

// envDuration reads a time.Duration from env, falling back to def.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
