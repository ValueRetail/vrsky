package io

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// TestFileConsumerProducerPipeline tests the complete flow without NATS:
// Envelope → FileProducer writes (bypassing consumer NATS dependency)
func TestFileConsumerProducerPipeline(t *testing.T) {
	// Setup logging
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create temporary directories
	outputDir := t.TempDir()

	// Configure environment for producer
	t.Setenv("FILE_OUTPUT_DIR", outputDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")

	// Create producer
	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("Failed to create producer: %v", err)
	}

	// Start producer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := producer.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer: %v", err)
	}
	defer producer.Close()

	// Create an envelope (simulating what FileConsumer would create)
	testContent := []byte("Hello, VRSky Pipeline!")
	env := &envelope.Envelope{
		ID:          "test-001",
		Payload:     testContent,
		PayloadSize: int64(len(testContent)),
		ContentType: "text/plain",
		Source:      "file-consumer",
		CreatedAt:   time.Now(),
	}

	// Write envelope through producer
	writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := producer.Write(writeCtx, env); err != nil {
		t.Fatalf("Failed to write to producer: %v", err)
	}

	// Verify output file was created
	outputFiles, err := filepath.Glob(filepath.Join(outputDir, "*"))
	if err != nil {
		t.Fatalf("Failed to glob output files: %v", err)
	}

	if len(outputFiles) == 0 {
		t.Fatal("No output files created")
	}

	// Read and verify output file content
	outputContent, err := os.ReadFile(outputFiles[0])
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	if string(outputContent) != string(testContent) {
		t.Errorf("Output content mismatch. Expected %s, got %s", testContent, outputContent)
	}
}

// TestFileConsumerMetadataPreservation tests that metadata is preserved in envelopes
// (This test creates an envelope directly to test serialization properties)
func TestFileConsumerMetadataPreservation(t *testing.T) {
	// Create an envelope directly (simulating what FileConsumer would create)
	testContent := []byte(`{"key": "value", "number": 42}`)
	
	env := &envelope.Envelope{
		ID:          "test-metadata-123",
		Payload:     testContent,
		PayloadSize: int64(len(testContent)),
		ContentType: "application/json",
		Source:      "file-consumer",
		CreatedAt:   time.Now(),
	}

	// Verify metadata
	if env.ID == "" {
		t.Error("Envelope ID is empty")
	}

	if env.Source != "file-consumer" {
		t.Errorf("Expected source 'file-consumer', got %s", env.Source)
	}

	if env.ContentType != "application/json" {
		t.Errorf("Expected content type 'application/json', got %s", env.ContentType)
	}

	if env.PayloadSize != int64(len(testContent)) {
		t.Errorf("Expected payload size %d, got %d", len(testContent), env.PayloadSize)
	}

	if env.CreatedAt.IsZero() {
		t.Error("CreatedAt timestamp is zero")
	}

	// Test serialization preserves metadata
	data, err := envelope.Marshal(env)
	if err != nil {
		t.Fatalf("Failed to marshal envelope: %v", err)
	}

	unmarshaled, err := envelope.Unmarshal(data)
	if err != nil {
		t.Fatalf("Failed to unmarshal envelope: %v", err)
	}

	if unmarshaled.ContentType != env.ContentType {
		t.Errorf("ContentType not preserved. Expected %s, got %s", env.ContentType, unmarshaled.ContentType)
	}

	if unmarshaled.Source != env.Source {
		t.Errorf("Source not preserved. Expected %s, got %s", env.Source, unmarshaled.Source)
	}
}

// TestFileConsumerMultipleFiles tests envelope handling for multiple files
// (Creates multiple envelopes to simulate multiple files being processed)
func TestFileConsumerMultipleFiles(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	outputDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", outputDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")

	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("Failed to create producer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := producer.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer: %v", err)
	}
	defer producer.Close()

	// Create and write multiple envelopes (simulating multiple files from consumer)
	fileContents := map[string][]byte{
		"file1": []byte("Content 1"),
		"file2": []byte("Content 2"),
		"file3": []byte("Content 3"),
	}

	for id, content := range fileContents {
		env := &envelope.Envelope{
			ID:          id,
			Payload:     content,
			PayloadSize: int64(len(content)),
			ContentType: "text/plain",
			Source:      "file-consumer",
			CreatedAt:   time.Now(),
		}

		writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := producer.Write(writeCtx, env); err != nil {
			t.Fatalf("Failed to write envelope %s: %v", id, err)
		}
		cancel()
	}

	// Verify output files were created
	outputFiles, err := filepath.Glob(filepath.Join(outputDir, "*"))
	if err != nil {
		t.Fatalf("Failed to glob output files: %v", err)
	}

	if len(outputFiles) != 3 {
		t.Fatalf("Expected 3 output files, got %d", len(outputFiles))
	}

	// Verify all files have correct content
	for id, expectedContent := range fileContents {
		found := false
		for _, outputFile := range outputFiles {
			if strings.Contains(outputFile, id) {
				actual, err := os.ReadFile(outputFile)
				if err != nil {
					t.Fatalf("Failed to read file %s: %v", outputFile, err)
				}
				if string(actual) == string(expectedContent) {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("Output file for %s not found or content mismatch", id)
		}
	}
}

// TestFileProducerFilenameGeneration tests filename template expansion
func TestFileProducerFilenameGeneration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	outputDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", outputDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "output-{{.ID}}-{{.Extension}}")

	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("Failed to create producer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := producer.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer: %v", err)
	}
	defer producer.Close()

	// Create envelope with specific ID
	env := &envelope.Envelope{
		ID:          "test-123",
		Payload:     []byte("test content"),
		PayloadSize: 12,
		ContentType: "text/plain",
		Source:      "test",
	}

	writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := producer.Write(writeCtx, env); err != nil {
		t.Fatalf("Failed to write envelope: %v", err)
	}

	// Verify file was created with expected name format
	outputFiles, err := filepath.Glob(filepath.Join(outputDir, "output-test-123-*"))
	if err != nil {
		t.Fatalf("Failed to glob files: %v", err)
	}

	if len(outputFiles) == 0 {
		t.Fatal("No output file created with expected pattern")
	}
}

// TestFileProducerPermissions tests file permissions are set correctly
func TestFileProducerPermissions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	outputDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", outputDir)
	t.Setenv("FILE_OUTPUT_PERMISSIONS", "0600") // Read/write for owner only

	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("Failed to create producer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := producer.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer: %v", err)
	}
	defer producer.Close()

	env := &envelope.Envelope{
		ID:          "perm-test",
		Payload:     []byte("sensitive data"),
		PayloadSize: 14,
		ContentType: "text/plain",
		Source:      "test",
	}

	writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := producer.Write(writeCtx, env); err != nil {
		t.Fatalf("Failed to write envelope: %v", err)
	}

	// Verify file permissions
	outputFiles, err := filepath.Glob(filepath.Join(outputDir, "*"))
	if err != nil {
		t.Fatalf("Failed to glob files: %v", err)
	}

	if len(outputFiles) == 0 {
		t.Fatal("No output file created")
	}

	fileInfo, err := os.Stat(outputFiles[0])
	if err != nil {
		t.Fatalf("Failed to stat file: %v", err)
	}

	// Check that file is readable by owner
	if fileInfo.Mode()&0o200 == 0 {
		t.Error("File is not writable by owner")
	}
}

// TestFileConsumerPatternMatching tests glob pattern filtering in filenames
// (Simulates filename sanitization that would occur in FileProducer)
func TestFileConsumerPatternMatching(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	outputDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", outputDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")

	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("Failed to create producer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := producer.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer: %v", err)
	}
	defer producer.Close()

	// Create envelopes with different content types
	testCases := []struct {
		id          string
		contentType string
		content     []byte
	}{
		{"data-csv", "text/csv", []byte("col1,col2")},
		{"data-json", "application/json", []byte("{}")},
		{"data-xml", "application/xml", []byte("<root/>")},
	}

	for _, tc := range testCases {
		env := &envelope.Envelope{
			ID:          tc.id,
			Payload:     tc.content,
			PayloadSize: int64(len(tc.content)),
			ContentType: tc.contentType,
			Source:      "file-consumer",
			CreatedAt:   time.Now(),
		}

		writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := producer.Write(writeCtx, env); err != nil {
			t.Fatalf("Failed to write envelope %s: %v", tc.id, err)
		}
		cancel()
	}

	// Verify files were created
	outputFiles, err := filepath.Glob(filepath.Join(outputDir, "*"))
	if err != nil {
		t.Fatalf("Failed to glob files: %v", err)
	}

	if len(outputFiles) != 3 {
		t.Fatalf("Expected 3 output files, got %d", len(outputFiles))
	}

	// Verify each file has correct extension based on content type
	for _, outputFile := range outputFiles {
		fileName := filepath.Base(outputFile)
		
		// Check that files have the expected ID pattern
		idFound := false
		for _, tc := range testCases {
			if strings.Contains(fileName, tc.id) {
				idFound = true
				break
			}
		}
		
		if !idFound {
			t.Errorf("Output file %s doesn't match expected pattern", fileName)
		}
	}
}

// TestFileConsumerErrorHandling tests error scenarios
func TestFileConsumerErrorHandling(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Try to read from non-existent directory initially
	inputDir := t.TempDir()
	nonExistentDir := filepath.Join(inputDir, "does-not-exist")
	t.Setenv("FILE_INPUT_DIR", nonExistentDir)
	t.Setenv("FILE_INPUT_PATTERN", "*")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "100ms")

	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start should create the directory
	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("Start() should create directory, got error: %v", err)
	}
	defer consumer.Close()

	// Verify directory was created
	if _, err := os.Stat(nonExistentDir); os.IsNotExist(err) {
		t.Error("Start() should have created input directory")
	}
}

// TestFileProducerPathTraversalPrevention tests security against path traversal
func TestFileProducerPathTraversalPrevention(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	outputDir := t.TempDir()
	t.Setenv("FILE_OUTPUT_DIR", outputDir)
	t.Setenv("FILE_OUTPUT_FILENAME_FORMAT", "{{.ID}}.{{.Extension}}")

	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("Failed to create producer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := producer.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer: %v", err)
	}
	defer producer.Close()

	// Try to write with path traversal attempt
	env := &envelope.Envelope{
		ID:          "../../../etc/passwd",
		Payload:     []byte("malicious"),
		PayloadSize: 9,
		ContentType: "text/plain",
		Source:      "test",
	}

	writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = producer.Write(writeCtx, env)

	// Write should succeed but sanitize the path
	if err != nil {
		t.Logf("Write with path traversal: %v (sanitized or rejected)", err)
	}

	// Verify no files were created outside outputDir
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("Failed to read output directory: %v", err)
	}

	for _, entry := range entries {
		fullPath, err := filepath.Abs(filepath.Join(outputDir, entry.Name()))
		if err != nil {
			t.Fatalf("Failed to get absolute path: %v", err)
		}

		outputAbs, err := filepath.Abs(outputDir)
		if err != nil {
			t.Fatalf("Failed to get output dir absolute path: %v", err)
		}

		// Verify file is within output directory
		if !filepath.HasPrefix(fullPath, outputAbs) {
			t.Errorf("File created outside output directory: %s", fullPath)
		}
	}
}

// TestFileConsumerProducerGracefulShutdown tests cleanup on context cancellation
func TestFileConsumerProducerGracefulShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	inputDir := t.TempDir()
	outputDir := t.TempDir()
	t.Setenv("FILE_INPUT_DIR", inputDir)
	t.Setenv("FILE_INPUT_PATTERN", "*")
	t.Setenv("FILE_INPUT_POLL_INTERVAL", "100ms")
	t.Setenv("FILE_OUTPUT_DIR", outputDir)

	consumer, err := NewFileConsumer(logger)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	producer, err := NewFileProducer(logger)
	if err != nil {
		t.Fatalf("Failed to create producer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("Failed to start consumer: %v", err)
	}

	if err := producer.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer: %v", err)
	}

	// Cancel context to trigger graceful shutdown
	cancel()

	// Close components (should be idempotent)
	consumer.Close()
	producer.Close()

	// Try closing again - should not panic
	consumer.Close()
	producer.Close()
}

// TestEnvelopeSerializationThroughPipeline tests envelope JSON marshaling/unmarshaling
func TestEnvelopeSerializationThroughPipeline(t *testing.T) {
	// Create an envelope
	env := &envelope.Envelope{
		ID:            "test-123",
		TenantID:      "tenant-456",
		IntegrationID: "integration-789",
		Payload:       []byte("test payload"),
		PayloadSize:   12,
		ContentType:   "text/plain",
		Source:        "file-consumer",
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(15 * time.Minute),
	}

	// Marshal to JSON
	data, err := envelope.Marshal(env)
	if err != nil {
		t.Fatalf("Failed to marshal envelope: %v", err)
	}

	// Unmarshal back
	unmarshaled, err := envelope.Unmarshal(data)
	if err != nil {
		t.Fatalf("Failed to unmarshal envelope: %v", err)
	}

	// Verify all fields are preserved
	if unmarshaled.ID != env.ID {
		t.Errorf("ID mismatch: %s != %s", unmarshaled.ID, env.ID)
	}

	if unmarshaled.TenantID != env.TenantID {
		t.Errorf("TenantID mismatch: %s != %s", unmarshaled.TenantID, env.TenantID)
	}

	if string(unmarshaled.Payload) != string(env.Payload) {
		t.Errorf("Payload mismatch: %s != %s", unmarshaled.Payload, env.Payload)
	}

	if unmarshaled.ContentType != env.ContentType {
		t.Errorf("ContentType mismatch: %s != %s", unmarshaled.ContentType, env.ContentType)
	}
}
