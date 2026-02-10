package io

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// TestNewPostgresInput_Configuration tests environment variable parsing
func TestNewPostgresInput_Configuration(t *testing.T) {
	tests := []struct {
		name        string
		envVars     map[string]string
		wantErr     bool
		wantHost    string
		wantPort    int
		wantUser    string
		wantDatabase string
	}{
		{
			name: "default configuration",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
			},
			wantErr:       false,
			wantHost:      "localhost",
			wantPort:      5432,
			wantUser:      "postgres",
			wantDatabase:  "source_db",
		},
		{
			name: "custom host and port",
			envVars: map[string]string{
				"POSTGRES_INPUT_HOST":     "db.example.com",
				"POSTGRES_INPUT_PORT":     "5433",
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
			},
			wantErr:      false,
			wantHost:     "db.example.com",
			wantPort:     5433,
			wantUser:     "postgres",
			wantDatabase: "source_db",
		},
		{
			name: "missing required password",
			envVars: map[string]string{
				"POSTGRES_INPUT_DATABASE": "source_db",
			},
			wantErr: true,
		},
		{
			name: "missing required database",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
			},
			wantErr: true,
		},
		{
			name: "invalid port format",
			envVars: map[string]string{
				"POSTGRES_INPUT_PORT":     "invalid",
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear environment
			for key := range tt.envVars {
				os.Unsetenv(key)
			}

			// Set test environment variables
			for key, val := range tt.envVars {
				os.Setenv(key, val)
				defer os.Unsetenv(key)
			}

			pi, err := NewPostgresInput(slog.Default())

			if (err != nil) != tt.wantErr {
				t.Errorf("NewPostgresInput() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil {
				if pi.host != tt.wantHost {
					t.Errorf("host = %s, want %s", pi.host, tt.wantHost)
				}
				if pi.port != tt.wantPort {
					t.Errorf("port = %d, want %d", pi.port, tt.wantPort)
				}
				if pi.user != tt.wantUser {
					t.Errorf("user = %s, want %s", pi.user, tt.wantUser)
				}
				if pi.database != tt.wantDatabase {
					t.Errorf("database = %s, want %s", pi.database, tt.wantDatabase)
				}
			}
		})
	}
}

// TestNewPostgresInput_BatchConfiguration tests batch size and timeout settings
func TestNewPostgresInput_BatchConfiguration(t *testing.T) {
	tests := []struct {
		name           string
		envVars        map[string]string
		wantBatchSize  int
		wantBatchTime  time.Duration
	}{
		{
			name: "default batch settings",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
			},
			wantBatchSize: 100,
			wantBatchTime: 5 * time.Second,
		},
		{
			name: "custom batch size",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_BATCH_SIZE": "50",
			},
			wantBatchSize: 50,
		},
		{
			name: "invalid batch size ignored",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_BATCH_SIZE": "invalid",
			},
			wantBatchSize: 100, // Falls back to default
		},
		{
			name: "zero batch size ignored",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_BATCH_SIZE": "0",
			},
			wantBatchSize: 100, // Falls back to default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear and set environment
			for key := range tt.envVars {
				os.Unsetenv(key)
			}
			for key, val := range tt.envVars {
				os.Setenv(key, val)
				defer os.Unsetenv(key)
			}

			pi, err := NewPostgresInput(slog.Default())
			if err != nil {
				t.Fatalf("NewPostgresInput() error = %v", err)
			}

			if pi.batchSize != tt.wantBatchSize {
				t.Errorf("batchSize = %d, want %d", pi.batchSize, tt.wantBatchSize)
			}
			if tt.wantBatchTime > 0 && pi.batchTimeout != tt.wantBatchTime {
				t.Errorf("batchTimeout = %v, want %v", pi.batchTimeout, tt.wantBatchTime)
			}
		})
	}
}

// TestNewPostgresInput_TableFilters tests table whitelist filtering
func TestNewPostgresInput_TableFilters(t *testing.T) {
	tests := []struct {
		name        string
		envVars     map[string]string
		wantFilters map[string]bool
	}{
		{
			name: "no table filters",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
			},
			wantFilters: map[string]bool{},
		},
		{
			name: "single table filter",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_TABLES": "users",
			},
			wantFilters: map[string]bool{"users": true},
		},
		{
			name: "multiple table filters",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_TABLES": "users,orders,products",
			},
			wantFilters: map[string]bool{
				"users":    true,
				"orders":   true,
				"products": true,
			},
		},
		{
			name: "table filters with whitespace",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_TABLES": " users , orders , products ",
			},
			wantFilters: map[string]bool{
				"users":    true,
				"orders":   true,
				"products": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear and set environment
			for key := range tt.envVars {
				os.Unsetenv(key)
			}
			for key, val := range tt.envVars {
				os.Setenv(key, val)
				defer os.Unsetenv(key)
			}

			pi, err := NewPostgresInput(slog.Default())
			if err != nil {
				t.Fatalf("NewPostgresInput() error = %v", err)
			}

			if len(pi.tableFilters) != len(tt.wantFilters) {
				t.Errorf("tableFilters length = %d, want %d", len(pi.tableFilters), len(tt.wantFilters))
			}

			for table := range tt.wantFilters {
				if !pi.tableFilters[table] {
					t.Errorf("table %q not found in filters", table)
				}
			}
		})
	}
}

// TestNewPostgresInput_NATSConfiguration tests NATS settings
func TestNewPostgresInput_NATSConfiguration(t *testing.T) {
	tests := []struct {
		name           string
		envVars        map[string]string
		wantNATSURL    string
		wantNATSSubject string
	}{
		{
			name: "default NATS configuration",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
			},
			wantNATSURL:     "nats://localhost:4222",
			wantNATSSubject: "postgres.changes",
		},
		{
			name: "custom NATS URL",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"NATS_URL": "nats://nats.example.com:4222",
			},
			wantNATSURL:     "nats://nats.example.com:4222",
			wantNATSSubject: "postgres.changes",
		},
		{
			name: "custom NATS subject",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_SUBJECT": "custom.cdc.topic",
			},
			wantNATSURL:     "nats://localhost:4222",
			wantNATSSubject: "custom.cdc.topic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear and set environment
			for key := range tt.envVars {
				os.Unsetenv(key)
			}
			for key, val := range tt.envVars {
				os.Setenv(key, val)
				defer os.Unsetenv(key)
			}

			pi, err := NewPostgresInput(slog.Default())
			if err != nil {
				t.Fatalf("NewPostgresInput() error = %v", err)
			}

			if pi.natsURL != tt.wantNATSURL {
				t.Errorf("natsURL = %s, want %s", pi.natsURL, tt.wantNATSURL)
			}
			if pi.natsSubject != tt.wantNATSSubject {
				t.Errorf("natsSubject = %s, want %s", pi.natsSubject, tt.wantNATSSubject)
			}
		})
	}
}

// TestNewPostgresInput_ReplicationSlotConfiguration tests replication slot naming
func TestNewPostgresInput_ReplicationSlotConfiguration(t *testing.T) {
	tests := []struct {
		name             string
		envVars          map[string]string
		wantSlotName     string
		wantPublication  string
	}{
		{
			name: "default replication settings",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
			},
			wantSlotName:    "vrsky_slot",
			wantPublication: "vrsky_publication",
		},
		{
			name: "custom replication slot",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_REPLICATION_SLOT": "custom_slot",
			},
			wantSlotName:    "custom_slot",
			wantPublication: "vrsky_publication",
		},
		{
			name: "custom publication",
			envVars: map[string]string{
				"POSTGRES_INPUT_PASSWORD": "password",
				"POSTGRES_INPUT_DATABASE": "source_db",
				"POSTGRES_INPUT_PUBLICATION": "custom_pub",
			},
			wantSlotName:    "vrsky_slot",
			wantPublication: "custom_pub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear and set environment
			for key := range tt.envVars {
				os.Unsetenv(key)
			}
			for key, val := range tt.envVars {
				os.Setenv(key, val)
				defer os.Unsetenv(key)
			}

			pi, err := NewPostgresInput(slog.Default())
			if err != nil {
				t.Fatalf("NewPostgresInput() error = %v", err)
			}

			if pi.replicationSlot != tt.wantSlotName {
				t.Errorf("replicationSlot = %s, want %s", pi.replicationSlot, tt.wantSlotName)
			}
			if pi.publication != tt.wantPublication {
				t.Errorf("publication = %s, want %s", pi.publication, tt.wantPublication)
			}
		})
	}
}

// TestCreateEnvelopeFromWAL_ValidChange tests envelope creation from WAL data
func TestCreateEnvelopeFromWAL_ValidChange(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	change := CDCChange{
		Operation:     "INSERT",
		Schema:        "public",
		Table:         "users",
		After:         map[string]interface{}{"id": 1, "name": "John"},
		Timestamp:     time.Now(),
		TransactionID: 100,
		LSN:           0x0000000001,
	}

	data, _ := json.Marshal(change)
	env := pi.createEnvelopeFromWAL(data)

	if env == nil {
		t.Fatal("createEnvelopeFromWAL() returned nil")
	}

	if env.ContentType != "application/cdc+json" {
		t.Errorf("ContentType = %s, want application/cdc+json", env.ContentType)
	}

	if env.Source != "PostgresInput" {
		t.Errorf("Source = %s, want PostgresInput", env.Source)
	}

	// Check metadata
	if metadata, ok := env.Metadata["operation"]; !ok || metadata != "INSERT" {
		t.Errorf("metadata operation not correct: %v", metadata)
	}

	if metadata, ok := env.Metadata["table"]; !ok || metadata != "users" {
		t.Errorf("metadata table not correct: %v", metadata)
	}
}

// TestCreateEnvelopeFromWAL_InvalidJSON tests handling of invalid JSON data
func TestCreateEnvelopeFromWAL_InvalidJSON(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	env := pi.createEnvelopeFromWAL([]byte("invalid json"))
	if env != nil {
		t.Fatal("createEnvelopeFromWAL() should return nil for invalid JSON")
	}
}

// TestCreateEnvelopeFromWAL_TableFiltering tests that filtered tables are ignored
func TestCreateEnvelopeFromWAL_TableFiltering(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	os.Setenv("POSTGRES_INPUT_TABLES", "users")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")
	defer os.Unsetenv("POSTGRES_INPUT_TABLES")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	// Change from allowed table
	allowedChange := CDCChange{
		Operation:     "INSERT",
		Schema:        "public",
		Table:         "users",
		After:         map[string]interface{}{"id": 1},
		Timestamp:     time.Now(),
		TransactionID: 100,
		LSN:           1,
	}

	data, _ := json.Marshal(allowedChange)
	env := pi.createEnvelopeFromWAL(data)
	if env == nil {
		t.Fatal("createEnvelopeFromWAL() should return envelope for allowed table")
	}

	// Change from disallowed table
	disallowedChange := CDCChange{
		Operation:     "INSERT",
		Schema:        "public",
		Table:         "orders",
		After:         map[string]interface{}{"id": 1},
		Timestamp:     time.Now(),
		TransactionID: 101,
		LSN:           2,
	}

	data, _ = json.Marshal(disallowedChange)
	env = pi.createEnvelopeFromWAL(data)
	if env != nil {
		t.Fatal("createEnvelopeFromWAL() should return nil for disallowed table")
	}
}

// TestCreateEnvelopeFromWAL_AllOperationTypes tests envelope creation for different operations
func TestCreateEnvelopeFromWAL_AllOperationTypes(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	operations := []string{"INSERT", "UPDATE", "DELETE"}

	for _, op := range operations {
		t.Run(op, func(t *testing.T) {
			change := CDCChange{
				Operation:     op,
				Schema:        "public",
				Table:         "users",
				Before:        map[string]interface{}{"id": 1},
				After:         map[string]interface{}{"id": 1, "name": "John"},
				Timestamp:     time.Now(),
				TransactionID: 100,
				LSN:           1,
			}

			data, _ := json.Marshal(change)
			env := pi.createEnvelopeFromWAL(data)

			if env == nil {
				t.Fatalf("createEnvelopeFromWAL() returned nil for operation %s", op)
			}

			if operation, ok := env.Metadata["operation"]; !ok || operation != op {
				t.Errorf("operation = %v, want %s", operation, op)
			}
		})
	}
}

// TestCreateEnvelopeFromWAL_LSNTracking tests LSN is updated
func TestCreateEnvelopeFromWAL_LSNTracking(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	if pi.lsn != 0 {
		t.Errorf("initial LSN = %d, want 0", pi.lsn)
	}

	change := CDCChange{
		Operation:     "INSERT",
		Schema:        "public",
		Table:         "users",
		After:         map[string]interface{}{"id": 1},
		Timestamp:     time.Now(),
		TransactionID: 100,
		LSN:           0x0000000042,
	}

	data, _ := json.Marshal(change)
	_ = pi.createEnvelopeFromWAL(data)

	if pi.lsn != 0x0000000042 {
		t.Errorf("LSN after change = %d, want %d", pi.lsn, 0x0000000042)
	}
}

// TestAddToPendingBatch_BatchSize tests batch flushing when size threshold is reached
func TestPostgresInput_AddToPendingBatch_BatchSize(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	os.Setenv("POSTGRES_INPUT_BATCH_SIZE", "3")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")
	defer os.Unsetenv("POSTGRES_INPUT_BATCH_SIZE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	// Create test envelopes
	env1 := envelope.New()
	env2 := envelope.New()
	env3 := envelope.New()

	// Add first two envelopes - should not flush
	pi.addToPendingBatch(env1)
	if len(pi.pendingBatch) != 1 {
		t.Errorf("pending batch size = %d, want 1", len(pi.pendingBatch))
	}

	pi.addToPendingBatch(env2)
	if len(pi.pendingBatch) != 2 {
		t.Errorf("pending batch size = %d, want 2", len(pi.pendingBatch))
	}

	// Add third envelope - should trigger flush
	pi.addToPendingBatch(env3)

	// After flush, pending batch should be empty
	if len(pi.pendingBatch) != 0 {
		t.Errorf("pending batch size after flush = %d, want 0", len(pi.pendingBatch))
	}
}

// TestAddToPendingBatch_BatchTimeout tests batch timer creation
func TestPostgresInput_AddToPendingBatch_BatchTimeout(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	env := envelope.New()

	// Add first envelope - should start timer
	pi.addToPendingBatch(env)

	if pi.batchTimer == nil {
		t.Fatal("batchTimer should be created after first message")
	}

	// Clean up
	pi.batchTimer.Stop()
}

// TestFlushBatch_EmptyBatch tests that flush on empty batch is no-op
func TestFlushBatch_EmptyBatch(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	pi.ctx, pi.cancel = context.WithCancel(context.Background())

	// Flush empty batch should be safe
	pi.flushBatch()

	if len(pi.pendingBatch) != 0 {
		t.Errorf("pending batch = %v, want empty", pi.pendingBatch)
	}
}

// TestFlushBatch_PublishesEnvelopes tests that flush publishes to messages channel
func TestFlushBatch_PublishesEnvelopes(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	pi.ctx, pi.cancel = context.WithCancel(context.Background())

	env1 := envelope.New()
	env1.ID = "test-1"
	env2 := envelope.New()
	env2.ID = "test-2"

	pi.pendingBatch = []*envelope.Envelope{env1, env2}

	// Start reading messages in background
	var received []*envelope.Envelope
	go func() {
		for i := 0; i < 2; i++ {
			env, ok := <-pi.messages
			if ok {
				received = append(received, env)
			}
		}
	}()

	// Allow time for goroutine to start
	time.Sleep(10 * time.Millisecond)

	// Flush should send to channel
	pi.flushBatch()

	// Wait for messages to be received
	time.Sleep(50 * time.Millisecond)

	if len(received) != 2 {
		t.Errorf("received %d messages, want 2", len(received))
	}
}

// TestClose_GracefulShutdown tests graceful shutdown
func TestClose_GracefulShutdown(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	// Add envelopes to pending batch
	env := envelope.New()
	env.ID = "test-1"
	pi.pendingBatch = []*envelope.Envelope{env}

	// Mock context
	pi.ctx, pi.cancel = context.WithCancel(context.Background())

	// Start reading messages
	var received []*envelope.Envelope
	go func() {
		for env := range pi.messages {
			received = append(received, env)
		}
	}()

	// Close
	err = pi.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Verify closed flag
	if !pi.closed {
		t.Error("closed flag should be true")
	}

	// Give goroutine time to read pending messages
	time.Sleep(50 * time.Millisecond)

	// Pending batch should be flushed (may or may not receive due to channel state)
	if len(pi.pendingBatch) != 0 {
		t.Errorf("pending batch after close = %v, want empty", pi.pendingBatch)
	}
}

// TestClose_Idempotent tests that Close can be called multiple times
func TestPostgresInput_Close_Idempotent(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	pi.ctx, pi.cancel = context.WithCancel(context.Background())

	// Close multiple times should not panic
	err1 := pi.Close()
	err2 := pi.Close()
	err3 := pi.Close()

	if err1 != nil || err2 != nil || err3 != nil {
		t.Errorf("Close() should not error on multiple calls: %v, %v, %v", err1, err2, err3)
	}
}

// TestRead_ContextCancellation tests that Read respects context cancellation
func TestRead_ContextCancellation(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	pi.ctx, pi.cancel = context.WithCancel(context.Background())

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	env, err := pi.Read(ctx)
	if env != nil {
		t.Errorf("Read() should return nil envelope on context cancellation")
	}
	if err == nil || err != context.Canceled {
		t.Errorf("Read() error = %v, want context.Canceled", err)
	}
}

// TestRead_ConsumerClosed tests that Read returns error when consumer is closed
func TestRead_ConsumerClosed(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	pi.ctx, pi.cancel = context.WithCancel(context.Background())
	pi.Close()

	ctx := context.Background()
	env, err := pi.Read(ctx)

	if env != nil {
		t.Errorf("Read() should return nil envelope when consumer is closed")
	}
	if err == nil {
		t.Error("Read() should return error when consumer is closed")
	}
}

// TestCreateEnvelopeFromWAL_EnvelopeID tests envelope ID generation
func TestCreateEnvelopeFromWAL_EnvelopeID(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	change := CDCChange{
		Operation:     "INSERT",
		Schema:        "public",
		Table:         "users",
		After:         map[string]interface{}{"id": 1},
		Timestamp:     time.Now(),
		TransactionID: 12345,
		LSN:           0x0000000042,
	}

	data, _ := json.Marshal(change)
	env := pi.createEnvelopeFromWAL(data)

	expectedID := fmt.Sprintf("cdc-%d-%d", 12345, uint64(0x0000000042))
	if env.ID != expectedID {
		t.Errorf("envelope ID = %s, want %s", env.ID, expectedID)
	}
}

// TestCreateEnvelopeFromWAL_PayloadSerialization tests payload is correctly serialized
func TestCreateEnvelopeFromWAL_PayloadSerialization(t *testing.T) {
	os.Setenv("POSTGRES_INPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_INPUT_DATABASE", "source_db")
	defer os.Unsetenv("POSTGRES_INPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_INPUT_DATABASE")

	pi, err := NewPostgresInput(slog.Default())
	if err != nil {
		t.Fatalf("NewPostgresInput() error = %v", err)
	}

	change := CDCChange{
		Operation:     "UPDATE",
		Schema:        "public",
		Table:         "users",
		Before:        map[string]interface{}{"id": 1, "name": "John"},
		After:         map[string]interface{}{"id": 1, "name": "Jane"},
		Timestamp:     time.Now(),
		TransactionID: 100,
		LSN:           1,
	}

	data, _ := json.Marshal(change)
	env := pi.createEnvelopeFromWAL(data)

	if env == nil {
		t.Fatal("envelope is nil")
	}

	if len(env.Payload) == 0 {
		t.Error("envelope payload is empty")
	}

	// Verify payload can be unmarshalled
	var payload map[string]interface{}
	err = json.Unmarshal(env.Payload, &payload)
	if err != nil {
		t.Errorf("failed to unmarshal payload: %v", err)
	}

	if payload["operation"] != "UPDATE" {
		t.Errorf("payload operation = %v, want UPDATE", payload["operation"])
	}
}
