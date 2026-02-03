package io

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// ProcessedFile tracks the hash and modification time of a processed file
type ProcessedFile struct {
	Hash  string
	Mtime int64
}

// FileRetry tracks retry attempts for failed files
type FileRetry struct {
	Attempts    int
	LastError   string
	LastAttempt time.Time
}

// FileConsumer monitors a directory for files and publishes them to NATS
type FileConsumer struct {
	// Configuration
	dir                    string
	pattern                string
	pollInterval           time.Duration
	archiveDir             string
	errorDir               string
	deleteAfterProcessing  bool
	maxRetries             int
	retryBackoffMs         int
	archiveRetentionDays   int

	// Runtime
	ctx            context.Context
	cancel         context.CancelFunc
	ticker         *time.Ticker
	messages       chan *envelope.Envelope
	subject        string
	nc             *nats.Conn
	logger         *slog.Logger
	mu             sync.Mutex
	closed         bool
	closedOnce     sync.Once
	processedFiles map[string]ProcessedFile
	failedFiles    map[string]FileRetry
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
		effectiveLogger := logger
		if effectiveLogger == nil {
			effectiveLogger = slog.Default()
		}
		parsed, err := time.ParseDuration(pollIntervalStr)
		if err != nil {
			effectiveLogger.Warn("invalid FILE_INPUT_POLL_INTERVAL, using default", "value", pollIntervalStr, "error", err, "default", pollInterval)
		} else {
			pollInterval = parsed
		}
	}
	subject := os.Getenv("FILE_INPUT_NATS_SUBJECT")
	if subject == "" {
		subject = "file.input"
	}

	// Read archive/error directory configuration
	archiveDir := os.Getenv("FILE_INPUT_ARCHIVE_DIR")
	errorDir := os.Getenv("FILE_INPUT_ERROR_DIR")
	deleteAfterProcessing := os.Getenv("FILE_INPUT_DELETE_AFTER_PROCESSING") == "true"

	// Read retry configuration
	maxRetries := 3
	if maxRetriesStr := os.Getenv("FILE_INPUT_MAX_RETRIES"); maxRetriesStr != "" {
		if parsed, err := strconv.Atoi(maxRetriesStr); err == nil && parsed > 0 {
			maxRetries = parsed
		}
	}

	retryBackoffMs := 1000
	if retryBackoffStr := os.Getenv("FILE_INPUT_RETRY_BACKOFF_MS"); retryBackoffStr != "" {
		if parsed, err := strconv.Atoi(retryBackoffStr); err == nil && parsed > 0 {
			retryBackoffMs = parsed
		}
	}

	// Read archive retention configuration
	archiveRetentionDays := 30
	if retentionStr := os.Getenv("FILE_INPUT_ARCHIVE_RETENTION_DAYS"); retentionStr != "" {
		if parsed, err := strconv.Atoi(retentionStr); err == nil && parsed > 0 {
			archiveRetentionDays = parsed
		}
	}

	// Validate configuration
	if err := validateFileInputConfig(dir, pattern, pollInterval); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = slog.Default()
	}

	bufferSizeStr := os.Getenv("FILE_INPUT_BUFFER_SIZE")
	bufferSize := 100
	if bufferSizeStr != "" {
		if parsed, err := strconv.Atoi(bufferSizeStr); err == nil && parsed > 0 {
			bufferSize = parsed
		}
	}
	return &FileConsumer{
		dir:                   dir,
		pattern:               pattern,
		pollInterval:          pollInterval,
		subject:               subject,
		archiveDir:            archiveDir,
		errorDir:              errorDir,
		deleteAfterProcessing: deleteAfterProcessing,
		maxRetries:            maxRetries,
		retryBackoffMs:        retryBackoffMs,
		archiveRetentionDays:  archiveRetentionDays,
		logger:                logger,
		messages:              make(chan *envelope.Envelope, bufferSize),
		processedFiles:        make(map[string]ProcessedFile),
		failedFiles:           make(map[string]FileRetry),
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
	dirPerm := os.FileMode(0755)
	if permStr := os.Getenv("FILE_INPUT_PERMISSIONS"); permStr != "" {
		if parsed, err := strconv.ParseUint(permStr, 8, 32); err != nil {
			f.logger.Warn("invalid FILE_INPUT_PERMISSIONS, using default", "value", permStr, "error", err, "default", dirPerm)
		} else {
			dirPerm = os.FileMode(parsed)
		}
	}
	if err := os.MkdirAll(f.dir, dirPerm); err != nil {
		return fmt.Errorf("create input directory: %w", err)
	}

	// Connect to NATS
	natsURL := os.Getenv("FILE_INPUT_NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	nc, err := nats.Connect(natsURL)
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

func (f *FileConsumer) Close() error {
	f.closedOnce.Do(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closed = true
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

		// Check if file is locked
		if f.isFileLocked(filePath) {
			f.logger.Debug("File is locked, skipping", "path", filePath)
			continue
		}

		// Check if should retry failed file
		if f.shouldRetry(filePath) {
			f.logger.Debug("Retrying failed file", "path", filePath)
		}

		// Process file
		if err := f.processFile(filePath); err != nil {
			f.logger.Error("Failed to process file", "path", filePath, "err", err)
		}
	}

	// Clean up old archives
	if f.archiveDir != "" {
		f.cleanupOldArchives()
	}
}

// calculateFileHash computes SHA256 hash of first 64KB of file
func (f *FileConsumer) calculateFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	// Read first 64KB for hashing
	limitedReader := io.LimitReader(file, 64*1024)
	if _, err := io.Copy(hash, limitedReader); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// isFileProcessed checks if file has been processed before (no changes)
func (f *FileConsumer) isFileProcessed(filePath string) (bool, error) {
	fileName := filepath.Base(filePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return false, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	processed, exists := f.processedFiles[fileName]
	if !exists {
		return false, nil
	}

	// Check if modification time changed
	if processed.Mtime != info.ModTime().Unix() {
		return false, nil
	}

	// Check if hash matches
	currentHash, err := f.calculateFileHash(filePath)
	if err != nil {
		return false, err
	}

	return currentHash == processed.Hash, nil
}

// recordProcessedFile stores file hash and modification time
func (f *FileConsumer) recordProcessedFile(filePath string, hash string, mtime int64) {
	fileName := filepath.Base(filePath)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.processedFiles[fileName] = ProcessedFile{
		Hash:  hash,
		Mtime: mtime,
	}

	// Remove from failed files on success
	delete(f.failedFiles, fileName)
}

// isFileLocked checks if file is currently open/being written
func (f *FileConsumer) isFileLocked(filePath string) bool {
	// Try to open file - if locked, this will fail
	file, err := os.Open(filePath)
	if err != nil {
		// File is likely locked or has permission issues
		return true
	}
	defer file.Close()

	// Check for recent modification (likely being written)
	info, err := os.Stat(filePath)
	if err != nil {
		return true
	}

	// If modified in last second, likely being written
	if time.Since(info.ModTime()) < 1*time.Second {
		return true
	}

	return false
}

// shouldRetry checks if we should retry a failed file
func (f *FileConsumer) shouldRetry(filePath string) bool {
	fileName := filepath.Base(filePath)

	f.mu.Lock()
	defer f.mu.Unlock()

	retry, exists := f.failedFiles[fileName]
	if !exists {
		return false
	}

	if retry.Attempts >= f.maxRetries {
		return false
	}

	// Calculate backoff: exponential (1s, 2s, 4s, 8s, ...)
	backoffMs := f.retryBackoffMs * (1 << uint(retry.Attempts-1))
	backoffDuration := time.Duration(backoffMs) * time.Millisecond

	return time.Since(retry.LastAttempt) >= backoffDuration
}

// recordFailedFile tracks retry attempts for a failed file
func (f *FileConsumer) recordFailedFile(filePath string, errMsg string) {
	fileName := filepath.Base(filePath)

	f.mu.Lock()
	defer f.mu.Unlock()

	retry, exists := f.failedFiles[fileName]
	if !exists {
		retry = FileRetry{Attempts: 0}
	}

	retry.Attempts++
	retry.LastError = errMsg
	retry.LastAttempt = time.Now()

	f.failedFiles[fileName] = retry
}

// moveToArchive moves processed file to archive directory with date subdirectory
func (f *FileConsumer) moveToArchive(filePath string) error {
	if f.archiveDir == "" {
		return nil
	}

	// Create date subdirectory (YYYY-MM-DD)
	today := time.Now().Format("2006-01-02")
	archivePath := filepath.Join(f.archiveDir, today)

	if err := os.MkdirAll(archivePath, 0o755); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}

	// Move file
	destPath := filepath.Join(archivePath, filepath.Base(filePath))
	if err := os.Rename(filePath, destPath); err != nil {
		return fmt.Errorf("move to archive: %w", err)
	}

	f.logger.Info("Moved file to archive", "source", filePath, "dest", destPath)
	return nil
}

// moveToError moves failed file to error directory with metadata
func (f *FileConsumer) moveToError(filePath string, errMsg string) error {
	if f.errorDir == "" {
		return nil
	}

	// Create date subdirectory (YYYY-MM-DD)
	today := time.Now().Format("2006-01-02")
	errorPath := filepath.Join(f.errorDir, today)

	if err := os.MkdirAll(errorPath, 0o755); err != nil {
		return fmt.Errorf("create error directory: %w", err)
	}

	// Move file
	fileName := filepath.Base(filePath)
	destPath := filepath.Join(errorPath, fileName)
	if err := os.Rename(filePath, destPath); err != nil {
		return fmt.Errorf("move to error: %w", err)
	}

	// Create .error metadata file
	errorMetadataPath := destPath + ".error"
	metadata := fmt.Sprintf("timestamp=%s\nerror=%s\n", time.Now().Format(time.RFC3339), errMsg)
	if err := os.WriteFile(errorMetadataPath, []byte(metadata), 0o644); err != nil {
		f.logger.Warn("Failed to write error metadata", "path", errorMetadataPath, "err", err)
	}

	f.logger.Error("Moved file to error directory", "source", filePath, "dest", destPath, "reason", errMsg)
	return nil
}

// handleProcessedFile determines what to do with a successfully processed file
func (f *FileConsumer) handleProcessedFile(filePath string) error {
	if f.deleteAfterProcessing {
		if err := os.Remove(filePath); err != nil {
			return fmt.Errorf("delete processed file: %w", err)
		}
		f.logger.Info("Deleted processed file", "path", filePath)
		return nil
	}

	if f.archiveDir != "" {
		return f.moveToArchive(filePath)
	}

	// Leave in place
	return nil
}

// cleanupOldArchives removes archived files older than retention period
func (f *FileConsumer) cleanupOldArchives() {
	if f.archiveDir == "" || f.archiveRetentionDays <= 0 {
		return
	}

	cutoffTime := time.Now().AddDate(0, 0, -f.archiveRetentionDays)

	entries, err := os.ReadDir(f.archiveDir)
	if err != nil {
		f.logger.Debug("Failed to read archive directory", "path", f.archiveDir, "err", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirPath := filepath.Join(f.archiveDir, entry.Name())
		info, err := os.Stat(dirPath)
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoffTime) {
			if err := os.RemoveAll(dirPath); err != nil {
				f.logger.Warn("Failed to cleanup old archive", "path", dirPath, "err", err)
			} else {
				f.logger.Info("Cleaned up old archive directory", "path", dirPath)
			}
		}
	}
}

// processFile reads a file and publishes it as an envelope
func (f *FileConsumer) processFile(filePath string) error {
	// Check if already processed
	isProcessed, err := f.isFileProcessed(filePath)
	if err != nil {
		f.logger.Warn("Failed to check if file was processed", "path", filePath, "err", err)
	} else if isProcessed {
		f.logger.Debug("File already processed, skipping", "path", filePath)
		return nil
	}

	// Read file contents
	content, err := os.ReadFile(filePath)
	if err != nil {
		f.recordFailedFile(filePath, err.Error())
		if f.failedFiles[filepath.Base(filePath)].Attempts >= f.maxRetries {
			if err := f.moveToError(filePath, fmt.Sprintf("max retries exceeded: %v", err)); err != nil {
				f.logger.Error("Failed to move file to error directory", "path", filePath, "err", err)
			}
			return nil
		}
		return fmt.Errorf("read file: %w", err)
	}

	// Calculate file hash for reprocessing prevention
	fileHash, err := f.calculateFileHash(filePath)
	if err != nil {
		f.logger.Warn("Failed to calculate file hash", "path", filePath, "err", err)
		fileHash = ""
	}

	// Create envelope using the proper structure
	env := envelope.New()
	env.ID = uuid.New().String()
	env.Source = "FileConsumer"
	env.Payload = content
	env.PayloadSize = int64(len(content))
	env.ContentType = f.detectContentType(filePath)

	// Check for context cancellation before attempting to send to the channel
	if err := f.ctx.Err(); err != nil {
		return err
	}

	// Send to messages channel with timeout
	sendTimeout := 5 * time.Second
	select {
	case f.messages <- env:
		// Only publish to NATS after successful channel send
		data, err := envelope.Marshal(env)
		if err != nil {
			f.recordFailedFile(filePath, err.Error())
			return fmt.Errorf("marshal envelope: %w", err)
		}
		if err := f.nc.Publish(f.subject, data); err != nil {
			f.recordFailedFile(filePath, err.Error())
			return fmt.Errorf("publish to NATS: %w", err)
		}

		// Handle processed file (archive, delete, or leave)
		if err := f.handleProcessedFile(filePath); err != nil {
			f.logger.Error("Failed to handle processed file", "path", filePath, "err", err)
		}

		// Record file as processed
		info, _ := os.Stat(filePath)
		mtime := int64(0)
		if info != nil {
			mtime = info.ModTime().Unix()
		}
		f.recordProcessedFile(filePath, fileHash, mtime)

		f.logger.Info("Processed file", "filename", filepath.Base(filePath), "size", len(content), "id", env.ID)
		return nil
	case <-time.After(sendTimeout):
		return fmt.Errorf("timeout sending envelope to messages channel (buffer may be full)")
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

// detectContentType determines the MIME type from file extension
func (f *FileConsumer) detectContentType(filePath string) string {
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
