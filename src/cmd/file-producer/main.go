package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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

	// Cache for connection configs
	configCache     map[string]*ConnectionConfig
	configCacheMu   sync.RWMutex
	configCacheTTL  time.Duration
	configCacheTime map[string]time.Time

	// Signal channels
	stopCh    chan struct{}
	stoppedCh chan struct{}
}

// ConnectionConfig holds the file output configuration for a connection
type ConnectionConfig struct {
	ID          string
	TenantID    string
	OutputPath  string
	FilePattern string
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
		configCache:     make(map[string]*ConnectionConfig),
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

	// Get output path from connection config
	config, err := s.getConnectionConfig(ctx, connectionID)
	if err != nil {
		s.logger.Error("Failed to get connection config", "error", err, "connection_id", connectionID)
		return
	}

	// Determine output path
	outputPath := config.OutputPath
	if outputPath == "" {
		outputPath = s.config.DefaultOutputDir
	}

	// Write the file
	if err := s.writeFile(ctx, &env, outputPath, config.FilePattern); err != nil {
		s.logger.Error("Failed to write file", "error", err, "envelope_id", env.ID, "path", outputPath)
		return
	}

	s.logger.Info("File written successfully",
		"envelope_id", env.ID,
		"connection_id", connectionID,
		"path", outputPath,
		"size", len(env.Payload))
}

// getConnectionConfig retrieves the connection configuration from the database (with caching)
func (s *FileProducerService) getConnectionConfig(ctx context.Context, connectionID string) (*ConnectionConfig, error) {
	// Check cache first
	s.configCacheMu.RLock()
	if config, ok := s.configCache[connectionID]; ok {
		if time.Since(s.configCacheTime[connectionID]) < s.configCacheTTL {
			s.configCacheMu.RUnlock()
			return config, nil
		}
	}
	s.configCacheMu.RUnlock()

	// Query database for connection config
	// The nodes are stored as a JSON array in the connections table
	query := `SELECT nodes FROM connections WHERE id = $1`

	var nodesJSON []byte
	err := s.db.QueryRowContext(ctx, query, connectionID).Scan(&nodesJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			// Connection not found, use defaults
			s.logger.Debug("Connection not found, using defaults", "connection_id", connectionID)
			config := &ConnectionConfig{
				ID:         connectionID,
				OutputPath: s.config.DefaultOutputDir,
			}
			s.cacheConfig(connectionID, config)
			return config, nil
		}
		return nil, fmt.Errorf("query connection config: %w", err)
	}

	// Parse nodes array to find file producer
	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		s.logger.Warn("Failed to parse nodes, using defaults", "error", err, "connection_id", connectionID)
		config := &ConnectionConfig{
			ID:         connectionID,
			OutputPath: s.config.DefaultOutputDir,
		}
		s.cacheConfig(connectionID, config)
		return config, nil
	}

	// Find the file producer node
	for _, node := range nodes {
		if node.Type != "producer" {
			continue
		}

		// Parse the node config to extract file output settings
		var nodeConfig struct {
			File struct {
				Path        string `json:"path"`
				FilePattern string `json:"file_pattern"`
			} `json:"file"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			continue
		}

		// Check if it has file config
		if nodeConfig.File.Path == "" {
			continue
		}

		config := &ConnectionConfig{
			ID:          connectionID,
			OutputPath:  expandHomePath(nodeConfig.File.Path),
			FilePattern: nodeConfig.File.FilePattern,
		}

		s.cacheConfig(connectionID, config)
		return config, nil
	}

	// No file producer found, use defaults
	s.logger.Debug("No file producer config found, using defaults", "connection_id", connectionID)
	config := &ConnectionConfig{
		ID:         connectionID,
		OutputPath: s.config.DefaultOutputDir,
	}
	s.cacheConfig(connectionID, config)
	return config, nil
}

// cacheConfig stores a connection config in the cache
func (s *FileProducerService) cacheConfig(connectionID string, config *ConnectionConfig) {
	s.configCacheMu.Lock()
	defer s.configCacheMu.Unlock()
	s.configCache[connectionID] = config
	s.configCacheTime[connectionID] = time.Now()
}

// writeFile writes the envelope payload to a file
func (s *FileProducerService) writeFile(ctx context.Context, env *envelope.Envelope, outputPath, filePattern string) error {
	// Ensure output directory exists
	if err := os.MkdirAll(outputPath, 0755); err != nil {
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

	s.logger.Debug("Wrote file", "path", absPath, "size", len(env.Payload))
	return nil
}

// generateFilename creates a filename from the envelope and pattern
func (s *FileProducerService) generateFilename(env *envelope.Envelope, pattern string) string {
	if pattern == "" {
		// Default pattern: {id}.{extension}
		ext := s.deriveExtension(env.ContentType)
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
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
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
