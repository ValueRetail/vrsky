package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/nats-io/nats.go"
)

// fileConsumer watches a directory and accepts HTTP uploads per active
// connection, publishing file contents into the pipeline. It is an SDK
// Consumer: the runner provides NATS/DB/health/lifecycle; this type implements
// Configure (wire deps + register HTTP handlers), Run (subscribe to command
// subjects, block), and Stop (cancel watchers).
type fileConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc // injected by the runner; the one data-emit path
	logger  *slog.Logger

	baseDir  string // FILE_CONSUMER_BASE_DIR; default watch root when a node has no path
	hostHome string // HOST_HOME; used to expand a leading ~ in node paths

	activeConnections map[string]*ActiveConnection
	mu                sync.RWMutex

	// Event subscribers: connectionId → list of channels
	eventSubs   map[string][]chan FileEvent
	eventSubsMu sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// FileEvent represents an activity event for the UI
type FileEvent struct {
	Type     string `json:"type"` // "added", "deleted", "uploaded", "error", "connected"
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Time     string `json:"time"`
	Message  string `json:"message,omitempty"`
}

type ActiveConnection struct {
	ConnectionID string
	TenantID     string
	WatchDir     string
	Cancel       context.CancelFunc
	knownFiles   map[string]bool // all files seen in last scan
}

// Configure wires dependencies and registers the HTTP endpoints the UI uses
// (served on the SDK auxiliary HTTP port, WORKER_HTTP_PORT/9200). Called once
// before Run.
func (s *fileConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("file-consumer requires DATABASE_URL")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.activeConnections = make(map[string]*ActiveConnection)
	s.eventSubs = make(map[string][]chan FileEvent)
	s.baseDir = getEnv("FILE_CONSUMER_BASE_DIR", "/data/input")
	s.hostHome = os.Getenv("HOST_HOME")

	s.RegisterHTTPHandler("/upload/", s.handleUpload())
	s.RegisterHTTPHandler("/events/", s.handleEvents())
	s.RegisterHTTPHandler("/sample-data/", s.handleSampleData())

	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until the runner
// cancels ctx. Per-connection directory watching is driven from the handlers.
func (s *fileConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish
	s.logger.Info("Starting File Consumer Service")

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

// Stop cancels all directory watchers. The SDK runner calls this after Run
// returns; it also shuts down the aux HTTP server it owns.
func (s *fileConsumer) Stop(ctx context.Context) error {
	s.logger.Info("Stopping File Consumer Service")

	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	s.mu.Lock()
	for connId, ac := range s.activeConnections {
		s.logger.Info("Stopping file watcher", "connection_id", connId)
		ac.Cancel()
	}
	s.activeConnections = make(map[string]*ActiveConnection)
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}

type CommandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

func (s *fileConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received start command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	s.mu.RLock()
	_, exists := s.activeConnections[cmd.ConnectionID]
	s.mu.RUnlock()
	if exists {
		s.logger.Warn("File watcher already active", "connection_id", cmd.ConnectionID)
		return
	}

	conn, err := s.getConnection(cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		s.logger.Error("Failed to fetch connection", "error", err, "connection_id", cmd.ConnectionID)
		return
	}

	if !s.hasFileConsumer(conn) {
		s.logger.Debug("Not a file consumer, ignoring", "connection_id", cmd.ConnectionID)
		return
	}

	// Get watch directory from node config, fall back to {baseDir}/{connectionId}
	watchDir := s.extractWatchDir(conn)
	if watchDir == "" {
		watchDir = filepath.Join(s.baseDir, cmd.ConnectionID)
	}

	// Expand ~ to host home directory
	if len(watchDir) > 0 && watchDir[0] == '~' {
		home := s.hostHome
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		if home != "" {
			watchDir = filepath.Join(home, watchDir[1:])
		}
	}

	if err := os.MkdirAll(watchDir, 0777); err != nil {
		s.logger.Error("Failed to create watch directory", "error", err, "dir", watchDir)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	ac := &ActiveConnection{
		ConnectionID: cmd.ConnectionID,
		TenantID:     cmd.TenantID,
		WatchDir:     watchDir,
		Cancel:       cancel,
		knownFiles:   make(map[string]bool),
	}

	s.mu.Lock()
	s.activeConnections[cmd.ConnectionID] = ac
	s.mu.Unlock()

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	// Start directory watch goroutine (only tracks additions/deletions, no auto-processing)
	go s.watchDirectory(ctx, ac)

	s.logger.Info("File watcher started",
		"connection_id", cmd.ConnectionID,
		"watch_dir", watchDir)
}

func (s *fileConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received stop command", "connection_id", cmd.ConnectionID)

	s.mu.Lock()
	ac, exists := s.activeConnections[cmd.ConnectionID]
	if exists {
		ac.Cancel()
		delete(s.activeConnections, cmd.ConnectionID)
	}
	s.mu.Unlock()

	if !exists {
		s.logger.Warn("File watcher not active", "connection_id", cmd.ConnectionID)
		return
	}

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	s.logger.Info("File watcher stopped", "connection_id", cmd.ConnectionID)
}

// watchDirectory polls for file additions and deletions only (no auto-processing).
// Files are only ingested via explicit upload through the HTTP endpoint.
func (s *fileConsumer) watchDirectory(ctx context.Context, ac *ActiveConnection) {
	logger := s.logger.With("connection_id", ac.ConnectionID, "watch_dir", ac.WatchDir)
	logger.Info("Starting directory watch", "interval", "5s")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Snapshot current files on first scan (no events emitted)
	ac.knownFiles = s.listFiles(ac.WatchDir)

	for {
		select {
		case <-ctx.Done():
			logger.Info("Directory watch stopped")
			return
		case <-ticker.C:
			currentFiles := s.listFiles(ac.WatchDir)

			// Detect new files
			for name := range currentFiles {
				if !ac.knownFiles[name] {
					logger.Info("File added", "file", name)
					s.emitEvent(ac.ConnectionID, FileEvent{
						Type: "added", Filename: name,
						Time: time.Now().UTC().Format(time.RFC3339),
					})
				}
			}

			// Detect deleted files
			for name := range ac.knownFiles {
				if !currentFiles[name] {
					logger.Info("File deleted", "file", name)
					s.emitEvent(ac.ConnectionID, FileEvent{
						Type: "deleted", Filename: name,
						Time: time.Now().UTC().Format(time.RFC3339),
					})
				}
			}

			ac.knownFiles = currentFiles
		}
	}
}

// listFiles returns a set of non-directory filenames in a directory.
func (s *fileConsumer) listFiles(dir string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return make(map[string]bool)
	}
	files := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() {
			files[entry.Name()] = true
		}
	}
	return files
}

func detectContentType(filename string, data []byte) string {
	ext := filepath.Ext(filename)
	switch ext {
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".csv":
		return "text/csv"
	case ".txt":
		return "text/plain"
	case ".html":
		return "text/html"
	}
	// Try to detect from content
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}
	if len(data) > 0 && data[0] == '<' {
		return "application/xml"
	}
	return "application/octet-stream"
}

func (s *fileConsumer) getActiveConnection(connectionID string) *ActiveConnection {
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

type Node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (s *fileConsumer) getConnection(connectionID, tenantID string) (*Connection, error) {
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
	return &conn, nil
}

// extractWatchDir gets the watch directory from the file consumer node config
func (s *fileConsumer) extractWatchDir(conn *Connection) string {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return ""
	}
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		var config struct {
			Type string `json:"type"`
			File struct {
				Path string `json:"path"`
			} `json:"file"`
		}
		if err := json.Unmarshal(node.Config, &config); err != nil {
			continue
		}
		if config.Type == "file" && config.File.Path != "" {
			return config.File.Path
		}
	}
	return ""
}

func (s *fileConsumer) hasFileConsumer(conn *Connection) bool {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return false
	}
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		// Check config.type == "file"
		var config map[string]json.RawMessage
		if err := json.Unmarshal(node.Config, &config); err != nil {
			continue
		}
		if typeRaw, ok := config["type"]; ok {
			var configType string
			if json.Unmarshal(typeRaw, &configType) == nil && configType == "file" {
				return true
			}
		}
	}
	return false
}

func (s *fileConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
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

// --- Event broadcasting (SSE) ---

func (s *fileConsumer) subscribeEvents(connectionID string) (chan FileEvent, func()) {
	ch := make(chan FileEvent, 50)
	s.eventSubsMu.Lock()
	s.eventSubs[connectionID] = append(s.eventSubs[connectionID], ch)
	s.eventSubsMu.Unlock()

	return ch, func() {
		s.eventSubsMu.Lock()
		defer s.eventSubsMu.Unlock()
		subs := s.eventSubs[connectionID]
		for i, sub := range subs {
			if sub == ch {
				s.eventSubs[connectionID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}
}

// emitEvent sends an event to all subscribers for a connection
func (s *fileConsumer) emitEvent(connectionID string, event FileEvent) {
	s.eventSubsMu.RLock()
	defer s.eventSubsMu.RUnlock()
	for _, ch := range s.eventSubs[connectionID] {
		select {
		case ch <- event:
		default: // drop if channel full
		}
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
