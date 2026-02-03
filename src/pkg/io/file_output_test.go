package io

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	err := validateFileOutputConfig("", "{{.ID}}.{{.Extension}}", 0o644)
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
	err := validateFileOutputConfig("/tmp", "", 0o644)
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
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("NewFileProducer() error = %v", err)
	}

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

	// Check that permissions match (mask with 0777 to ignore platform-specific bits)
	actualPerms := info.Mode().Perm() & 0o777
	expectedPerms := producer.permissions & 0o777
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

	// Verify path traversal prevention: files should be within tmpDir
	for _, entry := range entries {
		filePath := filepath.Join(tmpDir, entry.Name())
		absFilePath, err := filepath.Abs(filePath)
		if err != nil {
			t.Fatalf("Failed to get absolute path: %v", err)
		}

		absTmpDir, err := filepath.Abs(tmpDir)
		if err != nil {
			t.Fatalf("Failed to get absolute tmpDir path: %v", err)
		}

		// Verify file is within output directory
		if !strings.HasPrefix(absFilePath, absTmpDir) {
			t.Errorf("Path traversal detected: file %s is outside output directory %s", absFilePath, absTmpDir)
		}
	}

	// Verify no files were created in parent directories
	parentDir := filepath.Dir(tmpDir)
	parentEntries, err := os.ReadDir(parentDir)
	if err != nil {
		t.Fatalf("Failed to read parent directory: %v", err)
	}

	// Count files created by this test in parent directory (should be 0)
	for _, entry := range parentEntries {
		if strings.Contains(entry.Name(), "test-traversal") {
			t.Errorf("Path traversal allowed: file created in parent directory: %s", entry.Name())
		}
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

// Test 9: Streaming write functionality with large files
func TestFileProducer_StreamWrite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")
	t.Setenv("FILE_OUTPUT_CHUNK_SIZE", "1024")
	t.Setenv("FILE_OUTPUT_FSYNC_INTERVAL", "5")

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

	// Create large payload (100KB)
	largePayload := make([]byte, 100*1024)
	for i := range largePayload {
		largePayload[i] = byte(i % 256)
	}

	// Create test envelope
	env := envelope.New()
	env.ID = "test-large"
	env.Payload = largePayload
	env.ContentType = "application/octet-stream"
	env.Source = "TestProducer"

	// Write envelope
	err = producer.Write(ctx, env)
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}

	// Verify file was created with correct size
	expectedFile := filepath.Join(tmpDir, "test-large.bin")
	content, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Errorf("Failed to read output file: %v", err)
	}

	if len(content) != len(largePayload) {
		t.Errorf("File size mismatch: got %d, want %d", len(content), len(largePayload))
	}

	if !bytes.Equal(content, largePayload) {
		t.Error("File content does not match original payload")
	}

	producer.Close()
}

// Test 10: File organization by type
func TestFileProducer_OrganizeByType(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")
	t.Setenv("FILE_OUTPUT_CREATE_SUBDIRS", "true")
	t.Setenv("FILE_OUTPUT_ORGANIZE_BY", "type")

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

	// Create test envelopes with different content types
	tests := []struct {
		contentType string
		expectedDir string
	}{
		{"application/json", "application-json"},
		{"text/csv", "text-csv"},
		{"application/xml", "application-xml"},
	}

	for _, tt := range tests {
		env := envelope.New()
		env.ID = "test-type-" + tt.expectedDir
		env.Payload = []byte("test data")
		env.ContentType = tt.contentType
		env.Source = "TestProducer"

		err = producer.Write(ctx, env)
		if err != nil {
			t.Errorf("Write() error for %s: %v", tt.contentType, err)
		}

		// Verify subdirectory was created
		expectedPath := filepath.Join(tmpDir, tt.expectedDir)
		if _, err := os.Stat(expectedPath); err != nil {
			t.Errorf("Subdirectory not created for %s: %v", tt.contentType, err)
		}
	}

	producer.Close()
}

// Test 11: File organization by date
func TestFileProducer_OrganizeByDate(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")
	t.Setenv("FILE_OUTPUT_CREATE_SUBDIRS", "true")
	t.Setenv("FILE_OUTPUT_ORGANIZE_BY", "date")

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
	env.ID = "test-date"
	env.Payload = []byte("test data")
	env.ContentType = "text/plain"
	env.Source = "TestProducer"

	err = producer.Write(ctx, env)
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}

	// Verify date subdirectory was created (YYYY/MM/DD format)
	today := time.Now().Format("2006/01/02")
	expectedPath := filepath.Join(tmpDir, today)
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("Date subdirectory not created: %v", err)
	}

	producer.Close()
}

// Test 12: Disk space validation
func TestFileProducer_DiskSpaceCheck(t *testing.T) {
	if os.Getenv("CI") == "true" {
		t.Skip("Skipping disk space test in CI environment")
	}

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

	// Test successful disk space check for normal file
	env := envelope.New()
	env.ID = "test-diskspace"
	env.Payload = []byte("test data")
	env.ContentType = "text/plain"
	env.Source = "TestProducer"

	err = producer.Write(ctx, env)
	if err != nil {
		t.Errorf("Write() with sufficient disk space failed: %v", err)
	}

	producer.Close()
}

// Test 13: Envelope validation
func TestFileProducer_ValidateEnvelope(t *testing.T) {
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

	// Test 1: Empty payload
	env := envelope.New()
	env.ID = "test-empty"
	env.Payload = []byte("")
	env.ContentType = "text/plain"
	err = producer.Write(ctx, env)
	if err == nil {
		t.Error("Write() with empty payload should fail")
	}

	// Test 2: Empty ID
	env = envelope.New()
	env.ID = ""
	env.Payload = []byte("test")
	env.ContentType = "text/plain"
	err = producer.Write(ctx, env)
	if err == nil {
		t.Error("Write() with empty ID should fail")
	}

	// Test 3: Exceeding max file size
	env = envelope.New()
	env.ID = "test-large-exceeds"
	env.Payload = make([]byte, 2*1024*1024*1024) // 2GB (exceeds 1GB default)
	env.ContentType = "text/plain"
	err = producer.Write(ctx, env)
	if err == nil {
		t.Error("Write() with payload exceeding max size should fail")
	}

	producer.Close()
}

// Test 14: Checksum calculation in streamWrite
func TestFileProducer_ChecksumCalculation(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", tmpDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")
	t.Setenv("FILE_OUTPUT_CHUNK_SIZE", "512")

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

	// Create test envelope with known payload
	testData := []byte("the quick brown fox jumps over the lazy dog")
	env := envelope.New()
	env.ID = "test-checksum"
	env.Payload = testData
	env.ContentType = "text/plain"
	env.Source = "TestProducer"

	err = producer.Write(ctx, env)
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}

	// Verify checksum is logged (indirectly by verifying file content)
	expectedFile := filepath.Join(tmpDir, "test-checksum.txt")
	content, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Errorf("Failed to read output file: %v", err)
	}

	if !bytes.Equal(content, testData) {
		t.Error("File content does not match original payload")
	}

	producer.Close()
}
