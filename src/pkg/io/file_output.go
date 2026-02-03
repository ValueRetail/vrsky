package io

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// FileProducer writes message envelopes to the file system
type FileProducer struct {
	// Configuration
	outputDir      string
	fileNameFormat string
	permissions    os.FileMode

	// Runtime
	logger     *slog.Logger
	mu         sync.Mutex
	closed     bool
	closedOnce sync.Once
}

// NewFileProducer creates a new file producer from environment configuration
func NewFileProducer(logger *slog.Logger) (*FileProducer, error) {
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
			if logger != nil {
				logger.Warn("Invalid FILE_OUTPUT_PERMISSIONS; using default permissions", "value", permissionsStr, "error", err)
			} else {
				slog.Default().Warn("Invalid FILE_OUTPUT_PERMISSIONS; using default permissions", "value", permissionsStr, "error", err)
			}
		}
	}

	// Validate configuration
	if err := validateFileOutputConfig(outputDir, fileNameFormat, permissions); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = slog.Default()
	}

	return &FileProducer{
		outputDir:      outputDir,
		fileNameFormat: fileNameFormat,
		permissions:    permissions,
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
	if err := os.MkdirAll(f.outputDir, f.permissions); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Validate template can be parsed
	if _, err := template.New("filename").Parse(f.fileNameFormat); err != nil {
		return fmt.Errorf("invalid filename template: %w", err)
	}

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

	if env == nil {
		return fmt.Errorf("envelope cannot be nil")
	}

	// Generate filename
	fileName, err := f.generateFileName(env)
	if err != nil {
		return fmt.Errorf("generate filename: %w", err)
	}

	// Construct full path
	filePath := filepath.Join(f.outputDir, fileName)

	// Sanitize path to prevent directory traversal
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}

	absOutputDir, err := filepath.Abs(f.outputDir)

	if err != nil {
		return fmt.Errorf("resolve output directory path: %w", err)
	}

	relPath, err := filepath.Rel(absOutputDir, absPath)
	if err != nil {
		return fmt.Errorf("resolve relative path: %w", err)
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("path traversal detected: %s is outside output directory", filePath)
	}

	// Write payload to file
	if err := os.WriteFile(filePath, env.Payload, f.permissions); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	f.logger.Info("Wrote file", "filename", fileName, "size", len(env.Payload), "id", env.ID)
	return nil
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

	// Parse and execute template
	tmpl, err := template.New("filename").Parse(f.fileNameFormat)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	fileName := buf.String()

	// Additional sanitization for filename
	fileName = sanitizeForFilename(fileName)
	if fileName == "" {
		return "", fmt.Errorf("filename template resulted in empty string")
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
