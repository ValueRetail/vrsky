package io

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// FileConsumer monitors a directory for files and publishes them to NATS
type FileConsumer struct {
	// Configuration
	dir          string
	pattern      string
	pollInterval time.Duration

	// Runtime
	ctx        context.Context
	cancel     context.CancelFunc
	ticker     *time.Ticker
	messages   chan *envelope.Envelope
	nc         *nats.Conn
	logger     *slog.Logger
	mu         sync.Mutex
	closed     bool
	closedOnce sync.Once
}

// NewFileConsumer creates a new file consumer from environment configuration
func NewFileConsumer(logger *slog.Logger) (*FileConsumer, error) {
	// Read configuration from environment variables
	dir := os.Getenv("FILE_INPUT_DIR")
	if dir == "" {
		dir = "/tmp/file-input"
	}

	pattern := os.Getenv("FILE_INPUT_PATTERN")
	if pattern == "" {
		pattern = "*"
	}

	pollIntervalStr := os.Getenv("FILE_INPUT_POLL_INTERVAL")
	pollInterval := 5 * time.Second
	if pollIntervalStr != "" {
		if parsed, err := time.ParseDuration(pollIntervalStr + "s"); err == nil {
			pollInterval = parsed
		}
	}

	// Validate configuration
	if err := validateFileInputConfig(dir, pattern, pollInterval); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = slog.Default()
	}

	return &FileConsumer{
		dir:          dir,
		pattern:      pattern,
		pollInterval: pollInterval,
		logger:       logger,
		messages:     make(chan *envelope.Envelope, 100),
	}, nil
}

// Start begins monitoring the directory for files
func (f *FileConsumer) Start(ctx context.Context) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return fmt.Errorf("file consumer already stopped")
	}
	f.mu.Unlock()

	// Create directory if it doesn't exist
	if err := os.MkdirAll(f.dir, 0755); err != nil {
		return fmt.Errorf("create input directory: %w", err)
	}

	// Connect to NATS
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	f.nc = nc

	// Create cancellable context
	f.ctx, f.cancel = context.WithCancel(ctx)

	// Start polling goroutine
	go f.pollLoop()

	f.logger.Info("File Consumer started", "dir", f.dir, "pattern", f.pattern, "interval", f.pollInterval)
	return nil
}

// Stop gracefully stops the file consumer
func (f *FileConsumer) Close() error {
	f.closedOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()

		if f.cancel != nil {
			f.cancel()
		}

		if f.ticker != nil {
			f.ticker.Stop()
		}

		if f.nc != nil {
			f.nc.Close()
		}

		close(f.messages)
	})

	f.logger.Info("File Consumer closed")
	return nil
}

// Read returns the next file envelope
func (f *FileConsumer) Read(ctx context.Context) (*envelope.Envelope, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case env, ok := <-f.messages:
		if !ok {
			return nil, fmt.Errorf("messages channel closed")
		}
		return env, nil
	}
}

// pollLoop runs in a goroutine and polls the directory for files
func (f *FileConsumer) pollLoop() {
	f.ticker = time.NewTicker(f.pollInterval)
	defer f.ticker.Stop()

	for {
		select {
		case <-f.ctx.Done():
			return
		case <-f.ticker.C:
			f.processFiles()
		}
	}
}

// processFiles finds and processes files in the monitored directory
func (f *FileConsumer) processFiles() {
	// Build glob pattern
	globPattern := filepath.Join(f.dir, f.pattern)

	// List files matching pattern
	files, err := filepath.Glob(globPattern)
	if err != nil {
		f.logger.Error("Failed to glob files", "pattern", globPattern, "err", err)
		return
	}

	for _, filePath := range files {
		// Skip directories
		info, err := os.Stat(filePath)
		if err != nil {
			f.logger.Warn("Failed to stat file", "path", filePath, "err", err)
			continue
		}
		if info.IsDir() {
			continue
		}

		// Process file
		if err := f.processFile(filePath); err != nil {
			f.logger.Error("Failed to process file", "path", filePath, "err", err)
		}
	}
}

// processFile reads a file and publishes it as an envelope
func (f *FileConsumer) processFile(filePath string) error {
	// Read file contents
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	// Create envelope using the proper structure
	env := envelope.New()
	env.ID = uuid.New().String()
	env.Source = "FileConsumer"
	env.Payload = content
	env.PayloadSize = int64(len(content))
	env.ContentType = detectContentType(filePath)

	// Publish to NATS
	data, err := envelope.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	if err := f.nc.Publish("integration.files.received", data); err != nil {
		return fmt.Errorf("publish to NATS: %w", err)
	}

	// Send to messages channel
	select {
	case f.messages <- env:
	case <-f.ctx.Done():
		return f.ctx.Err()
	}

	f.logger.Info("Processed file", "filename", filepath.Base(filePath), "size", len(content), "id", env.ID)
	return nil
}

// detectContentType determines the MIME type from file extension
func detectContentType(filePath string) string {
	ext := filepath.Ext(filePath)
	if ext == "" {
		return "application/octet-stream"
	}

	// Custom mappings for common types (takes precedence over mime package)
	switch ext {
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain"
	case ".csv":
		return "text/csv"
	case ".xml":
		return "application/xml"
	case ".yaml", ".yml":
		return "application/yaml"
	}

	// Try to detect using Go's mime package for other types
	contentType := mime.TypeByExtension(ext)
	if contentType != "" {
		return contentType
	}

	return "application/octet-stream"
}

// validateFileInputConfig validates the file input configuration
func validateFileInputConfig(dir, pattern string, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("poll interval must be positive, got %v", interval)
	}

	// Try to validate pattern (simple check - doesn't need to be exhaustive)
	if pattern == "" {
		return fmt.Errorf("pattern cannot be empty")
	}

	// Note: We don't check if dir is writable here - that's done in Start()
	// This allows for creation of the directory if it doesn't exist yet

	return nil
}
