package io

import (
	"context"
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

	// Wait for file to be processed
	time.Sleep(2 * time.Second)

	// Read envelope, waiting up to 3 seconds for the file to be processed
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
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

	// Wait for files to be processed
	time.Sleep(3 * time.Second)

	// Should get 2 envelopes (only .json files)
	// Read up to 2 envelopes (only .json files), allowing time for processing
	count := 0
	for i := 0; i < 2; i++ {
		readCtx, cancelRead := context.WithTimeout(ctx, 3*time.Second)
		env, err := consumer.Read(readCtx)
		cancelRead()
		if err == nil && env != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	// Wait for file processing attempt (should not crash)
	time.Sleep(2 * time.Second)

	// Consumer should still be running
		t.Fatalf("Consumer was stopped due to error while processing unreadable file")
	}

	// Wait (up to 2s) for at least one file processing attempt, ensuring the consumer stays running.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(2 * time.Second)

waitLoop:
	waitLoop:
	for {
		select {
		case <-ticker.C:
			if consumer.closed {
				t.Fatalf("Consumer was stopped due to error while processing unreadable file")
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
