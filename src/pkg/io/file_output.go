package io

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// FileProducer writes message envelopes to the file system
type FileProducer struct {
	// Configuration
	outputDir        string
	fileNameFormat   string
	permissions      os.FileMode
	chunkSize        int64
	maxFileSize      int64
	fsyncInterval    int
	createSubdirs    bool
	organizeBy       string

	// Runtime
	absOutputDir      string
	fileNameTemplate  *template.Template
	logger            *slog.Logger
	mu                sync.Mutex
	closed            bool
	closedOnce        sync.Once
}

// NewFileProducer creates a new file producer from environment configuration
func NewFileProducer(logger *slog.Logger) (*FileProducer, error) {
	// Initialize logger early so it's available for all operations
	if logger == nil {
		logger = slog.Default()
	}

	// Read configuration from environment variables
	outputDir := os.Getenv("FILE_OUTPUT_DIR")
	if outputDir == "" {
		outputDir = "/tmp/file-output"
	}

	fileNameFormat := os.Getenv("FILE_OUTPUT_FILENAME_FORMAT")
	if fileNameFormat == "" {
		fileNameFormat = "{{.ID}}.{{.Extension}}"
	}

	permissionsStr := os.Getenv("FILE_OUTPUT_PERMISSIONS")
	permissions := os.FileMode(0o644)
	if permissionsStr != "" {
		if parsed, err := strconv.ParseInt(permissionsStr, 8, 32); err == nil {
			permissions = os.FileMode(parsed)
		} else {
			logger.Warn("Invalid FILE_OUTPUT_PERMISSIONS; using default permissions", "value", permissionsStr, "error", err)
		}
	}

	// Read chunk size for streaming writes (default: 64KB)
	chunkSize := int64(64 * 1024)
	if chunkSizeStr := os.Getenv("FILE_OUTPUT_CHUNK_SIZE"); chunkSizeStr != "" {
		if parsed, err := strconv.ParseInt(chunkSizeStr, 10, 64); err == nil {
			if parsed <= 0 {
				logger.Warn("FILE_OUTPUT_CHUNK_SIZE must be positive; using default 64KB", "value", parsed)
			} else {
				chunkSize = parsed
			}
		} else {
			logger.Warn("Invalid FILE_OUTPUT_CHUNK_SIZE; using default 64KB", "value", chunkSizeStr, "error", err)
		}
	}

	// Read max file size (default: 1GB)
	maxFileSize := int64(1024 * 1024 * 1024)
	if maxFileSizeStr := os.Getenv("FILE_OUTPUT_MAX_FILE_SIZE"); maxFileSizeStr != "" {
		if parsed, err := strconv.ParseInt(maxFileSizeStr, 10, 64); err == nil {
			maxFileSize = parsed
		} else {
			logger.Warn("Invalid FILE_OUTPUT_MAX_FILE_SIZE; using default 1GB", "value", maxFileSizeStr, "error", err)
		}
	}

	// Read fsync interval (default: 10 chunks)
	// fsyncInterval = 0 means never fsync (valid), negative values are invalid
	fsyncInterval := 10
	if fsyncIntervalStr := os.Getenv("FILE_OUTPUT_FSYNC_INTERVAL"); fsyncIntervalStr != "" {
		if parsed, err := strconv.ParseInt(fsyncIntervalStr, 10, 32); err == nil {
			if parsed < 0 {
				logger.Warn("FILE_OUTPUT_FSYNC_INTERVAL cannot be negative; using default 10", "value", parsed)
			} else {
				fsyncInterval = int(parsed)
			}
		} else {
			logger.Warn("Invalid FILE_OUTPUT_FSYNC_INTERVAL; using default 10", "value", fsyncIntervalStr, "error", err)
		}
	}

	// Read subdirectory creation flag (default: false)
	createSubdirs := false
	if createSubdirsStr := os.Getenv("FILE_OUTPUT_CREATE_SUBDIRS"); createSubdirsStr != "" {
		createSubdirs = strings.ToLower(createSubdirsStr) == "true"
	}

	// Read organization strategy (default: "none")
	organizeBy := os.Getenv("FILE_OUTPUT_ORGANIZE_BY")
	if organizeBy == "" {
		organizeBy = "none"
	}
	// Validate organization strategy
	if organizeBy != "none" && organizeBy != "type" && organizeBy != "date" && organizeBy != "source" {
		logger.Warn("Invalid FILE_OUTPUT_ORGANIZE_BY; using 'none'", "value", organizeBy)
		organizeBy = "none"
	}

	// Validate configuration
	if err := validateFileOutputConfig(outputDir, fileNameFormat, permissions); err != nil {
		return nil, err
	}

	return &FileProducer{
		outputDir:      outputDir,
		fileNameFormat: fileNameFormat,
		permissions:    permissions,
		chunkSize:      chunkSize,
		maxFileSize:    maxFileSize,
		fsyncInterval:  fsyncInterval,
		createSubdirs:  createSubdirs,
		organizeBy:     organizeBy,
		logger:         logger,
	}, nil
}

// Start initializes the output directory
func (f *FileProducer) Start(ctx context.Context) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return fmt.Errorf("file producer already stopped")
	}
	f.mu.Unlock()

	// Create output directory if it doesn't exist
	dirPermissions := f.permissions | 0o111
	if err := os.MkdirAll(f.outputDir, dirPermissions); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Cache the absolute output directory path for use in Write()
	absDir, err := filepath.Abs(f.outputDir)
	if err != nil {
		return fmt.Errorf("resolve output directory path: %w", err)
	}
	f.absOutputDir = absDir

	// Parse and cache the filename template for use in Write()
	tmpl, err := template.New("filename").Parse(f.fileNameFormat)
	if err != nil {
		return fmt.Errorf("invalid filename template: %w", err)
	}
	f.fileNameTemplate = tmpl

	f.logger.Info("File Producer started", "dir", f.outputDir, "format", f.fileNameFormat, "permissions", fmt.Sprintf("%o", f.permissions))
	return nil
}

// Write writes an envelope to a file in the output directory
func (f *FileProducer) Write(ctx context.Context, env *envelope.Envelope) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return fmt.Errorf("file producer is closed")
	}
	f.mu.Unlock()

	// Verify that Start() has been called
	if f.fileNameTemplate == nil {
		return fmt.Errorf("file producer not started: call Start() before Write()")
	}

	// Validate envelope before processing
	if err := f.validateEnvelope(env); err != nil {
		return fmt.Errorf("invalid envelope: %w", err)
	}

	// Check disk space availability
	if err := f.checkDiskSpace(int64(len(env.Payload))); err != nil {
		return fmt.Errorf("disk space check failed: %w", err)
	}

	// Get organized subdirectory path if applicable
	organizedPath, err := f.getOrganizedPath(env)
	if err != nil {
		return fmt.Errorf("get organized path: %w", err)
	}

	// Generate filename
	fileName, err := f.generateFileName(env)
	if err != nil {
		return fmt.Errorf("generate filename: %w", err)
	}

	// Construct output directory (with organization subdirectory if applicable)
	outputDir := f.outputDir
	if organizedPath != "" {
		outputDir = filepath.Join(f.outputDir, organizedPath)
		// Create subdirectory structure
		dirPermissions := f.permissions | 0o111
		if err := os.MkdirAll(outputDir, dirPermissions); err != nil {
			return fmt.Errorf("create subdirectory: %w", err)
		}
	}

	// Construct full path
	filePath := filepath.Join(outputDir, fileName)

	// Sanitize path to prevent directory traversal
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	// Resolve symlinks in the target path. The file may not exist yet, so
	// ignore "not exist" errors and fall back to the absolute path.
	resolvedAbsPath := absPath
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		resolvedAbsPath = resolved
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("resolve symlinks for target path: %w", err)
	}

	// Use cached absolute output directory (calculated in Start())
	absOutputDir := f.absOutputDir

	// Resolve symlinks in the output directory
	resolvedOutputDir, err := filepath.EvalSymlinks(absOutputDir)
	if err != nil {
		return fmt.Errorf("resolve symlinks for output directory: %w", err)
	}

	relPath, err := filepath.Rel(resolvedOutputDir, resolvedAbsPath)
	if err != nil {
		return fmt.Errorf("resolve relative path: %w", err)
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("path traversal detected: %s is outside output directory", filePath)
	}

	// Open file for writing
	file, err := os.OpenFile(resolvedAbsPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.permissions)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			f.logger.Warn("Failed to close file after write", "path", resolvedAbsPath, "error", closeErr)
		}
	}()

	// Write payload using streaming approach with checksums
	checksum, err := f.streamWrite(file, env.Payload)
	if err != nil {
		// Attempt to remove partially written file
		_ = os.Remove(resolvedAbsPath)
		return fmt.Errorf("stream write: %w", err)
	}

	f.logger.Info("Wrote file", "filename", fileName, "size", len(env.Payload), "id", env.ID, "checksum", checksum)
	return nil
}

// checkDiskSpace verifies that the output directory has sufficient free space
func (f *FileProducer) checkDiskSpace(requiredSize int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(f.absOutputDir, &stat); err != nil {
		return fmt.Errorf("check disk space: %w", err)
	}

	// Calculate available space in bytes
	available := int64(stat.Bavail) * int64(stat.Bsize)

	// Require 2x the file size to be safe (avoid running disk out of space)
	// Check for overflow: if requiredSize > maxInt64/2, operation would overflow
	if requiredSize > math.MaxInt64/2 {
		return fmt.Errorf("payload size too large: %d bytes (max safe size: %d bytes)", requiredSize, math.MaxInt64/2)
	}
	required := requiredSize * 2
	if available < required {
		return fmt.Errorf("insufficient disk space: need %d bytes, available %d bytes", required, available)
	}

	return nil
}

// validateEnvelope checks that an envelope has required fields and valid payload
func (f *FileProducer) validateEnvelope(env *envelope.Envelope) error {
	if env == nil {
		return fmt.Errorf("envelope cannot be nil")
	}

	if env.ID == "" {
		return fmt.Errorf("envelope ID cannot be empty")
	}

	if env.Payload == nil {
		return fmt.Errorf("envelope payload cannot be nil")
	}

	payloadSize := int64(len(env.Payload))
	if payloadSize == 0 {
		return fmt.Errorf("envelope payload cannot be empty")
	}

	if payloadSize > f.maxFileSize {
		return fmt.Errorf("payload size (%d bytes) exceeds maximum (%d bytes)", payloadSize, f.maxFileSize)
	}

	return nil
}

// getOrganizedPath returns the subdirectory path based on organization strategy
func (f *FileProducer) getOrganizedPath(env *envelope.Envelope) (string, error) {
	if !f.createSubdirs || f.organizeBy == "none" {
		return "", nil
	}

	switch f.organizeBy {
	case "type":
		// Organize by content type (e.g., "application-json", "text-csv")
		contentType := env.ContentType
		if contentType == "" {
			contentType = "unknown"
		}
		contentType = strings.ReplaceAll(contentType, "/", "-")
		return sanitizeForFilename(contentType), nil

	case "date":
		// Organize by date (YYYY/MM/DD)
		dateStr := env.CreatedAt.Format("2006/01/02")
		return dateStr, nil

	case "source":
		// Organize by source system
		source := env.Source
		if source == "" {
			source = "unknown"
		}
		return sanitizeForFilename(source), nil

	default:
		return "", nil
	}
}

// streamWrite writes payload from a reader to a file in chunks with periodic fsync
// Returns the SHA256 checksum of the written data
func (f *FileProducer) streamWrite(file *os.File, payload []byte) (string, error) {
	hash := sha256.New()
	bytesWritten := int64(0)
	chunksWritten := 0

	// Write payload in chunks
	for bytesWritten < int64(len(payload)) {
		// Determine chunk size for this iteration
		remaining := int64(len(payload)) - bytesWritten
		currentChunkSize := f.chunkSize
		if remaining < currentChunkSize {
			currentChunkSize = remaining
		}

		// Extract and write chunk
		chunk := payload[bytesWritten : bytesWritten+currentChunkSize]
		n, err := file.Write(chunk)
		if err != nil {
			return "", fmt.Errorf("write chunk: %w", err)
		}

		if int64(n) != currentChunkSize {
			return "", fmt.Errorf("partial write: wrote %d bytes, expected %d", n, currentChunkSize)
		}

		// Update hash
		hash.Write(chunk)
		bytesWritten += int64(n)
		chunksWritten++

		// Periodic fsync to prevent memory exhaustion
		if f.fsyncInterval > 0 && chunksWritten%f.fsyncInterval == 0 {
			if err := file.Sync(); err != nil {
				return "", fmt.Errorf("fsync: %w", err)
			}
		}
	}

	// Final fsync to ensure all data is written to disk
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("final fsync: %w", err)
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Close gracefully stops the file producer
func (f *FileProducer) Close() error {
	f.closedOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()

		f.logger.Info("File Producer closed")
	})

	return nil
}

// generateFileName generates a filename from the configured template
func (f *FileProducer) generateFileName(env *envelope.Envelope) (string, error) {
	// Sanitize Source for safe use in filenames; log if sanitization changes the value.
	safeSource := sanitizeForFilename(env.Source)
	if safeSource != env.Source {
		f.logger.Warn(
			"Source contains characters unsafe for filenames; using sanitized value in filename",
			"source", env.Source,
			"sanitizedSource", safeSource,
		)
	}

	// Prepare template data
	data := map[string]any{
		"ID":        env.ID,
		"Source":    safeSource,
		"Extension": f.deriveExtension(env.ContentType),
		"Timestamp": env.CreatedAt.Format(time.RFC3339),
	}

	// Use cached template for better performance
	var buf bytes.Buffer
	if err := f.fileNameTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute filename template: %w", err)
	}

	rawFileName := buf.String()

	// Additional sanitization for filename
	fileName := sanitizeForFilename(rawFileName)
	if fileName == "" {
		return "", fmt.Errorf("filename template produced only unsafe characters that were sanitized away, resulting in an empty filename (raw: %q)", rawFileName)
	}

	return fileName, nil
}

// deriveExtension maps content type to file extension (without leading dot)
func (f *FileProducer) deriveExtension(contentType string) string {
	switch {
	case strings.Contains(contentType, "application/json"):
		return "json"
	case strings.Contains(contentType, "text/plain"):
		return "txt"
	case strings.Contains(contentType, "text/csv"):
		return "csv"
	case strings.Contains(contentType, "application/xml"):
		return "xml"
	case strings.Contains(contentType, "application/yaml"):
		return "yaml"
	case strings.Contains(contentType, "text/yaml"):
		return "yaml"
	default:
		return "bin"
	}
}

// sanitizeForFilename removes or replaces characters that are unsafe in filenames
func sanitizeForFilename(s string) string {
	// Replace unsafe characters with underscores
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
		"\n", "_",
		"\r", "_",
		"\x00", "_",
	)
	return replacer.Replace(s)
}

// validateFileOutputConfig validates the file output configuration
func validateFileOutputConfig(dir, format string, perms os.FileMode) error {
	if dir == "" {
		return fmt.Errorf("output directory cannot be empty")
	}

	if format == "" {
		return fmt.Errorf("filename format cannot be empty")
	}

	// Validate permissions are in valid range (0-0777)
	if perms > 0o777 {
		return fmt.Errorf("invalid file permissions: %o (must be 0000-0777)", perms)
	}

	return nil
}
