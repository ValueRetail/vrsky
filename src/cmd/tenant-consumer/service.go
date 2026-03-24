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

// TenantConsumerService manages tenant-to-tenant data bridges
type TenantConsumerService struct {
	db     *sql.DB
	nc     *nats.Conn
	logger *slog.Logger

	activeBridges map[string]context.CancelFunc
	mu            sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// NewTenantConsumerService creates a new service
func NewTenantConsumerService(db *sql.DB, nc *nats.Conn, logger *slog.Logger) *TenantConsumerService {
	return &TenantConsumerService{
		db:            db,
		nc:            nc,
		logger:        logger,
		activeBridges: make(map[string]context.CancelFunc),
	}
}

// CommandMessage represents a start/stop command from NATS
type CommandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

// Start subscribes to NATS command topics
func (s *TenantConsumerService) Start(ctx context.Context) error {
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
	return nil
}

// Stop gracefully shuts down all active bridges
func (s *TenantConsumerService) Stop(ctx context.Context) {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	s.mu.Lock()
	for connID, cancel := range s.activeBridges {
		s.logger.Info("Stopping bridge", "connection_id", connID)
		cancel()
	}
	s.activeBridges = make(map[string]context.CancelFunc)
	s.mu.Unlock()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
}

// handleStartCommand processes a start command
func (s *TenantConsumerService) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err)
		return
	}

	logger := s.logger.With("connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)
	logger.Info("Received start command")

	// Check if already running
	s.mu.RLock()
	_, exists := s.activeBridges[cmd.ConnectionID]
	s.mu.RUnlock()
	if exists {
		logger.Warn("Bridge already running")
		return
	}

	// Fetch connection from DB
	conn, err := s.getConnection(cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		logger.Debug("Connection not found or not a tenant consumer", "error", err)
		return
	}

	// Extract tenant consumer config from nodes
	tcConfig, err := s.extractTenantConsumerConfig(conn)
	if err != nil {
		logger.Debug("Not a tenant consumer connection", "error", err)
		return
	}

	logger.Info("Starting tenant consumer bridge",
		"source_tenant_id", tcConfig.SourceTenantID,
		"source_connection_id", tcConfig.SourceConnectionID)

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.activeBridges[cmd.ConnectionID] = cancel
	s.mu.Unlock()

	_ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running")

	go s.runBridge(ctx, cmd.ConnectionID, cmd.TenantID, tcConfig)
}

// handleStopCommand processes a stop command
func (s *TenantConsumerService) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err)
		return
	}

	s.logger.Info("Received stop command", "connection_id", cmd.ConnectionID)

	s.mu.Lock()
	cancel, exists := s.activeBridges[cmd.ConnectionID]
	if exists {
		cancel()
		delete(s.activeBridges, cmd.ConnectionID)
	}
	s.mu.Unlock()

	if exists {
		_ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped")
		s.logger.Info("Bridge stopped", "connection_id", cmd.ConnectionID)
	}
}

// TenantConsumerConfig holds config extracted from a tenant consumer node
type TenantConsumerConfig struct {
	SourceTenantID     string `json:"source_tenant_id"`
	SourceConnectionID string `json:"source_connection_id"`
}

// PipelineConnection represents a connection row from DB
type PipelineConnection struct {
	ID       string
	TenantID string
	Name     string
	Nodes    json.RawMessage
	Edges    json.RawMessage
}

func (s *TenantConsumerService) getConnection(connectionID, tenantID string) (*PipelineConnection, error) {
	var conn PipelineConnection
	err := s.db.QueryRow(`
		SELECT id, tenant_id, name, nodes, edges
		FROM connections
		WHERE id = $1 AND tenant_id = $2
	`, connectionID, tenantID).Scan(&conn.ID, &conn.TenantID, &conn.Name, &conn.Nodes, &conn.Edges)
	if err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
	}
	return &conn, nil
}

// extractTenantConsumerConfig finds a tenant consumer node in the connection's nodes
func (s *TenantConsumerService) extractTenantConsumerConfig(conn *PipelineConnection) (*TenantConsumerConfig, error) {
	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return nil, fmt.Errorf("failed to parse nodes: %w", err)
	}

	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}

		var nodeConfig struct {
			Tenant *TenantConsumerConfig `json:"tenant"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			continue
		}
		if nodeConfig.Tenant == nil || nodeConfig.Tenant.SourceTenantID == "" {
			continue
		}

		return nodeConfig.Tenant, nil
	}

	return nil, fmt.Errorf("no tenant consumer node found")
}

func (s *TenantConsumerService) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	return err
}
