package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	dialMaxAttempts = 3
	dialBaseBackoff = time.Second
)

// sftpConsumer watches a remote SFTP directory per active connection, fetches
// new files and publishes them into the pipeline. SDK Consumer: Configure wires
// deps, Run subscribes to command subjects and blocks, Stop cancels pollers.
type sftpConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	// dial opens an SFTP connection. Defaulted to realDial in Configure; tests
	// inject a fake so the poller runs without a live server.
	dial dialFunc

	active map[string]context.CancelFunc
	mu     sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// SFTPConfig is the per-node configuration (config.sftp). Password and
// PrivateKey are resolved from *_secret_id references before use.
type SFTPConfig struct {
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	Password            string `json:"password"`     // resolved from password_secret_id
	PrivateKey          string `json:"private_key"`  // resolved from private_key_secret_id
	HostKey             string `json:"host_key"`     // optional pinned host key (authorized_keys line)
	RemoteDir           string `json:"remote_dir"`   // directory to watch
	FilePattern         string `json:"file_pattern"` // optional glob, e.g. *.csv
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	AfterAction         string `json:"after_action"` // "delete" | "move" | "none" (default none)
	MoveDir             string `json:"move_dir"`     // destination when after_action=move
}

type nodeConfig struct {
	Type string      `json:"type"`
	SFTP *SFTPConfig `json:"sftp"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

type commandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

// Configure wires dependencies. Called once before Run.
func (s *sftpConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("sftp-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.active = make(map[string]context.CancelFunc)
	if s.dial == nil {
		s.dial = realDial
	}
	s.RegisterHTTPHandler("/test-connection/", s.handleTestConnection())
	s.RegisterHTTPHandler("/sample-data/", s.handleSampleData())
	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled. Per-connection polling is driven from the command handlers.
func (s *sftpConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish

	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("subscribe start commands: %w", err)
	}
	s.startSub = startSub

	stopSub, err := s.nc.Subscribe("vrsky.commands.*.connection.stop", s.handleStopCommand)
	if err != nil {
		return fmt.Errorf("subscribe stop commands: %w", err)
	}
	s.stopSub = stopSub

	s.logger.Info("Subscribed to NATS command topics")
	<-ctx.Done()
	return nil
}

// Stop cancels all pollers. The SDK runner calls this after Run returns.
func (s *sftpConsumer) Stop(ctx context.Context) error {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}
	s.mu.Lock()
	for id, cancel := range s.active {
		s.logger.Info("Stopping SFTP poller", "connection_id", id)
		cancel()
	}
	s.active = make(map[string]context.CancelFunc)
	s.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (s *sftpConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("parse start command", "error", err)
		return
	}
	logger := s.logger.With("connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	s.mu.RLock()
	_, exists := s.active[cmd.ConnectionID]
	s.mu.RUnlock()
	if exists {
		logger.Warn("SFTP poller already running")
		return
	}

	cfg, ok := s.getSFTPConfig(cmd.ConnectionID, cmd.TenantID)
	if !ok {
		logger.Debug("Not an SFTP consumer for this connection, ignoring")
		return
	}
	if cfg.Host == "" || cfg.Username == "" || cfg.RemoteDir == "" {
		logger.Error("SFTP config incomplete (need host, username, remote_dir)")
		return
	}
	if cfg.Password == "" && cfg.PrivateKey == "" {
		logger.Error("SFTP config has no credentials (set a password or a private key)")
		return
	}
	if cfg.HostKey == "" {
		logger.Warn("SFTP host_key not pinned; server identity will not be verified (set host_key in production)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.active[cmd.ConnectionID] = cancel
	s.mu.Unlock()
	_ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running")

	logger.Info("Starting SFTP poller", "host", cfg.Host, "remote_dir", cfg.RemoteDir, "after_action", cfg.AfterAction)
	go s.runPoller(ctx, cmd.ConnectionID, cmd.TenantID, cfg)
}

func (s *sftpConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("parse stop command", "error", err)
		return
	}
	s.mu.Lock()
	cancel, exists := s.active[cmd.ConnectionID]
	if exists {
		cancel()
		delete(s.active, cmd.ConnectionID)
	}
	s.mu.Unlock()
	if exists {
		_ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped")
		s.logger.Info("SFTP poller stopped", "connection_id", cmd.ConnectionID)
	}
}

// runPoller fetches once immediately then on the configured interval. Each
// cycle opens a fresh connection (natural reconnection) with bounded backoff.
func (s *sftpConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *SFTPConfig) {
	logger := s.logger.With("connection_id", connID)
	// processed tracks filenames already published this session, so an
	// after_action=none directory does not re-publish the same file every tick.
	processed := make(map[string]bool)

	s.pollOnce(ctx, connID, tenantID, cfg, processed, logger)

	// poll_interval_seconds <= 0 means run once (matches the UI label "0 = once").
	if cfg.PollIntervalSeconds <= 0 {
		logger.Info("SFTP one-shot poll complete")
		_ = s.updateConnectionStatus(connID, tenantID, "stopped")
		s.mu.Lock()
		delete(s.active, connID)
		s.mu.Unlock()
		return
	}

	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollOnce(ctx, connID, tenantID, cfg, processed, logger)
		}
	}
}

// pollOnce dials, lists the remote dir, and fetches+publishes+after-actions each
// new file. Connection errors are logged and retried on the next tick.
func (s *sftpConsumer) pollOnce(ctx context.Context, connID, tenantID string, cfg *SFTPConfig, processed map[string]bool, logger *slog.Logger) {
	conn, err := s.dialWithBackoff(ctx, cfg, logger)
	if err != nil {
		logger.Error("SFTP connect failed; will retry next cycle", "error", err)
		return
	}
	defer conn.Close()

	files, err := conn.List(cfg.RemoteDir)
	if err != nil {
		logger.Error("SFTP list failed", "dir", cfg.RemoteDir, "error", err)
		return
	}

	for _, f := range files {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if processed[f.Name] {
			continue
		}
		if cfg.FilePattern != "" {
			if match, _ := path.Match(cfg.FilePattern, f.Name); !match {
				continue
			}
		}

		remotePath := joinRemote(cfg.RemoteDir, f.Name)
		data, err := conn.Read(remotePath)
		if err != nil {
			logger.Error("SFTP read failed", "file", f.Name, "error", err)
			continue
		}

		if err := s.publishFile(ctx, connID, tenantID, f.Name, data); err != nil {
			logger.Error("publish failed", "file", f.Name, "error", err)
			continue // leave the file in place; retried next cycle
		}
		processed[f.Name] = true
		logger.Info("SFTP file ingested", "file", f.Name, "size", len(data))

		if err := s.afterAction(conn, cfg, f.Name, remotePath); err != nil {
			logger.Warn("after-action failed", "file", f.Name, "action", cfg.AfterAction, "error", err)
		}
	}
}

// dialWithBackoff tries to open a connection up to dialMaxAttempts times with
// exponential backoff, honouring ctx cancellation.
func (s *sftpConsumer) dialWithBackoff(ctx context.Context, cfg *SFTPConfig, logger *slog.Logger) (sftpConn, error) {
	var lastErr error
	backoff := dialBaseBackoff
	for attempt := 1; attempt <= dialMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, err := s.dial(cfg)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		logger.Warn("SFTP dial attempt failed", "attempt", attempt, "error", err)
		if attempt < dialMaxAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}
	return nil, lastErr
}

// afterAction applies the configured post-ingest action to a fetched file.
func (s *sftpConsumer) afterAction(conn sftpConn, cfg *SFTPConfig, name, remotePath string) error {
	switch cfg.AfterAction {
	case "delete":
		return conn.Remove(remotePath)
	case "move":
		dst := cfg.MoveDir
		if dst == "" {
			return fmt.Errorf("after_action=move but move_dir is empty")
		}
		if err := conn.MkdirAll(dst); err != nil {
			return fmt.Errorf("mkdir move_dir: %w", err)
		}
		return conn.Rename(remotePath, joinRemote(dst, name))
	default: // "none" / "" / "leave"
		return nil
	}
}

func (s *sftpConsumer) publishFile(ctx context.Context, connID, tenantID, filename string, data []byte) error {
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = detectContentType(filename, data)
	env.Source = "sftp:" + filename
	env.Payload = data
	env.PayloadSize = int64(len(data))
	env.StepHistory = []string{"sftp-consumer"}
	env.Metadata = map[string]interface{}{"filename": filename}
	return s.publish(ctx, env)
}

// --- DB helpers ---

// getSFTPConfig loads the connection, resolves *_secret_id references to
// plaintext, and extracts the SFTP consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (s *sftpConsumer) getSFTPConfig(connectionID, tenantID string) (*SFTPConfig, bool) {
	var nodesJSON json.RawMessage
	err := s.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON)
	if err != nil {
		s.logger.Debug("connection not found", "error", err, "connection_id", connectionID)
		return nil, false
	}

	resolved, err := crypto.ResolveSecretsInJSON(context.Background(), crypto.NewSQLSecretReader(s.db), tenantID, nodesJSON)
	if err != nil {
		s.logger.Error("resolve secrets", "error", err, "connection_id", connectionID)
		return nil, false
	}

	var nodes []node
	if err := json.Unmarshal(resolved, &nodes); err != nil {
		s.logger.Warn("parse nodes", "error", err, "connection_id", connectionID)
		return nil, false
	}
	for _, n := range nodes {
		if n.Type != "consumer" {
			continue
		}
		var nc nodeConfig
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type == "sftp" && nc.SFTP != nil {
			return nc.SFTP, true
		}
	}
	return nil, false
}

func (s *sftpConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	return err
}

// detectContentType picks a MIME type from the filename extension, falling back
// to a light content sniff.
func detectContentType(filename string, data []byte) string {
	switch {
	case hasSuffixFold(filename, ".json"):
		return "application/json"
	case hasSuffixFold(filename, ".xml"):
		return "application/xml"
	case hasSuffixFold(filename, ".csv"):
		return "text/csv"
	case hasSuffixFold(filename, ".txt"):
		return "text/plain"
	}
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}
	return "application/octet-stream"
}

func hasSuffixFold(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return equalFold(s[len(s)-len(suffix):], suffix)
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
