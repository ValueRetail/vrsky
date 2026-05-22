package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Config holds the file producer configuration
type Config struct {
	NATSUrl           string
	DatabaseURL       string
	LogLevel          string
	DefaultOutputDir  string
	SubscriptionTopic string
}

// FileProducerService handles writing files from NATS messages
type FileProducerService struct {
	nc     *nats.Conn
	db     *sql.DB
	logger *slog.Logger
	config *Config

	// Cache for connection configs (multiple producer nodes per connection)
	configCache     map[string][]*ConnectionConfig
	configCacheMu   sync.RWMutex
	configCacheTTL  time.Duration
	configCacheTime map[string]time.Time

	// Signal channels
	stopCh    chan struct{}
	stoppedCh chan struct{}
}

// ConnectionConfig holds the file output configuration for a connection
type ConnectionConfig struct {
	ID             string
	TenantID       string
	OutputPath     string
	FilePattern    string
	PredecessorID  string
	PredIsConsumer bool
}

func main() {
	// Setup logger
	logLevel := os.Getenv("LOG_LEVEL")
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	logger.Info("Starting File Producer Service", "version", "1.0.0")

	// Load configuration
	config := &Config{
		NATSUrl:           getEnv("NATS_URL", "nats://localhost:4222"),
		DatabaseURL:       getEnv("DATABASE_URL", ""),
		LogLevel:          logLevel,
		DefaultOutputDir:  expandHomePath(getEnv("FILE_OUTPUT_DIR", "/tmp/file-output")),
		SubscriptionTopic: getEnv("NATS_SUBSCRIPTION_TOPIC", "vrsky.data.*.pipeline.*"),
	}

	// Validate config
	if config.DatabaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	// Initialize database connection
	db, err := sql.Open("postgres", config.DatabaseURL)
	if err != nil {
		logger.Error("Failed to open database connection", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		logger.Error("Failed to ping database", "error", err)
		os.Exit(1)
	}
	logger.Info("Database connected successfully")

	// Initialize NATS connection
	nc, err := initNATS(config.NATSUrl, logger)
	if err != nil {
		logger.Error("Failed to initialize NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	// Create service
	service := &FileProducerService{
		nc:              nc,
		db:              db,
		logger:          logger,
		config:          config,
		configCache:     make(map[string][]*ConnectionConfig),
		configCacheTTL:  5 * time.Minute,
		configCacheTime: make(map[string]time.Time),
		stopCh:          make(chan struct{}),
		stoppedCh:       make(chan struct{}),
	}

	// Create default output directory
	if err := os.MkdirAll(config.DefaultOutputDir, 0755); err != nil {
		logger.Error("Failed to create default output directory", "error", err, "dir", config.DefaultOutputDir)
		os.Exit(1)
	}
	logger.Info("Output directory ready", "dir", config.DefaultOutputDir)

	// Start the service
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.Start(ctx); err != nil {
		logger.Error("Failed to start service", "error", err)
		os.Exit(1)
	}

	// Start HTTP server for file management
	httpPort := getEnv("FILE_PRODUCER_HTTP_PORT", "9500")
	allowedRoots := []string{config.DefaultOutputDir}
	if hostHome := os.Getenv("HOST_HOME"); hostHome != "" {
		allowedRoots = append(allowedRoots, hostHome)
	}
	startFileHTTPServer(httpPort, allowedRoots, logger)

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logger.Info("File Producer Service running. Press Ctrl+C to stop.")
	<-sigChan

	logger.Info("Shutting down...")
	cancel()
	service.Stop()
	logger.Info("File Producer Service stopped")
}

// Start initializes the service and starts subscribing to NATS
func (s *FileProducerService) Start(ctx context.Context) error {
	// Subscribe to data topics
	sub, err := s.nc.Subscribe(s.config.SubscriptionTopic, func(msg *nats.Msg) {
		s.handleMessage(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to %s: %w", s.config.SubscriptionTopic, err)
	}

	s.logger.Info("Subscribed to NATS topic", "topic", s.config.SubscriptionTopic)

	// Handle stop signal
	go func() {
		<-s.stopCh
		_ = sub.Unsubscribe()
		close(s.stoppedCh)
	}()

	return nil
}

// Stop gracefully stops the service
func (s *FileProducerService) Stop() {
	close(s.stopCh)
	<-s.stoppedCh
}

// handleMessage processes an incoming NATS message
func (s *FileProducerService) handleMessage(ctx context.Context, msg *nats.Msg) {
	s.logger.Debug("Received message", "subject", msg.Subject, "size", len(msg.Data))

	// Parse envelope
	var env envelope.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		s.logger.Error("Failed to unmarshal envelope", "error", err, "subject", msg.Subject)
		return
	}

	// Get connection config (including output path)
	connectionID := env.IntegrationID
	if connectionID == "" {
		// Try to extract from subject: vrsky.data.tenant-{tenantId}.pipeline.{connectionId}
		parts := strings.Split(msg.Subject, ".")
		if len(parts) >= 5 {
			connectionID = parts[4]
		}
	}

	if connectionID == "" {
		s.logger.Error("No connection ID in envelope or subject", "envelope_id", env.ID, "subject", msg.Subject)
		return
	}

	// Get all file producer configs for this connection
	configs, err := s.getConnectionConfigs(ctx, connectionID)
	if err != nil {
		s.logger.Error("Failed to get connection config", "error", err, "connection_id", connectionID)
		return
	}

	// Predecessor-based routing: process for each matching producer node
	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	for _, config := range configs {
		if config.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !config.PredIsConsumer && config.PredecessorID != "" && lastProcessedBy != config.PredecessorID {
			continue
		}

		outputPath := config.OutputPath
		if outputPath == "" {
			outputPath = s.config.DefaultOutputDir
		}

		if err := s.writeFile(ctx, &env, outputPath, config.FilePattern); err != nil {
			s.logger.Error("Failed to write file", "error", err, "envelope_id", env.ID, "path", outputPath)
			continue
		}

		s.logger.Info("File written successfully",
			"envelope_id", env.ID,
			"connection_id", connectionID,
			"path", outputPath,
			"size", len(env.Payload))
	}
}

// getConnectionConfigs retrieves ALL file producer configs for a connection (with caching)
func (s *FileProducerService) getConnectionConfigs(ctx context.Context, connectionID string) ([]*ConnectionConfig, error) {
	s.configCacheMu.RLock()
	if configs, ok := s.configCache[connectionID]; ok {
		if time.Since(s.configCacheTime[connectionID]) < s.configCacheTTL {
			s.configCacheMu.RUnlock()
			return configs, nil
		}
	}
	s.configCacheMu.RUnlock()

	var nodesJSON, edgesJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT nodes, edges FROM connections WHERE id = $1`, connectionID).Scan(&nodesJSON, &edgesJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			configs := []*ConnectionConfig{{ID: connectionID, OutputPath: s.config.DefaultOutputDir, PredIsConsumer: true}}
			s.cacheConfigs(connectionID, configs)
			return configs, nil
		}
		return nil, fmt.Errorf("query connection config: %w", err)
	}

	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		configs := []*ConnectionConfig{{ID: connectionID, OutputPath: s.config.DefaultOutputDir, PredIsConsumer: true}}
		s.cacheConfigs(connectionID, configs)
		return configs, nil
	}

	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	var configs []*ConnectionConfig
	for _, node := range nodes {
		if node.Type != "producer" {
			continue
		}

		var nodeConfig struct {
			Type string `json:"type"`
			File struct {
				Path        string `json:"path"`
				FilePattern string `json:"file_pattern"`
			} `json:"file"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			continue
		}
		// Accept nodes with type "file" or with a file path set
		if nodeConfig.Type != "file" && nodeConfig.File.Path == "" {
			continue
		}

		var predID string
		var predIsConsumer bool
		for _, e := range edges {
			if e.Target == node.ID {
				predID = e.Source
				for _, n := range nodes {
					if n.ID == predID && n.Type == "consumer" {
						predIsConsumer = true
						break
					}
				}
				break
			}
		}

		path := nodeConfig.File.Path
		if path == "" {
			path = s.config.DefaultOutputDir
		}

		configs = append(configs, &ConnectionConfig{
			ID:             connectionID,
			OutputPath:     expandHomePath(path),
			FilePattern:    nodeConfig.File.FilePattern,
			PredecessorID:  predID,
			PredIsConsumer: predIsConsumer,
		})
	}

	if len(configs) == 0 {
		configs = []*ConnectionConfig{{ID: connectionID, OutputPath: s.config.DefaultOutputDir, PredIsConsumer: true}}
	}

	s.cacheConfigs(connectionID, configs)
	return configs, nil
}

func (s *FileProducerService) cacheConfigs(connectionID string, configs []*ConnectionConfig) {
	s.configCacheMu.Lock()
	defer s.configCacheMu.Unlock()
	s.configCache[connectionID] = configs
	s.configCacheTime[connectionID] = time.Now()
}

// writeFile writes the envelope payload to a file
func (s *FileProducerService) writeFile(ctx context.Context, env *envelope.Envelope, outputPath, filePattern string) error {
	// Ensure output directory exists. Track which dirs we actually create so
	// we only chown those — pre-existing parent directories are never touched.
	if err := mkdirAllAndChown(outputPath); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Generate filename
	filename := s.generateFilename(env, filePattern)
	fullPath := filepath.Join(outputPath, filename)

	// Sanitize path to prevent directory traversal
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	absOutputPath, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	if !strings.HasPrefix(absPath, absOutputPath) {
		return fmt.Errorf("path traversal detected")
	}

	// Write file
	if err := os.WriteFile(absPath, env.Payload, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// Chown to host user so files are deletable without sudo
	chownToHostUser(absPath)
	chownToHostUser(absOutputPath)

	s.logger.Debug("Wrote file", "path", absPath, "size", len(env.Payload))
	return nil
}

// generateFilename creates a filename from the envelope and pattern
func (s *FileProducerService) generateFilename(env *envelope.Envelope, pattern string) string {
	if pattern == "" {
		ext := s.deriveExtension(env.ContentType)
		// Prefer original filename from metadata if available
		if env.Metadata != nil {
			if fn, ok := env.Metadata["filename"].(string); ok && fn != "" {
				// If the data was converted to a different format, update the extension
				if _, converted := env.Metadata["_converted"]; converted {
					baseName := fn
					if dotIdx := strings.LastIndex(fn, "."); dotIdx >= 0 {
						baseName = fn[:dotIdx]
					}
					return sanitizeForFilename(baseName + "." + ext)
				}
				return sanitizeForFilename(fn)
			}
		}
		// Default pattern: {id}.{extension}
		return fmt.Sprintf("%s.%s", env.ID, ext)
	}

	// Replace placeholders in pattern
	filename := pattern
	filename = strings.ReplaceAll(filename, "{id}", env.ID)
	filename = strings.ReplaceAll(filename, "{timestamp}", env.CreatedAt.Format("20060102-150405"))
	filename = strings.ReplaceAll(filename, "{extension}", s.deriveExtension(env.ContentType))
	filename = strings.ReplaceAll(filename, "{source}", sanitizeForFilename(env.Source))

	return filename
}

// deriveExtension maps content type to file extension
func (s *FileProducerService) deriveExtension(contentType string) string {
	switch {
	case strings.Contains(contentType, "application/json"):
		return "json"
	case strings.Contains(contentType, "text/plain"):
		return "txt"
	case strings.Contains(contentType, "text/csv"):
		return "csv"
	case strings.Contains(contentType, "application/xml"), strings.Contains(contentType, "text/xml"):
		return "xml"
	case strings.Contains(contentType, "application/yaml"), strings.Contains(contentType, "text/yaml"):
		return "yaml"
	case strings.Contains(contentType, "application/x-ndjson"):
		return "ndjson"
	case strings.Contains(contentType, "text/html"):
		return "html"
	case strings.Contains(contentType, "text/tab-separated-values"):
		return "tsv"
	default:
		return "bin"
	}
}

// Helper functions

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func expandHomePath(path string) string {
	// Prefer FILE_OUTPUT_DIR env var over $HOME (important in containers where $HOME=/root)
	resolveHome := func() string {
		if dir := os.Getenv("FILE_OUTPUT_DIR"); dir != "" {
			return dir
		}
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return ""
	}
	if path == "~" {
		if home := resolveHome(); home != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home := resolveHome(); home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func sanitizeForFilename(s string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	return replacer.Replace(s)
}

// --- File management HTTP server ---

func startFileHTTPServer(port string, allowedRoots []string, logger *slog.Logger) {
	mux := http.NewServeMux()

	// The /files endpoint can list and delete files, so it must not be callable
	// by arbitrary websites loaded in the user's browser. Two controls guard it:
	//   - CORS is restricted to the UI origin (not "*"), so a malicious cross-
	//     origin page can neither read GET responses nor issue the (preflighted)
	//     DELETE request.
	//   - When FILE_PRODUCER_AUTH_TOKEN is set, a matching bearer token is
	//     required, giving defense-in-depth against non-browser clients too.
	allowedOrigin := getEnv("FILE_PRODUCER_ALLOWED_ORIGIN", "http://localhost:5173")
	authToken := os.Getenv("FILE_PRODUCER_AUTH_TOKEN")

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		// Only echo the allow-origin header back when the request originates from
		// the configured UI origin. Vary: Origin keeps caches from leaking it.
		w.Header().Set("Vary", "Origin")
		if r.Header.Get("Origin") == allowedOrigin {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		// Preflight carries no credentials; answer it before the auth check.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if !authorizedFileRequest(r, authToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			handleListFiles(w, r, allowedRoots, logger)
		case http.MethodDelete:
			handleDeleteFiles(w, r, allowedRoots, logger)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// NOTE: binds to all interfaces because in Docker the published port is what
	// reaches this server, and Docker forwards to the container's bridge IP, not
	// its loopback. Network exposure is constrained at the host via the
	// "127.0.0.1:9900:9900" mapping in docker-compose.yml.
	server := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		logger.Info("File management HTTP server started", "port", port, "auth", authToken != "")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("File HTTP server error", "error", err)
		}
	}()
}

// authorizedFileRequest reports whether a /files request is permitted. When no
// token is configured the endpoint stays open (default local-dev behaviour);
// when a token is set, the request must present a matching bearer token.
func authorizedFileRequest(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	const prefix = "Bearer "
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got[len(prefix):]), []byte(token)) == 1
}

// isPathAllowed checks that the resolved path is under one of the allowed roots.
func isPathAllowed(path string, allowedRoots []string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	// Resolve symlinks for safety
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// Path may not exist yet; fall back to the abs path
		resolved = absPath
	}
	for _, root := range allowedRoots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if strings.HasPrefix(resolved, absRoot+"/") || resolved == absRoot {
			return true
		}
	}
	return false
}

type fileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func handleListFiles(w http.ResponseWriter, r *http.Request, allowedRoots []string, logger *slog.Logger) {
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter required"})
		return
	}

	if !isPathAllowed(dirPath, allowedRoots) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "path not allowed"})
		return
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"files": []fileEntry{}, "path": dirPath})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	files := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			Name:    e.Name(),
			Path:    filepath.Join(dirPath, e.Name()),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"files": files, "path": dirPath})
}

func handleDeleteFiles(w http.ResponseWriter, r *http.Request, allowedRoots []string, logger *slog.Logger) {
	targetPath := r.URL.Query().Get("path")
	if targetPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter required"})
		return
	}

	if !isPathAllowed(targetPath, allowedRoots) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "path not allowed"})
		return
	}

	// Don't allow deleting the root directories themselves
	absTarget, _ := filepath.Abs(targetPath)
	for _, root := range allowedRoots {
		absRoot, _ := filepath.Abs(root)
		if absTarget == absRoot {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot delete root output directory"})
			return
		}
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var count int
	if info.IsDir() {
		// Count files before deleting for feedback
		_ = filepath.WalkDir(targetPath, func(_ string, d fs.DirEntry, _ error) error {
			if d != nil && !d.IsDir() {
				count++
			}
			return nil
		})
		err = os.RemoveAll(targetPath)
	} else {
		count = 1
		err = os.Remove(targetPath)
	}

	if err != nil {
		logger.Error("Failed to delete", "path", targetPath, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	logger.Info("Deleted path", "path", targetPath, "files", count)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": targetPath,
		"files":   count,
	})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// chownToHostUser changes ownership of a path to the host user (FILE_OWNER_UID/GID env vars).
// Silently does nothing if env vars are not set or chown fails (best-effort).
func chownToHostUser(path string) {
	uid, gid := getHostOwner()
	if uid < 0 {
		return
	}
	_ = os.Chown(path, uid, gid)
}

// mkdirAllAndChown creates path and all missing parents, chowning *only* the
// directories it actually creates. Pre-existing directories are never touched,
// so this can never escape the intended subtree.
func mkdirAllAndChown(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Walk upward to find which ancestors are missing — those are the ones
	// MkdirAll will create, and the only ones we'll chown afterwards.
	var toChown []string
	p := absPath
	for {
		if _, err := os.Stat(p); err == nil {
			break
		}
		toChown = append(toChown, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	if err := os.MkdirAll(absPath, 0755); err != nil {
		return err
	}
	for _, d := range toChown {
		chownToHostUser(d)
	}
	return nil
}

func getHostOwner() (int, int) {
	// Prefer explicit env vars when set
	uidStr := os.Getenv("FILE_OWNER_UID")
	gidStr := os.Getenv("FILE_OWNER_GID")
	if uidStr != "" && gidStr != "" {
		uid, err1 := strconv.Atoi(uidStr)
		gid, err2 := strconv.Atoi(gidStr)
		if err1 == nil && err2 == nil {
			// Sanity-check: does this UID actually own the host home?
			// If HOST_HOME is mounted and owned by someone else, prefer the
			// mount's real owner — env vars on this machine were stale.
			if hostHome := os.Getenv("HOST_HOME"); hostHome != "" {
				if info, err := os.Stat(hostHome); err == nil {
					if st, ok := info.Sys().(*syscall.Stat_t); ok {
						if int(st.Uid) != uid {
							return int(st.Uid), int(st.Gid)
						}
					}
				}
			}
			return uid, gid
		}
	}
	// Fall back to stat-ing the mounted HOST_HOME
	if hostHome := os.Getenv("HOST_HOME"); hostHome != "" {
		if info, err := os.Stat(hostHome); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				return int(st.Uid), int(st.Gid)
			}
		}
	}
	return -1, -1
}

func initNATS(natsURL string, logger *slog.Logger) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("VRSky-File-Producer"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			logger.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("NATS reconnected", "url", nc.ConnectedUrl())
		}),
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	logger.Info("NATS connected", "url", nc.ConnectedUrl())
	return nc, nil
}
