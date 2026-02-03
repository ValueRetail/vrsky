package io

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileConsumer_NewFileConsumer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}
	if consumer == nil {
		t.Error("NewFileConsumer() returned nil")
	}
	if consumer.dir == "" {
		t.Error("NewFileConsumer() dir is empty")
	}
}

func TestFileConsumer_ValidateFails_NegativePollInterval(t *testing.T) {
	err := validateFileInputConfig("/tmp", "*", -1*time.Second)
	if err == nil {
		t.Error("validateFileInputConfig() should fail with negative interval")
	}
}

func TestFileConsumer_ValidateFails_ZeroPollInterval(t *testing.T) {
	err := validateFileInputConfig("/tmp", "*", 0*time.Second)
	if err == nil {
		t.Error("validateFileInputConfig() should fail with zero interval")
	}
}

func TestFileConsumer_ValidateFails_EmptyPattern(t *testing.T) {
	err := validateFileInputConfig("/tmp", "", 5*time.Second)
	if err == nil {
		t.Error("validateFileInputConfig() should fail with empty pattern")
	}
}

func TestFileConsumer_Start(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_PATTERN", "*")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "1s")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Close consumer
	err = consumer.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestFileConsumer_ReadsFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_PATTERN", "*")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "1s")

	// Create test file
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := []byte("hello world")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Read envelope with timeout instead of arbitrary sleep
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()

	env, err := consumer.Read(readCtx)
	if err != nil {
		t.Errorf("Read() error = %v", err)
	}
	if env == nil {
		t.Error("Read() returned nil envelope")
	}

	consumer.Close()
}

func TestFileConsumer_EnvelopeStructure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_PATTERN", "*.txt")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "1s")

	// Create test file
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := []byte("test payload")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Read envelope (waits until processed or context timeout)
	env, err := consumer.Read(ctx)
	if err != nil {
		t.Errorf("Read() error = %v", err)
	}

	// Verify structure
	if env.ID == "" {
		t.Error("Envelope ID is empty")
	}
	if env.CreatedAt.IsZero() {
		t.Error("Envelope CreatedAt is zero")
	}
	if string(env.Payload) != "test payload" {
		t.Errorf("Envelope payload mismatch: got %s, want test payload", env.Payload)
	}

	consumer.Close()
}

func TestFileConsumer_Metadata(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_PATTERN", "*.json")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "1s")

	// Create test JSON file
	testFile := filepath.Join(tmpDir, "data.json")
	testContent := []byte(`{"key":"value"}`)
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Read envelope, waiting up to 5 seconds for the file to be processed
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	env, err := consumer.Read(readCtx)

	// Verify fields
	if env.Source != "FileConsumer" {
		t.Errorf("Envelope source mismatch: got %s, want FileConsumer", env.Source)
	}
	if env.ContentType != "application/json" {
		t.Errorf("Envelope content_type mismatch: got %s, want application/json", env.ContentType)
	}
	if env.PayloadSize != int64(len(testContent)) {
		t.Errorf("Envelope payload_size mismatch: got %d, want %d", env.PayloadSize, len(testContent))
	}

	consumer.Close()
}

func TestFileConsumer_PatternMatching(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_PATTERN", "*.json")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "1s")

	// Create multiple files
	files := []string{"file1.json", "file2.json", "file3.txt"}
	for _, filename := range files {
		path := filepath.Join(tmpDir, filename)
		if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}


	// Read up to 2 envelopes (only .json files), allowing time for processing
	count := 0

	deadline, _ := ctx.Deadline()

	for count < 2 && time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		readCtx, cancelRead := context.WithTimeout(ctx, remaining)
		_, err := consumer.Read(readCtx)
		cancelRead()
		if err == nil {

			count++
		}
	}

	if count != 2 {
		t.Errorf("Expected 2 envelopes from pattern matching, got %d", count)
	}

	consumer.Close()
}

func TestFileConsumer_ReadErrorHandling(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_PATTERN", "*")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "1s")

	// Create file with no read permissions
	testFile := filepath.Join(tmpDir, "noperm.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0000); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := consumer.Start(ctx); err != nil && err != context.Canceled {
			t.Errorf("FileConsumer.Start() error = %v", err)
		}
	}()

	// Wait for file processing attempt (should not crash)
	time.Sleep(2 * time.Second)

	// Wait (up to 2s) for at least one file processing attempt, ensuring the consumer stays running.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(2 * time.Second)
waitLoop:
	for {
		select {
		case <-ticker.C:
			if consumer.closed {
				t.Fatalf("File consumer closed unexpectedly while handling read error")
			}
		case <-timeout:
			break waitLoop
		case <-ctx.Done():
			// Context timed out; stop waiting and let the test assertions run.
			break waitLoop
		}
	}
	consumer.Close()

	// Cleanup - restore permissions
	os.Chmod(testFile, 0644)
}

func TestFileConsumer_Stop(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_PATTERN", "*")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "1s")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Close should work
	err = consumer.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Should be marked as closed
	if !consumer.closed {
		t.Error("Consumer not marked as closed after Close()")
	}

	// Calling Close() again should be safe (no panic)
	err = consumer.Close()
	if err != nil {
		t.Errorf("Second Close() error = %v", err)
	}
}

func TestDetectContentType(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	cases := []struct {
		filename string
		expected string
	}{
		{"file.json", "application/json"},
		{"file.txt", "text/plain"},
		{"file.csv", "text/csv"},
		{"file.xml", "application/xml"},
		{"file.yaml", "application/yaml"},
		{"file.yml", "application/yaml"},
		{"file.unknown", "application/octet-stream"},
		{"noextension", "application/octet-stream"},
	}
	for _, tt := range cases {
		t.Run(tt.filename, func(t *testing.T) {
			got := consumer.detectContentType(tt.filename)
			if got != tt.expected {
				t.Errorf("detectContentType(%s) = %s, want %s", tt.filename, got, tt.expected)
		}
	})
	}
}

// Test 9: moveToArchive - file appears in archive/{YYYY-MM-DD}/
func TestFileConsumer_MoveToArchive(t *testing.T) {
	tmpDir := t.TempDir()
	archiveDir := t.TempDir()

	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_PATTERN", "*")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "1s")
	t.Setenv("FILE_INPUT_ARCHIVE_DIR", archiveDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	// Create test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Move to archive
	err = consumer.moveToArchive(testFile)
	if err != nil {
		t.Fatalf("moveToArchive() error = %v", err)
	}

	// Verify file no longer exists at original location
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("File still exists at original location after moving to archive")
	}

	// Verify file exists in archive with date subdirectory
	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(archiveDir, today, "test.txt")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("File not found in archive at %s", expectedPath)
	}
}

// Test 10: Archive directory creation - subdirs auto-created
func TestFileConsumer_ArchiveSubdirectoryCreation(t *testing.T) {
	tmpDir := t.TempDir()
	archiveDir := filepath.Join(t.TempDir(), "archive") // Non-existent parent

	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_ARCHIVE_DIR", archiveDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	// Create test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Move to archive (should create parent directories)
	err = consumer.moveToArchive(testFile)
	if err != nil {
		t.Fatalf("moveToArchive() error = %v", err)
	}

	// Verify archive directory was created
	if _, err := os.Stat(archiveDir); os.IsNotExist(err) {
		t.Errorf("Archive directory not created at %s", archiveDir)
	}
}

// Test 11: moveToError - creates .error metadata file
func TestFileConsumer_MoveToError(t *testing.T) {
	tmpDir := t.TempDir()
	errorDir := t.TempDir()

	t.Setenv("FILE_INPUT_DIR", tmpDir)
	t.Setenv("FILE_INPUT_ERROR_DIR", errorDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	// Create test file
	testFile := filepath.Join(tmpDir, "bad_file.txt")
	if err := os.WriteFile(testFile, []byte("bad content"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Move to error
	errMsg := "permission denied"
	err = consumer.moveToError(testFile, errMsg)
	if err != nil {
		t.Fatalf("moveToError() error = %v", err)
	}

	// Verify file moved
	today := time.Now().Format("2006-01-02")
	movedFile := filepath.Join(errorDir, today, "bad_file.txt")
	if _, err := os.Stat(movedFile); os.IsNotExist(err) {
		t.Errorf("File not found in error directory at %s", movedFile)
	}

	// Verify .error metadata file was created
	metadataFile := movedFile + ".error"
	if _, err := os.Stat(metadataFile); os.IsNotExist(err) {
		t.Errorf("Metadata file not found at %s", metadataFile)
	}

	// Verify metadata contains error message
	content, err := os.ReadFile(metadataFile)
	if err != nil {
		t.Fatalf("Failed to read metadata file: %v", err)
	}
	if !contains(string(content), errMsg) {
		t.Errorf("Metadata file doesn't contain error message: %s", string(content))
	}
}

// Test 12: isFileProcessed - prevents reprocessing
func TestFileConsumer_ReprocessingPrevention(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("test content")
	if err := os.WriteFile(testFile, content, 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Calculate hash
	hash, err := consumer.calculateFileHash(testFile)
	if err != nil {
		t.Fatalf("calculateFileHash() error = %v", err)
	}

	// Get mtime
	info, _ := os.Stat(testFile)
	mtime := info.ModTime().Unix()

	// Record as processed
	consumer.recordProcessedFile(testFile, hash, mtime)

	// Check if processed
	isProcessed, err := consumer.isFileProcessed(testFile)
	if err != nil {
		t.Fatalf("isFileProcessed() error = %v", err)
	}
	if !isProcessed {
		t.Error("File should be marked as processed")
	}
}

// Test 13: isFileLocked - skips locked files
func TestFileConsumer_FileLocking(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// File should not be locked
	if consumer.isFileLocked(testFile) {
		t.Error("Newly created file should not be locked")
	}

	// Open file for writing (simulates being locked)
	file, err := os.OpenFile(testFile, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	defer file.Close()

	// Write to file to update modification time
	if _, err := file.WriteString("more content"); err != nil {
		t.Fatalf("Failed to write to file: %v", err)
	}

	// File should be considered locked (recently modified)
	if !consumer.isFileLocked(testFile) {
		t.Error("Recently modified file should be considered locked")
	}
}

// Test 14: Retry logic - exponential backoff
func TestFileConsumer_RetryLogic(t *testing.T) {
	t.Setenv("FILE_INPUT_MAX_RETRIES", "3")
	t.Setenv("FILE_INPUT_RETRY_BACKOFF_MS", "100")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("NewFileConsumer() error = %v", err)
	}

	testFile := "test_file.txt"

	// First failure
	consumer.recordFailedFile(testFile, "error 1")
	if consumer.shouldRetry(testFile) {
		t.Error("Should not retry immediately after first failure")
	}

	// Wait for backoff
	time.Sleep(150 * time.Millisecond)
	if !consumer.shouldRetry(testFile) {
		t.Error("Should retry after backoff period")
	}

	// Simulate more failures until max retries
	for i := 2; i <= consumer.maxRetries; i++ {
		consumer.recordFailedFile(testFile, fmt.Sprintf("error %d", i))
		if i < consumer.maxRetries {
			time.Sleep(time.Duration(100*(1<<uint(i-1))) * time.Millisecond)
			if !consumer.shouldRetry(testFile) {
				t.Errorf("Should retry after attempt %d", i)
			}
		}
	}

	// After max retries, should not retry
	if consumer.shouldRetry(testFile) {
		t.Error("Should not retry after max retries exceeded")
	}
}

// helper function for test assertions
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && s[0:len(substr)] == substr || s[len(s)-len(substr):] == substr)
}
