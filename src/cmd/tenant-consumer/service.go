package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/nats-io/nats.go"
)

// tenantConsumer manages tenant-to-tenant data bridges. It is an SDK Consumer:
// the runner provides NATS/DB/health/lifecycle; this type implements Configure
// (wire deps), Run (subscribe to command subjects and block), and Stop (tear
// down bridges).
type tenantConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc // injected by the runner; the one data-emit path
	logger  *slog.Logger

	activeBridges map[string]*activeConn
	mu            sync.RWMutex
	// starts holds the claim of each start in progress (#397); guarded by mu.
	starts sdk.StartGuard

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// CommandMessage represents a start/stop command from NATS
type CommandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

// Configure wires dependencies. Called once by the runner before Run.
func (s *tenantConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("tenant-consumer requires DATABASE_URL")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.activeBridges = make(map[string]*activeConn)
	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until the runner
// cancels ctx (on SIGTERM/SIGINT). Per-bridge JetStream subscriptions and the
// republish path are driven from the command handlers.
func (s *tenantConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish

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
	// Bring back what the database says is running: start/stop are NATS commands
	// and are not persisted, so without this a restart silently stopped every
	// pipeline on this service until someone redeployed it.
	sdk.RestoreRunning(ctx, s.db, s.logger, "tenant", s.startConnection)

	<-ctx.Done()
	return nil
}

// Stop gracefully shuts down all active bridges. The SDK runner calls this
// after Run returns.
func (s *tenantConsumer) Stop(ctx context.Context) error {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	s.mu.Lock()
	for connID, ac := range s.activeBridges {
		s.logger.Info("Stopping bridge", "connection_id", connID)
		ac.cancel()
	}
	s.activeBridges = make(map[string]*activeConn)
	// A start still in flight must not leave a poller nobody will cancel.
	s.starts.StopAllLocked()
	s.mu.Unlock()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	return nil
}

// handleStartCommand processes a start command
func (s *tenantConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err)
		return
	}
	s.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

// startConnection is the start command's work, also what RestoreRunning calls on
// boot for every pipeline the database says is running (plans/stable-connections.md).
func (s *tenantConsumer) startConnection(ctx context.Context, connectionID, tenantID string) {
	_ = ctx
	logger := s.logger.With("connection_id", connectionID, "tenant_id", tenantID)
	logger.Info("Received start command")

	// Restore and the start-command handler call this on different goroutines.
	// The check and the claim are one critical section, so a second start for
	// the same connection stops here instead of starting a second poller (#397).
	// A start refused while another is in flight is not lost: it runs again if
	// that one gives up without activating (#424). The refused call returns
	// right away, so the retry gets its caller's ctx, before any reassignment.
	claim, ok := s.starts.ReserveOrRetry(&s.mu, tenantID, connectionID, func() bool { _, ok := s.activeBridges[connectionID]; return ok },
		func() { s.startConnection(ctx, connectionID, tenantID) })
	if !ok {
		logger.Warn("Bridge already running or starting")
		return
	}
	// Frees the claim on every early return below; a no-op once active.
	defer claim.Release()

	// Fetch connection from DB
	conn, err := s.getConnection(connectionID, tenantID)
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
	ac := &activeConn{cancel: cancel}
	// A stop that arrived during the work above has taken the claim back and wins.
	if !claim.Activate(func() { s.activeBridges[connectionID] = ac }) {
		cancel()
		logger.Info("Start abandoned: a stop arrived while it was in progress")
		return
	}

	// Through the guard's status order: a stop that cancelled the connection
	// since Activate and wrote "stopped" first makes this a no-op (#423).
	claim.RunningStatus().Write(func() { _ = s.updateConnectionStatus(connectionID, tenantID, "running") })

	go s.runBridge(ctx, ac, connectionID, tenantID, tcConfig)
}

// activeConn is one start's entry in activeBridges. The bridge goroutine gets
// the same pointer, so when it ends on its own it can tell whether the entry is
// still its own (#408).
type activeConn struct {
	cancel context.CancelFunc
}

// dropActive removes connectionID from activeBridges if the entry is still own.
// After a stop and a new start it belongs to the newer bridge, and an old one
// failing must leave it alone, or the next start runs a second bridge beside
// the new one (#408).
//
// It reports whether the entry was own. Only then may the bridge write its
// final status; otherwise the newer start has written "running" and owns the
// status too (#456). The write goes through the returned StatusWrite, taken
// in the same critical section, so it cannot land over the "running" of a
// start that activates after the drop (#423).
func (s *tenantConsumer) dropActive(connectionID, tenantID string, own *activeConn) (sdk.StatusWrite, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeBridges[connectionID] != own {
		return sdk.StatusWrite{}, false
	}
	delete(s.activeBridges, connectionID)
	return s.starts.StopStatusLocked(tenantID, connectionID), true
}

// handleStopCommand processes a stop command
func (s *tenantConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err)
		return
	}

	s.logger.Info("Received stop command", "connection_id", cmd.ConnectionID)

	s.mu.Lock()
	ac, exists := s.activeBridges[cmd.ConnectionID]
	if exists {
		ac.cancel()
		delete(s.activeBridges, cmd.ConnectionID)
	}
	// A start still in progress gives up instead of finishing after the stop (#397).
	starting := s.starts.StopLocked(cmd.ConnectionID)
	// Taken with the stop, and only when there is something to write: of this
	// "stopped" write and a start's "running" write, only the one for the later
	// of the stop and the start's Activate is left standing, whichever reaches
	// the database first (#423).
	var stopped sdk.StatusWrite
	if exists {
		stopped = s.starts.StopStatusLocked(cmd.TenantID, cmd.ConnectionID)
	}
	s.mu.Unlock()
	if starting {
		s.logger.Info("Stop arrived while the connection was starting; the start is abandoned", "connection_id", cmd.ConnectionID)
	}

	if exists {
		stopped.Write(func() { _ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped") })
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

func (s *tenantConsumer) getConnection(connectionID, tenantID string) (*PipelineConnection, error) {
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
func (s *tenantConsumer) extractTenantConsumerConfig(conn *PipelineConnection) (*TenantConsumerConfig, error) {
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

func (s *tenantConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	return err
}
