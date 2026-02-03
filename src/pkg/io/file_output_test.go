package io

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func TestFileProducer_NewFileProducer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("NewFileProducer() error = %v", err)
	}
	if producer == nil {
		t.Error("NewFileProducer() returned nil")
	}
	if producer.outputDir == "" {
		t.Error("NewFileProducer() outputDir is empty")
	}
	if producer.fileNameFormat == "" {
		t.Error("NewFileProducer() fileNameFormat is empty")
	}
}

func TestFileProducer_ValidateFails_EmptyDir(t *testing.T) {
	err := validateFileOutputConfig("", "{{.ID}}.{{.Extension}}", 0644)
	if err == nil {
		t.Error("validateFileOutputConfig() should fail with empty directory")
	}
}

func TestFileProducer_ValidateFails_InvalidPermissions(t *testing.T) {
	err := validateFileOutputConfig("/tmp", "{{.ID}}.{{.Extension}}", 0o1000)
	if err == nil {
		t.Error("validateFileOutputConfig() should fail with invalid permissions")
	}
}

func TestFileProducer_ValidateFails_EmptyFormat(t *testing.T) {
	err := validateFileOutputConfig("/tmp", "", 0644)
	if err == nil {
		t.Error("validateFileOutputConfig() should fail with empty format")
	}
}

func TestFileProducer_Start(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")
	t.Setenv("FILE_OUTPUT_PERMISSIONS", "0644")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("NewFileProducer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = producer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(tmpDir); err != nil {
		t.Errorf("Output directory not created: %v", err)
	}

	producer.Close()
}

func TestFileProducer_WriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")
	t.Setenv("FILE_OUTPUT_PERMISSIONS", "0644")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("NewFileProducer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = producer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Create test envelope
	env := envelope.New()
	env.ID = "test-123"
	env.Payload = []byte("hello world")
	env.ContentType = "text/plain"
	env.Source = "TestProducer"

	// Write envelope
	err = producer.Write(ctx, env)
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}

	// Verify file was created
	expectedFile := filepath.Join(tmpDir, "test-123.txt")
	t.Logf("Looking for file at: %s", expectedFile)
	
	// List directory contents
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Logf("Failed to list directory: %v", err)
	} else {
		t.Logf("Directory contains %d entries:", len(entries))
		for _, e := range entries {
			t.Logf("  - %s", e.Name())
		}
	}
	
	content, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Errorf("Failed to read output file: %v", err)
	}

	if string(content) != "hello world" {
		t.Errorf("File content mismatch: got %s, want hello world", string(content))
	}

	producer.Close()
}

func TestFileProducer_FileNameGeneration(t *testing.T) {
	tests := []struct {
		format      string
		contentType string
		expectedEnd string
	}{
		{"{{.ID}}.{{.Extension}}", "application/json", "test-123.json"},
		{"{{.ID}}.{{.Extension}}", "text/plain", "test-123.txt"},
		{"output-{{.ID}}.{{.Extension}}", "text/csv", "output-test-123.csv"},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Setenv("FILE_OUTPUT_DIR", tmpDir)
			t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", tt.format)
			t.Setenv("FILE_OUTPUT_PERMISSIONS", "0644")

			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			producer, err := NewFileProducer(logger)
			if err != nil {
				t.Fatalf("NewFileProducer() error = %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err = producer.Start(ctx)
			if err != nil {
				t.Errorf("Start() error = %v", err)
			}

			// Create test envelope
			env := envelope.New()
			env.ID = "test-123"
			env.Payload = []byte("test")
			env.ContentType = tt.contentType
			env.Source = "TestProducer"

			err = producer.Write(ctx, env)
			if err != nil {
				t.Errorf("Write() error = %v", err)
			}

			// Verify file was created with expected name
			expectedFile := filepath.Join(tmpDir, tt.expectedEnd)
			if _, err := os.Stat(expectedFile); err != nil {
				t.Errorf("File not created at expected path: %s", expectedFile)
			}

			producer.Close()
		})
	}
}

func TestFileProducer_ContentTypeToExtension(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	producer, _ := NewFileProducer(logger)

	cases := []struct {
		contentType string
		expected    string
	}{
		{"application/json", "json"},
		{"text/plain", "txt"},
		{"text/csv", "csv"},
		{"application/xml", "xml"},
		{"application/yaml", "yaml"},
		{"text/yaml", "yaml"},
		{"application/octet-stream", "bin"},
		{"unknown/type", "bin"},
	}

	for _, tt := range cases {
		t.Run(tt.contentType, func(t *testing.T) {
			got := producer.deriveExtension(tt.contentType)
			if got != tt.expected {
				t.Errorf("deriveExtension(%s) = %s, want %s", tt.contentType, got, tt.expected)
			}
		})
	}
}

func TestFileProducer_PermissionRespect(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")
	t.Setenv("FILE_OUTPUT_PERMISSIONS", "0600")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("NewFileProducer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = producer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Create and write test envelope
	env := envelope.New()
	env.ID = "test-perms"
	env.Payload = []byte("test")
	env.ContentType = "text/plain"

	err = producer.Write(ctx, env)
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}

	// Verify permissions
	filePath := filepath.Join(tmpDir, "test-perms.txt")
	info, err := os.Stat(filePath)
	if err != nil {
		t.Errorf("Failed to stat file: %v", err)
	}

	// Check that permissions match or are close (mask with 0777 to ignore platform-specific bits)
	actualPerms := info.Mode().Perm()
	expectedPerms := os.FileMode(0o600)
	if actualPerms != expectedPerms {
		t.Errorf("File permissions mismatch: got %o, want %o", actualPerms, expectedPerms)
	}

	producer.Close()
}

func TestFileProducer_Close(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("NewFileProducer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = producer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Close should work
	err = producer.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Should be marked as closed
	if !producer.closed {
		t.Error("Producer not marked as closed after Close()")
	}

	// Calling Close() again should be safe (idempotent)
	err = producer.Close()
	if err != nil {
		t.Errorf("Second Close() error = %v", err)
	}
}

func TestFileProducer_PathTraversalPrevention(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.Source}}/{{.ID}}.{{.Extension}}")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("NewFileProducer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = producer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Create test envelope with source containing path traversal
	env := envelope.New()
	env.ID = "test-traversal"
	env.Payload = []byte("test")
	env.ContentType = "text/plain"
	env.Source = "../../../etc/passwd"

	// Write should succeed but sanitize the path
	err = producer.Write(ctx, env)
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}

	// Verify the file was created in the correct directory (with underscores replacing slashes)
	// The exact behavior depends on sanitization, but it should not create files outside tmpDir
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Errorf("Failed to read output directory: %v", err)
	}

	if len(entries) == 0 {
		t.Error("No files created in output directory")
	}

	producer.Close()
}

func TestFileProducer_NilEnvelopeError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("NewFileProducer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = producer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Try to write nil envelope
	err = producer.Write(ctx, nil)
	if err == nil {
		t.Error("Write(nil) should return error")
	}

	producer.Close()
}
