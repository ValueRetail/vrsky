package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/google/uuid"
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

	// publishStream is non-nil only when the SDK selected the streaming path
	// (ADR 0001) — i.e. this worker has a payload store. When it is available a
	// watched file is streamed from disk rather than read into memory, which is
	// what lifts the maxWatchFileBytes ceiling. inlineMax is the size above which
	// the SDK offloads anyway, so it marks where buffering stops paying off.
	publishStream sdk.PublishStreamFunc
	inlineMax     int

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

// maxWatchFileBytes caps the size of a watched file the poller will read into
// memory before publishing (mirrors the 32 MiB cap on the HTTP upload path).
// It applies only to the buffered path: when the worker can stream (ADR 0001)
// a file over the inline threshold is sent straight from disk and this ceiling
// does not apply at all.
const maxWatchFileBytes = 32 << 20

// ingestWatchedFile publishes one newly-detected file, streaming it from disk
// when the worker supports that and the file is over the inline threshold —
// which is what allows files larger than maxWatchFileBytes to be ingested at
// all. Returns the envelope, the file size, and whether it was streamed.
func (s *fileConsumer) ingestWatchedFile(ctx context.Context, ac *ActiveConnection, name, path string) (*envelope.Envelope, int64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("stat: %w", err)
	}
	size := info.Size()

	// Small enough to buffer, or no streaming available: existing behaviour,
	// including the size ceiling that protects worker memory.
	if s.publishStream == nil || s.inlineMax <= 0 || size <= int64(s.inlineMax) {
		if size > maxWatchFileBytes {
			return nil, size, false, fmt.Errorf("file exceeds the %d-byte limit and this worker cannot stream (no payload store configured)", int64(maxWatchFileBytes))
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, size, false, rerr
		}
		env, perr := s.ingestFile(ctx, ac, name, data, "watch:"+name)
		return env, size, false, perr
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, size, false, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	// Sniff the content type from a small head, then hand the whole file over as
	// head + remainder so nothing is read twice and nothing is buffered.
	head := make([]byte, contentSniffBytes)
	n, rerr := io.ReadFull(f, head)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		return nil, size, false, fmt.Errorf("read: %w", rerr)
	}
	head = head[:n]

	env := &envelope.Envelope{
		ID:            uuid.New().String(),
		TenantID:      ac.TenantID,
		IntegrationID: ac.ConnectionID,
		ContentType:   detectContentType(name, head),
		Source:        "watch:" + name,
		StepHistory:   []string{"file-consumer"},
		CreatedAt:     time.Now().UTC(),
		Metadata:      map[string]interface{}{"filename": name},
	}
	if err := s.publishStream(ctx, env, io.MultiReader(bytes.NewReader(head), f)); err != nil {
		return nil, size, true, err
	}
	return env, size, true, nil
}

// contentSniffBytes is how much of a streamed file is read up front to detect
// its content type — enough for detectContentType, small enough to be free.
const contentSniffBytes = 512

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
	s.inlineMax = res.InlineMaxBytes()
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
// RunStream is Run with large-file streaming enabled (ADR 0001). The SDK calls
// it instead of Run when a payload store is configured; watched files above the
// inline threshold are then streamed from disk into the pipeline, so size is
// bounded by the copy buffer rather than by worker memory.
func (s *fileConsumer) RunStream(ctx context.Context, publish sdk.PublishFunc, publishStream sdk.PublishStreamFunc) error {
	s.publishStream = publishStream
	return s.Run(ctx, publish)
}

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

	if err := os.MkdirAll(watchDir, 0o755); err != nil {
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

			// Detect new files and ingest them into the pipeline. Previously
			// this only emitted a UI "added" event and never published the
			// file's contents — so the directory-watch source silently did
			// nothing downstream (#143).
			for name := range currentFiles {
				if ac.knownFiles[name] {
					continue
				}
				logger.Info("File added", "file", name)
				path := filepath.Join(ac.WatchDir, name)
				env, size, streamed, ingErr := s.ingestWatchedFile(ctx, ac, name, path)
				if ingErr != nil {
					logger.Error("Failed to ingest detected file", "file", name, "error", ingErr)
					s.emitEvent(ac.ConnectionID, FileEvent{
						Type: "error", Filename: name, Size: size, Message: ingErr.Error(),
						Time: time.Now().UTC().Format(time.RFC3339),
					})
					continue
				}
				logger.Info("File ingested from watch dir", "file", name, "size", size, "streamed", streamed, "envelope_id", env.ID)
				s.emitEvent(ac.ConnectionID, FileEvent{
					Type: "added", Filename: name, Size: size,
					Time: time.Now().UTC().Format(time.RFC3339),
				})
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
