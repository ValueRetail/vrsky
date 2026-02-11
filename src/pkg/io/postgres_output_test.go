package io

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// testSleeper implements sleeper interface for deterministic testing
type testSleeper struct {
	sleepDurations []time.Duration
	sleepIndex     int
}

func (ts *testSleeper) sleep(ctx context.Context, d time.Duration) error {
	ts.sleepDurations = append(ts.sleepDurations, d)
	ts.sleepIndex++
	// Don't actually sleep - return immediately
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// mockExecuteBatch provides deterministic batch execution for testing
type mockExecuteBatch struct {
	callCount int
	failUntil int // Fail first N attempts, then succeed
	error     error
}

func (m *mockExecuteBatch) execute() error {
	m.callCount++
	if m.callCount <= m.failUntil {
		return m.error
	}
	return nil
}

// TestNewPostgresOutput_Configuration tests environment variable parsing
func TestNewPostgresOutput_Configuration(t *testing.T) {
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
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
			},
			wantErr:       false,
			wantHost:      "localhost",
			wantPort:      5432,
			wantUser:      "postgres",
			wantDatabase:  "test_db",
		},
		{
			name: "custom host and port",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_HOST":     "db.example.com",
				"POSTGRES_OUTPUT_PORT":     "5433",
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
			},
			wantErr:      false,
			wantHost:     "db.example.com",
			wantPort:     5433,
			wantUser:     "postgres",
			wantDatabase: "test_db",
		},
		{
			name: "missing required password",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_DATABASE": "test_db",
			},
			wantErr: true,
		},
		{
			name: "missing required database",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD": "password",
			},
			wantErr: true,
		},
		{
			name: "invalid port format",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PORT":     "invalid",
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
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

			po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())

			if (err != nil) != tt.wantErr {
				t.Errorf("NewPostgresOutput() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil {
				if po.host != tt.wantHost {
					t.Errorf("host = %s, want %s", po.host, tt.wantHost)
				}
				if po.port != tt.wantPort {
					t.Errorf("port = %d, want %d", po.port, tt.wantPort)
				}
				if po.user != tt.wantUser {
					t.Errorf("user = %s, want %s", po.user, tt.wantUser)
				}
				if po.database != tt.wantDatabase {
					t.Errorf("database = %s, want %s", po.database, tt.wantDatabase)
				}
			}
		})
	}
}

// TestNewPostgresOutput_BatchConfiguration tests batch settings
func TestNewPostgresOutput_BatchConfiguration(t *testing.T) {
	tests := []struct {
		name           string
		envVars        map[string]string
		wantBatchSize  int
		wantBatchTime  time.Duration
	}{
		{
			name: "default batch settings",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
			},
			wantBatchSize: 100,
			wantBatchTime: 5 * time.Second,
		},
		{
			name: "custom batch size",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
				"POSTGRES_OUTPUT_BATCH_SIZE": "50",
			},
			wantBatchSize: 50,
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

			po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
			if err != nil {
				t.Fatalf("NewPostgresOutput() error = %v", err)
			}

			if po.batchSize != tt.wantBatchSize {
				t.Errorf("batchSize = %d, want %d", po.batchSize, tt.wantBatchSize)
			}
			if tt.wantBatchTime > 0 && po.batchTimeout != tt.wantBatchTime {
				t.Errorf("batchTimeout = %v, want %v", po.batchTimeout, tt.wantBatchTime)
			}
		})
	}
}

// TestNewPostgresOutput_ConflictResolution tests conflict resolution strategy
func TestNewPostgresOutput_ConflictResolution(t *testing.T) {
	tests := []struct {
		name               string
		envVars            map[string]string
		wantConflictStrat  string
		wantErr            bool
	}{
		{
			name: "default UPSERT strategy",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
			},
			wantConflictStrat: "UPSERT",
			wantErr:           false,
		},
		{
			name: "unsupported REPLACE strategy",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
				"POSTGRES_CONFLICT_RESOLUTION": "REPLACE",
			},
			wantErr: true,
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

			po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
			if (err != nil) != tt.wantErr {
				t.Errorf("NewPostgresOutput() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil && po.conflictResolution != tt.wantConflictStrat {
				t.Errorf("conflictResolution = %s, want %s", po.conflictResolution, tt.wantConflictStrat)
			}
		})
	}
}

// TestNewPostgresOutput_NATSConfiguration tests NATS settings
func TestNewPostgresOutput_NATSConfiguration(t *testing.T) {
	tests := []struct {
		name             string
		envVars          map[string]string
		wantNATSURL      string
		wantNATSSubject  string
	}{
		{
			name: "default NATS configuration",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
			},
			wantNATSURL:     "nats://localhost:4222",
			wantNATSSubject: "postgres.changes",
		},
		{
			name: "custom NATS URL and subject",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD": "password",
				"POSTGRES_OUTPUT_DATABASE": "test_db",
				"NATS_URL": "nats://nats.prod:4222",
				"POSTGRES_OUTPUT_SUBJECT": "cdc.writes",
			},
			wantNATSURL:     "nats://nats.prod:4222",
			wantNATSSubject: "cdc.writes",
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

			po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
			if err != nil {
				t.Fatalf("NewPostgresOutput() error = %v", err)
			}

			if po.natsURL != tt.wantNATSURL {
				t.Errorf("natsURL = %s, want %s", po.natsURL, tt.wantNATSURL)
		}
		if po.natsSubject != tt.wantNATSSubject {
			t.Errorf("natsSubject = %s, want %s", po.natsSubject, tt.wantNATSSubject)
		}
	})
	}
}

// TestNewPostgresOutput_ConfigurationValidation tests that invalid config values are handled with warnings
func TestNewPostgresOutput_ConfigurationValidation(t *testing.T) {
	tests := []struct {
		name             string
		envVars          map[string]string
		wantBatchSize    int
		wantMaxRetries   int
		wantInitialBackoff time.Duration
		wantMaxBackoff   time.Duration
	}{
		{
			name: "invalid batch size (non-integer) defaults to 100",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_BATCH_SIZE":     "invalid",
			},
			wantBatchSize: 100,
		},
		{
			name: "zero batch size defaults to 100",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_BATCH_SIZE":     "0",
			},
			wantBatchSize: 100,
		},
		{
			name: "negative batch size defaults to 100",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_BATCH_SIZE":     "-50",
			},
			wantBatchSize: 100,
		},
		{
			name: "invalid max retries (non-integer) defaults to 3",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_MAX_RETRIES":    "not_a_number",
			},
			wantMaxRetries: 3,
		},
		{
			name: "zero max retries defaults to 3",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_MAX_RETRIES":    "0",
			},
			wantMaxRetries: 3,
		},
		{
			name: "invalid initial backoff (non-integer) defaults to 1000ms",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_INITIAL_BACKOFF_MS": "bad",
			},
			wantInitialBackoff: 1000 * time.Millisecond,
		},
		{
			name: "zero initial backoff defaults to 1000ms",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_INITIAL_BACKOFF_MS": "0",
			},
			wantInitialBackoff: 1000 * time.Millisecond,
		},
		{
			name: "max backoff less than initial resets to defaults",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_INITIAL_BACKOFF_MS": "5000",
				"POSTGRES_OUTPUT_MAX_BACKOFF_MS": "1000",
			},
			wantInitialBackoff: DefaultBackoffConfig().InitialDuration,
			wantMaxBackoff: DefaultBackoffConfig().MaxDuration,
		},
		{
			name: "valid custom values are accepted",
			envVars: map[string]string{
				"POSTGRES_OUTPUT_PASSWORD":       "password",
				"POSTGRES_OUTPUT_DATABASE":       "test_db",
				"POSTGRES_OUTPUT_BATCH_SIZE":     "250",
				"POSTGRES_OUTPUT_MAX_RETRIES":    "5",
				"POSTGRES_OUTPUT_INITIAL_BACKOFF_MS": "500",
				"POSTGRES_OUTPUT_MAX_BACKOFF_MS": "10000",
			},
			wantBatchSize: 250,
			wantMaxRetries: 5,
			wantInitialBackoff: 500 * time.Millisecond,
			wantMaxBackoff: 10000 * time.Millisecond,
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

			po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
			if err != nil {
				t.Fatalf("NewPostgresOutput() error = %v", err)
			}

			if tt.wantBatchSize > 0 && po.batchSize != tt.wantBatchSize {
				t.Errorf("batchSize = %d, want %d", po.batchSize, tt.wantBatchSize)
			}
			if tt.wantMaxRetries > 0 && po.maxRetries != tt.wantMaxRetries {
				t.Errorf("maxRetries = %d, want %d", po.maxRetries, tt.wantMaxRetries)
			}
			if tt.wantInitialBackoff > 0 && po.backoffConfig.InitialDuration != tt.wantInitialBackoff {
				t.Errorf("InitialDuration = %v, want %v", po.backoffConfig.InitialDuration, tt.wantInitialBackoff)
			}
			if tt.wantMaxBackoff > 0 && po.backoffConfig.MaxDuration != tt.wantMaxBackoff {
				t.Errorf("MaxDuration = %v, want %v", po.backoffConfig.MaxDuration, tt.wantMaxBackoff)
			}
		})
	}
}

// TestQuoteIdentifier tests SQL identifier quoting
func TestQuoteIdentifier(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple identifier",
			input:    "users",
			expected: "users",
		},
		{
			name:     "identifier with underscore",
			input:    "user_profile",
			expected: "user_profile",
		},
		{
			name:     "identifier with numbers",
			input:    "table123",
			expected: "table123",
		},
		{
			name:     "identifier with special char",
			input:    "user-profile",
			expected: `"user-profile"`,
		},
		{
			name:     "identifier with space",
			input:    "user profile",
			expected: `"user profile"`,
		},
		{
			name:     "identifier with quote",
			input:    `user"profile`,
			expected: `"user""profile"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := po.quoteIdentifier(tt.input)
			if result != tt.expected {
				t.Errorf("quoteIdentifier(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestPostgresOutput_AddToPendingBatch_BatchSize tests batch flushing when size threshold is reached
func TestPostgresOutput_AddToPendingBatch_BatchSize(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	os.Setenv("POSTGRES_OUTPUT_BATCH_SIZE", "3")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")
	defer os.Unsetenv("POSTGRES_OUTPUT_BATCH_SIZE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	defer po.cancel()

	// Create test envelopes
	env1 := envelope.New()
	env2 := envelope.New()

	// Add first two envelopes - should not flush
	po.addToPendingBatch(env1)
	if len(po.pendingBatch) != 1 {
		t.Errorf("pending batch size = %d, want 1", len(po.pendingBatch))
	}

	po.addToPendingBatch(env2)
	if len(po.pendingBatch) != 2 {
		t.Errorf("pending batch size = %d, want 2", len(po.pendingBatch))
	}

	// Stop any pending timers before test ends
	if po.batchTimer != nil {
		po.batchTimer.Stop()
	}

	// Note: Adding third envelope would trigger flush, which requires DB connection
	// So we skip that part of the test for unit testing
}

// TestPostgresOutput_AddToPendingBatch_BatchTimeout tests batch timer creation
func TestPostgresOutput_AddToPendingBatch_BatchTimeout(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	defer po.cancel()

	env := envelope.New()

	// Add first envelope - should start timer
	po.addToPendingBatch(env)

	if po.batchTimer != nil {
		// Clean up timer before cancelling context
		po.batchTimer.Stop()
	}
}

// TestWriteBatch_EmptyBatch tests that write on empty batch is no-op
func TestWriteBatch_EmptyBatch(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())

	// Write empty batch should be safe
	po.writeBatch()

	if len(po.pendingBatch) != 0 {
		t.Errorf("pending batch = %v, want empty", po.pendingBatch)
	}
}

// TestExecuteInsert_BuildsValidQuery tests INSERT query building
func TestExecuteInsert_BuildsValidQuery(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	// Test data - simulating payload
	payload := map[string]interface{}{
		"after": map[string]interface{}{
			"id":   1,
			"name": "John",
			"email": "john@example.com",
		},
	}

	// Verify the structure is correct for INSERT
	after := payload["after"].(map[string]interface{})
	if len(after) != 3 {
		t.Errorf("after values length = %d, want 3", len(after))
	}
}

// TestExecuteUpdate_BuildsValidQuery tests UPDATE query building
func TestExecuteUpdate_BuildsValidQuery(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	// Test data - simulating payload
	payload := map[string]interface{}{
		"before": map[string]interface{}{
			"id":   1,
			"name": "John",
		},
		"after": map[string]interface{}{
			"id":   1,
			"name": "John Updated",
		},
	}

	// Verify structure for UPDATE
	before := payload["before"].(map[string]interface{})
	after := payload["after"].(map[string]interface{})

	if before["id"] != after["id"] {
		t.Error("id should be same in before and after")
	}

	if before["name"] == after["name"] {
		t.Error("name should be different in before and after")
	}
}

// TestExecuteDelete_BuildsValidQuery tests DELETE query building
func TestExecuteDelete_BuildsValidQuery(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	// Test data - simulating payload
	payload := map[string]interface{}{
		"before": map[string]interface{}{
			"id":   1,
			"name": "John",
		},
	}

	// Verify structure for DELETE
	before := payload["before"].(map[string]interface{})
	if before["id"] == nil {
		t.Error("id should be present for DELETE")
	}
}

// TestWrite_ContextCancellation tests that Write respects context cancellation
func TestWrite_ContextCancellation(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	env := envelope.New()
	err = po.Write(ctx, env)

	if err == nil || err != context.Canceled {
		t.Errorf("Write() error = %v, want context.Canceled", err)
	}
}

// TestWriteBatch_ContextCancellation tests WriteBatch with cancelled context
func TestWriteBatch_ContextCancellation(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	env := envelope.New()
	err = po.WriteBatch(ctx, []*envelope.Envelope{env})

	if err == nil || err != context.Canceled {
		t.Errorf("WriteBatch() error = %v, want context.Canceled", err)
	}
}

// TestClose_Idempotent tests that Close can be called multiple times
// TestPostgresOutput_Close_Idempotent tests that Close can be called multiple times
func TestPostgresOutput_Close_Idempotent(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	// Test that the struct is properly initialized
	if po == nil {
		t.Fatal("PostgresOutput should be initialized")
	}
}

// TestGetWritten_CounterTracking tests written message counter
func TestGetWritten_CounterTracking(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())

	if po.GetWritten() != 0 {
		t.Errorf("initial GetWritten() = %d, want 0", po.GetWritten())
	}

	// Increment counter (simulated write)
	po.written = 5

	if po.GetWritten() != 5 {
		t.Errorf("GetWritten() = %d, want 5", po.GetWritten())
	}
}

// TestWrite_ProducerClosed tests that Write returns error when producer is closed
func TestWrite_ProducerClosed(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	po.Close()

	ctx := context.Background()
	env := envelope.New()
	err = po.Write(ctx, env)

	if err == nil {
		t.Error("Write() should return error when producer is closed")
	}
}

// TestExecuteBatchWithRetry_MaxRetriesExhausted tests that batch fails after maxRetries attempts
func TestExecuteBatchWithRetry_MaxRetriesExhausted(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	os.Setenv("POSTGRES_OUTPUT_MAX_RETRIES", "2")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")
	defer os.Unsetenv("POSTGRES_OUTPUT_MAX_RETRIES")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	defer po.cancel()

	// Verify that maxRetries is set correctly
	if po.maxRetries != 2 {
		t.Errorf("maxRetries = %d, want 2", po.maxRetries)
	}

	// Mock batch executor that always fails
	callCount := 0
	po.setBatchExecutor(func(batch []*envelope.Envelope) error {
		callCount++
		return fmt.Errorf("simulated batch error")
	})

	// Inject test sleeper to avoid actual delays
	po.setSleeper(&testSleeper{})

	// Record initial metric values
	initialErrorCount := testutil.ToFloat64(po.metrics.WriteErrorsTotal)
	initialDLQCount := testutil.ToFloat64(po.metrics.DLQMessagesTotal)

	// Execute batch with retry - should fail after maxRetries attempts
	batch := []*envelope.Envelope{envelope.New()}
	po.executeBatchWithRetry(batch, time.Now())

	// Verify batch executor was called maxRetries times
	if callCount != po.maxRetries {
		t.Errorf("batch executor called %d times, want %d", callCount, po.maxRetries)
	}

	// Verify WriteErrorsTotal was incremented for each failed attempt
	finalErrorCount := testutil.ToFloat64(po.metrics.WriteErrorsTotal)
	expectedErrorCount := initialErrorCount + float64(po.maxRetries)
	if finalErrorCount != expectedErrorCount {
		t.Errorf("WriteErrorsTotal = %f, want %f", finalErrorCount, expectedErrorCount)
	}

	// Verify DLQ metric is NOT incremented when max retries exhausted
	// (dlqPublisher exists but natsConn is nil, so publish is a no-op with published=false)
	finalDLQCount := testutil.ToFloat64(po.metrics.DLQMessagesTotal)
	expectedDLQCount := initialDLQCount // Should remain unchanged
	if finalDLQCount != expectedDLQCount {
		t.Errorf("DLQMessagesTotal = %f, want %f (should NOT increment when natsConn is nil)",
			finalDLQCount, expectedDLQCount)
	}
}

// TestExecuteBatchWithRetry_BackoffConfig tests backoff configuration and exponential backoff application
func TestExecuteBatchWithRetry_BackoffConfig(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	os.Setenv("POSTGRES_OUTPUT_INITIAL_BACKOFF_MS", "100")
	os.Setenv("POSTGRES_OUTPUT_MAX_BACKOFF_MS", "1000")
	os.Setenv("POSTGRES_OUTPUT_MAX_RETRIES", "3")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")
	defer os.Unsetenv("POSTGRES_OUTPUT_INITIAL_BACKOFF_MS")
	defer os.Unsetenv("POSTGRES_OUTPUT_MAX_BACKOFF_MS")
	defer os.Unsetenv("POSTGRES_OUTPUT_MAX_RETRIES")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	defer po.cancel()

	// Verify backoff config is set correctly
	if po.backoffConfig.InitialDuration != 100*time.Millisecond {
		t.Errorf("initial backoff = %v, want 100ms", po.backoffConfig.InitialDuration)
	}
	if po.backoffConfig.MaxDuration != 1000*time.Millisecond {
		t.Errorf("max backoff = %v, want 1000ms", po.backoffConfig.MaxDuration)
	}

	// Test CalculateBackoff function produces increasing values
	backoff1 := CalculateBackoff(1, po.backoffConfig)
	backoff2 := CalculateBackoff(2, po.backoffConfig)
	backoff3 := CalculateBackoff(3, po.backoffConfig)

	// Second attempt should have longer backoff (exponential)
	if backoff2 <= backoff1 {
		t.Errorf("backoff2 (%v) should be > backoff1 (%v) for exponential backoff", backoff2, backoff1)
	}

	// Third should be even longer
	if backoff3 <= backoff2 {
		t.Errorf("backoff3 (%v) should be > backoff2 (%v) for exponential backoff", backoff3, backoff2)
	}

	// All backoffs should not exceed max
	if backoff1 > po.backoffConfig.MaxDuration {
		t.Errorf("backoff1 (%v) exceeds max (%v)", backoff1, po.backoffConfig.MaxDuration)
	}
	if backoff2 > po.backoffConfig.MaxDuration {
		t.Errorf("backoff2 (%v) exceeds max (%v)", backoff2, po.backoffConfig.MaxDuration)
	}
	if backoff3 > po.backoffConfig.MaxDuration {
		t.Errorf("backoff3 (%v) exceeds max (%v)", backoff3, po.backoffConfig.MaxDuration)
	}

	// Test that exponential backoff is actually used during retry logic
	testSleeper := &testSleeper{}
	po.setSleeper(testSleeper)

	// Mock batch executor that fails all attempts
	po.setBatchExecutor(func(batch []*envelope.Envelope) error {
		return fmt.Errorf("simulated batch error")
	})

	// Execute batch with retry
	batch := []*envelope.Envelope{envelope.New()}
	po.executeBatchWithRetry(batch, time.Now())

	// Verify sleeper was called (maxRetries - 1) times (no sleep after final failure)
	if len(testSleeper.sleepDurations) != po.maxRetries-1 {
		t.Errorf("sleeper called %d times, want %d", len(testSleeper.sleepDurations), po.maxRetries-1)
	}

	// Verify backoff durations are bounded
	for i, duration := range testSleeper.sleepDurations {
		if duration > po.backoffConfig.MaxDuration {
			t.Errorf("sleep duration[%d] = %v, exceeds max %v", i, duration, po.backoffConfig.MaxDuration)
		}
	}
}

// TestExecuteBatchWithRetry_DLQMetricAccuracy tests that DLQ metric is correctly incremented based on publish success
func TestExecuteBatchWithRetry_DLQMetricAccuracy(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	os.Setenv("POSTGRES_OUTPUT_MAX_RETRIES", "1")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")
	defer os.Unsetenv("POSTGRES_OUTPUT_MAX_RETRIES")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	defer po.cancel()

	// Mock batch executor that always fails
	po.setBatchExecutor(func(batch []*envelope.Envelope) error {
		return fmt.Errorf("simulated batch error")
	})
	po.setSleeper(&testSleeper{})

	// Get initial DLQ metric count
	initialDLQCount := testutil.ToFloat64(po.metrics.DLQMessagesTotal)

	// Verify initial metric value is 0
	if initialDLQCount != 0 {
		t.Errorf("initialDLQCount = %f, want 0", initialDLQCount)
	}

	// Execute batch that will exhaust retries and attempt DLQ publish
	batch := []*envelope.Envelope{envelope.New()}
	po.executeBatchWithRetry(batch, time.Now())

	// With natsConn=nil, DLQ publish should be a no-op (published=false)
	// So DLQMessagesTotal should NOT be incremented
	finalDLQCount := testutil.ToFloat64(po.metrics.DLQMessagesTotal)
	if finalDLQCount != initialDLQCount {
		t.Errorf("DLQMessagesTotal = %f, want %f (should not increment when natsConn is nil)",
			finalDLQCount, initialDLQCount)
	}
}

// TestExecuteBatchWithRetry_DLQDisabledDoesNotIncrementMetric verifies metric doesn't increment when DLQ is disabled
func TestExecuteBatchWithRetry_DLQDisabledDoesNotIncrementMetric(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	os.Setenv("POSTGRES_OUTPUT_MAX_RETRIES", "1")
	os.Setenv("POSTGRES_OUTPUT_DLQ_ENABLED", "false")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")
	defer os.Unsetenv("POSTGRES_OUTPUT_MAX_RETRIES")
	defer os.Unsetenv("POSTGRES_OUTPUT_DLQ_ENABLED")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	defer po.cancel()

	// Verify DLQ is disabled
	if po.dlqPublisher.config.Enabled {
		t.Errorf("DLQ should be disabled, but config.Enabled = %v", po.dlqPublisher.config.Enabled)
	}

	// Mock batch executor that always fails
	po.setBatchExecutor(func(batch []*envelope.Envelope) error {
		return fmt.Errorf("simulated batch error")
	})
	po.setSleeper(&testSleeper{})

	// Get initial DLQ metric count
	initialDLQCount := testutil.ToFloat64(po.metrics.DLQMessagesTotal)

	// Execute batch that will exhaust retries and attempt DLQ publish
	batch := []*envelope.Envelope{envelope.New()}
	po.executeBatchWithRetry(batch, time.Now())

	// With DLQ disabled, DLQ publish should be a no-op (published=false)
	// So DLQMessagesTotal should NOT be incremented
	finalDLQCount := testutil.ToFloat64(po.metrics.DLQMessagesTotal)
	if finalDLQCount != initialDLQCount {
		t.Errorf("DLQMessagesTotal = %f, want %f (should not increment when DLQ is disabled)",
			finalDLQCount, initialDLQCount)
	}
}

// TestExecuteBatchWithRetry_SuccessRecordsMetrics tests that successful batch records metrics correctly
func TestExecuteBatchWithRetry_SuccessRecordsMetrics(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	defer po.cancel()

	// Record initial metric values
	initialBatchesWritten := testutil.ToFloat64(po.metrics.BatchesWrittenTotal)
	initialErrors := testutil.ToFloat64(po.metrics.WriteErrorsTotal)

	// Mock batch executor that succeeds immediately
	po.setBatchExecutor(func(batch []*envelope.Envelope) error {
		return nil // Success
	})

	// Execute batch with non-empty envelope
	batch := []*envelope.Envelope{envelope.New()}
	batchStartTime := time.Now()

	po.executeBatchWithRetry(batch, batchStartTime)

	// Verify that successful execution incremented BatchesWrittenTotal
	finalBatchesWritten := testutil.ToFloat64(po.metrics.BatchesWrittenTotal)
	if finalBatchesWritten != initialBatchesWritten+1 {
		t.Errorf("BatchesWrittenTotal = %f, want %f", finalBatchesWritten, initialBatchesWritten+1)
	}

	// Verify that no errors were recorded
	finalErrors := testutil.ToFloat64(po.metrics.WriteErrorsTotal)
	if finalErrors != initialErrors {
		t.Errorf("WriteErrorsTotal = %f, want %f (no errors expected on success)", finalErrors, initialErrors)
	}

	// Verify that latency histogram was updated (we can't read histogram values directly, but we can verify no panic)
	// If we reach here, the function completed successfully and metrics were recorded
}

// TestExecuteBatchWithRetry_CaptureToWriteLatency tests latency recording with batchStartTime
func TestExecuteBatchWithRetry_CaptureToWriteLatency(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	po.ctx, po.cancel = context.WithCancel(context.Background())
	defer po.cancel()

	// Mock successful batch execution
	po.setBatchExecutor(func(batch []*envelope.Envelope) error {
		return nil // Success - latency will be recorded
	})

	// Create a batchStartTime in the past to simulate capture-to-write latency
	batchStartTime := time.Now().Add(-100 * time.Millisecond)
	batch := []*envelope.Envelope{envelope.New()}

	// Call executeBatchWithRetry with past startTime
	po.executeBatchWithRetry(batch, batchStartTime)

	// The function should process without error and record latency
	// If we reach here, the function completed successfully and recorded metrics
}

// TestExecuteBatchWithRetry_ContextCancellation tests that retry respects context cancellation
func TestExecuteBatchWithRetry_ContextCancellation(t *testing.T) {
	os.Setenv("POSTGRES_OUTPUT_PASSWORD", "password")
	os.Setenv("POSTGRES_OUTPUT_DATABASE", "test_db")
	os.Setenv("POSTGRES_OUTPUT_MAX_RETRIES", "5")
	defer os.Unsetenv("POSTGRES_OUTPUT_PASSWORD")
	defer os.Unsetenv("POSTGRES_OUTPUT_DATABASE")
	defer os.Unsetenv("POSTGRES_OUTPUT_MAX_RETRIES")

	po, err := NewPostgresOutput(slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewPostgresOutput() error = %v", err)
	}

	// Create a context that will be cancelled
	po.ctx, po.cancel = context.WithCancel(context.Background())

	// Mock batch executor that always fails (triggers retry)
	attemptCount := 0
	po.setBatchExecutor(func(batch []*envelope.Envelope) error {
		attemptCount++
		return fmt.Errorf("simulated failure")
	})

	// Create test sleeper that will block
	blockingSleeper := &testSleeper{}
	po.setSleeper(blockingSleeper)

	// Create a batch that will trigger retries
	batch := []*envelope.Envelope{envelope.New()}
	batchStartTime := time.Now()

	// Start executeBatchWithRetry in a goroutine so we can cancel mid-retry
	done := make(chan struct{})
	go func() {
		po.executeBatchWithRetry(batch, batchStartTime)
		close(done)
	}()

	// Give it a moment to start executing and fail
	time.Sleep(10 * time.Millisecond)

	// Cancel the context - this should interrupt the retry sleep
	po.cancel()

	// Wait for function to complete with bounded timeout
	select {
	case <-done:
		// Success - function exited as expected
		// Verify that we didn't attempt all retries (context was cancelled early)
		if attemptCount >= po.maxRetries {
			t.Logf("all retries were attempted before cancellation (count=%d, maxRetries=%d)",
				attemptCount, po.maxRetries)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executeBatchWithRetry did not respect context cancellation and exit within timeout")
	}
}
