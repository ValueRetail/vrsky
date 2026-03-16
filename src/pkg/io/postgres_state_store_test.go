//go:build integration
// +build integration

package io

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v4/stdlib"
)

// ============================================================================
// TEST HELPERS
// ============================================================================

const (
	defaultTestPostgresURL = "postgres://postgres:source_password@localhost:5432/source_db?sslmode=disable"
)

// getTestPostgresURL returns the PostgreSQL connection string for tests
func getTestPostgresURL() string {
	if url := os.Getenv("TEST_POSTGRES_URL"); url != "" {
		return url
	}
	return defaultTestPostgresURL
}

// setupTestDB creates a database connection and ensures the schema exists
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	db, err := sql.Open("pgx", getTestPostgresURL())
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("failed to ping database: %v", err)
	}

	// Create the api_consumer_state table if it doesn't exist
	createTableSQL := `
		CREATE TABLE IF NOT EXISTS api_consumer_state (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			consumer_id VARCHAR(255) NOT NULL UNIQUE,
			tenant_id UUID,
			state_data JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			total_polls BIGINT DEFAULT 0,
			total_records_fetched BIGINT DEFAULT 0,
			last_error TEXT,
			last_error_at TIMESTAMP WITH TIME ZONE
		);

		CREATE INDEX IF NOT EXISTS idx_api_consumer_state_consumer_id 
			ON api_consumer_state(consumer_id);
	`

	_, err = db.ExecContext(ctx, createTableSQL)
	if err != nil {
		db.Close()
		t.Fatalf("failed to create test table: %v", err)
	}

	// Cleanup function
	cleanup := func() {
		// Clean up test data (delete all rows with test- prefix)
		_, _ = db.Exec("DELETE FROM api_consumer_state WHERE consumer_id LIKE 'test-%'")
		db.Close()
	}

	return db, cleanup
}

// newTestPostgresLogger creates a logger for tests
func newTestPostgresLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// ============================================================================
// CONSTRUCTOR TESTS
// ============================================================================

func TestNewPostgresStateStore_ValidDB(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewPostgresStateStore(db, newTestPostgresLogger())
	if err != nil {
		t.Fatalf("NewPostgresStateStore() error = %v", err)
	}
	if store == nil {
		t.Fatal("NewPostgresStateStore() returned nil")
	}
}

func TestNewPostgresStateStore_NilDB(t *testing.T) {
	_, err := NewPostgresStateStore(nil, newTestPostgresLogger())
	if err == nil {
		t.Fatal("expected error for nil database")
	}
}

func TestNewPostgresStateStore_NilLogger(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Should not error, should use default logger
	store, err := NewPostgresStateStore(db, nil)
	if err != nil {
		t.Fatalf("NewPostgresStateStore() error = %v", err)
	}
	if store == nil {
		t.Fatal("NewPostgresStateStore() returned nil")
	}
}

// ============================================================================
// SAVE AND LOAD TESTS
// ============================================================================

func TestPostgresStateStore_SaveAndLoad_NewState(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-consumer-%d", time.Now().UnixNano())

	// Create state
	state := &apiInputState{
		ConsumerID:     consumerID,
		Offset:         100,
		Cursor:         "cursor-abc",
		NextLink:       "https://api.example.com/next?page=2",
		LastPoll:       time.Now().Truncate(time.Millisecond),
		FailureCount:   2,
		IsExhausted:    false,
		PaginationType: "cursor",
	}

	// Save
	err := store.Save(ctx, consumerID, state)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load
	loaded, err := store.Load(ctx, consumerID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("Load() returned nil, expected state")
	}

	// Verify fields
	if loaded.ConsumerID != consumerID {
		t.Errorf("ConsumerID = %v, want %v", loaded.ConsumerID, consumerID)
	}
	if loaded.Offset != 100 {
		t.Errorf("Offset = %v, want 100", loaded.Offset)
	}
	if loaded.Cursor != "cursor-abc" {
		t.Errorf("Cursor = %v, want cursor-abc", loaded.Cursor)
	}
	if loaded.NextLink != "https://api.example.com/next?page=2" {
		t.Errorf("NextLink = %v, want https://api.example.com/next?page=2", loaded.NextLink)
	}
	if loaded.FailureCount != 2 {
		t.Errorf("FailureCount = %v, want 2", loaded.FailureCount)
	}
	if loaded.PaginationType != "cursor" {
		t.Errorf("PaginationType = %v, want cursor", loaded.PaginationType)
	}
}

func TestPostgresStateStore_SaveAndLoad_UpdateExisting(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-update-%d", time.Now().UnixNano())

	// Initial save
	state := &apiInputState{
		ConsumerID:     consumerID,
		Offset:         10,
		PaginationType: "offset",
	}
	err := store.Save(ctx, consumerID, state)
	if err != nil {
		t.Fatalf("Initial Save() error = %v", err)
	}

	// Get initial stats
	initialPolls, _, err := store.GetStats(ctx, consumerID)
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	if initialPolls != 1 {
		t.Errorf("initial total_polls = %d, want 1", initialPolls)
	}

	// Update save
	state.Offset = 50
	state.Cursor = "new-cursor"
	err = store.Save(ctx, consumerID, state)
	if err != nil {
		t.Fatalf("Update Save() error = %v", err)
	}

	// Load and verify
	loaded, err := store.Load(ctx, consumerID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Offset != 50 {
		t.Errorf("Offset after update = %v, want 50", loaded.Offset)
	}
	if loaded.Cursor != "new-cursor" {
		t.Errorf("Cursor after update = %v, want new-cursor", loaded.Cursor)
	}

	// Verify poll count incremented
	polls, _, err := store.GetStats(ctx, consumerID)
	if err != nil {
		t.Fatalf("GetStats() after update error = %v", err)
	}
	if polls != 2 {
		t.Errorf("total_polls after update = %d, want 2", polls)
	}
}

func TestPostgresStateStore_Load_NonExistent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()

	// Load non-existent consumer
	loaded, err := store.Load(ctx, "test-nonexistent-consumer-xyz")
	if err != nil {
		t.Fatalf("Load() error = %v, expected nil error for non-existent", err)
	}
	if loaded != nil {
		t.Errorf("Load() = %v, want nil for non-existent consumer", loaded)
	}
}

// ============================================================================
// STATISTICS TESTS
// ============================================================================

func TestPostgresStateStore_SaveWithStats(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-stats-%d", time.Now().UnixNano())

	state := &apiInputState{
		ConsumerID:     consumerID,
		Offset:         0,
		PaginationType: "offset",
	}

	// Save with 10 records
	err := store.SaveWithStats(ctx, consumerID, state, 10)
	if err != nil {
		t.Fatalf("SaveWithStats() error = %v", err)
	}

	// Save again with 25 records
	state.Offset = 10
	err = store.SaveWithStats(ctx, consumerID, state, 25)
	if err != nil {
		t.Fatalf("SaveWithStats() second call error = %v", err)
	}

	// Verify cumulative stats
	polls, records, err := store.GetStats(ctx, consumerID)
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}

	if polls != 2 {
		t.Errorf("total_polls = %d, want 2", polls)
	}
	if records != 35 {
		t.Errorf("total_records_fetched = %d, want 35", records)
	}
}

func TestPostgresStateStore_GetStats_NonExistent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()

	polls, records, err := store.GetStats(ctx, "test-nonexistent-stats")
	if err != nil {
		t.Fatalf("GetStats() error = %v, expected nil for non-existent", err)
	}
	if polls != 0 || records != 0 {
		t.Errorf("GetStats() = (%d, %d), want (0, 0) for non-existent", polls, records)
	}
}

// ============================================================================
// ERROR TRACKING TESTS
// ============================================================================

func TestPostgresStateStore_SaveError(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-error-%d", time.Now().UnixNano())

	// First, create state
	state := &apiInputState{
		ConsumerID: consumerID,
		Offset:     0,
	}
	err := store.Save(ctx, consumerID, state)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Save error
	err = store.SaveError(ctx, consumerID, "connection timeout after 30s")
	if err != nil {
		t.Fatalf("SaveError() error = %v", err)
	}

	// Verify error was saved (load state and check via raw query)
	var lastError sql.NullString
	var lastErrorAt sql.NullTime
	err = db.QueryRowContext(ctx,
		"SELECT last_error, last_error_at FROM api_consumer_state WHERE consumer_id = $1",
		consumerID,
	).Scan(&lastError, &lastErrorAt)

	if err != nil {
		t.Fatalf("Query error = %v", err)
	}

	if !lastError.Valid || lastError.String != "connection timeout after 30s" {
		t.Errorf("last_error = %v, want 'connection timeout after 30s'", lastError.String)
	}
	if !lastErrorAt.Valid {
		t.Error("last_error_at should be set")
	}
}

func TestPostgresStateStore_SaveError_NewConsumer(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-new-error-%d", time.Now().UnixNano())

	// Save error for non-existent consumer (should create entry)
	err := store.SaveError(ctx, consumerID, "initial error")
	if err != nil {
		t.Fatalf("SaveError() error = %v", err)
	}

	// Verify entry was created
	loaded, err := store.Load(ctx, consumerID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("Load() returned nil, expected state entry")
	}
}

// ============================================================================
// DELETE TESTS
// ============================================================================

func TestPostgresStateStore_Delete(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-delete-%d", time.Now().UnixNano())

	// Create state
	state := &apiInputState{
		ConsumerID: consumerID,
		Offset:     100,
	}
	err := store.Save(ctx, consumerID, state)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify exists
	loaded, _ := store.Load(ctx, consumerID)
	if loaded == nil {
		t.Fatal("state should exist before delete")
	}

	// Delete
	err = store.Delete(ctx, consumerID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify deleted
	loaded, err = store.Load(ctx, consumerID)
	if err != nil {
		t.Fatalf("Load() after delete error = %v", err)
	}
	if loaded != nil {
		t.Error("state should be nil after delete")
	}
}

func TestPostgresStateStore_Delete_NonExistent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()

	// Delete non-existent (should not error - idempotent)
	err := store.Delete(ctx, "test-delete-nonexistent")
	if err != nil {
		t.Errorf("Delete() error = %v, expected nil for non-existent (idempotent)", err)
	}
}

// ============================================================================
// MULTI-CONSUMER ISOLATION TESTS
// ============================================================================

func TestPostgresStateStore_MultiConsumer_Isolation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()

	consumerA := fmt.Sprintf("test-consumer-A-%d", time.Now().UnixNano())
	consumerB := fmt.Sprintf("test-consumer-B-%d", time.Now().UnixNano())

	// Save state for consumer A
	stateA := &apiInputState{
		ConsumerID:     consumerA,
		Offset:         100,
		Cursor:         "cursor-A",
		PaginationType: "cursor",
	}
	err := store.Save(ctx, consumerA, stateA)
	if err != nil {
		t.Fatalf("Save() consumer A error = %v", err)
	}

	// Save state for consumer B
	stateB := &apiInputState{
		ConsumerID:     consumerB,
		Offset:         200,
		Cursor:         "cursor-B",
		PaginationType: "offset",
	}
	err = store.Save(ctx, consumerB, stateB)
	if err != nil {
		t.Fatalf("Save() consumer B error = %v", err)
	}

	// Load consumer A - should only see A's state
	loadedA, _ := store.Load(ctx, consumerA)
	if loadedA.Offset != 100 || loadedA.Cursor != "cursor-A" {
		t.Errorf("Consumer A state corrupted: offset=%d, cursor=%s", loadedA.Offset, loadedA.Cursor)
	}

	// Load consumer B - should only see B's state
	loadedB, _ := store.Load(ctx, consumerB)
	if loadedB.Offset != 200 || loadedB.Cursor != "cursor-B" {
		t.Errorf("Consumer B state corrupted: offset=%d, cursor=%s", loadedB.Offset, loadedB.Cursor)
	}

	// Update A should not affect B
	stateA.Offset = 150
	err = store.Save(ctx, consumerA, stateA)
	if err != nil {
		t.Fatalf("Update Save() consumer A error = %v", err)
	}

	loadedB, _ = store.Load(ctx, consumerB)
	if loadedB.Offset != 200 {
		t.Errorf("Consumer B offset changed after A update: got %d, want 200", loadedB.Offset)
	}
}

// ============================================================================
// CONTEXT TESTS
// ============================================================================

func TestPostgresStateStore_ContextCancellation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())

	// Create already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	consumerID := "test-cancelled-context"
	state := &apiInputState{ConsumerID: consumerID, Offset: 1}

	// Save should fail with cancelled context
	err := store.Save(ctx, consumerID, state)
	if err == nil {
		t.Error("Save() should error with cancelled context")
	}

	// Load should fail with cancelled context
	_, err = store.Load(ctx, consumerID)
	if err == nil {
		t.Error("Load() should error with cancelled context")
	}
}

func TestPostgresStateStore_ContextTimeout(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())

	// Create context with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Wait for timeout
	time.Sleep(10 * time.Millisecond)

	consumerID := "test-timeout-context"
	state := &apiInputState{ConsumerID: consumerID, Offset: 1}

	// Operations should fail with timed out context
	err := store.Save(ctx, consumerID, state)
	if err == nil {
		t.Error("Save() should error with timed out context")
	}
}

// ============================================================================
// PING TEST
// ============================================================================

func TestPostgresStateStore_Ping(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()

	err := store.Ping(ctx)
	if err != nil {
		t.Errorf("Ping() error = %v", err)
	}
}

// ============================================================================
// JSON EDGE CASES
// ============================================================================

func TestPostgresStateStore_JSON_EmptyState(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-empty-state-%d", time.Now().UnixNano())

	// Save empty state
	state := &apiInputState{}
	err := store.Save(ctx, consumerID, state)
	if err != nil {
		t.Fatalf("Save() empty state error = %v", err)
	}

	// Load should work
	loaded, err := store.Load(ctx, consumerID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("Load() returned nil")
	}
}

func TestPostgresStateStore_JSON_SpecialCharacters(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-special-chars-%d", time.Now().UnixNano())

	// State with special characters
	state := &apiInputState{
		ConsumerID: consumerID,
		Cursor:     `cursor-with-"quotes"-and-\-backslash`,
		NextLink:   "https://api.example.com/?query=hello%20world&emoji=🚀",
	}
	err := store.Save(ctx, consumerID, state)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := store.Load(ctx, consumerID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Cursor != state.Cursor {
		t.Errorf("Cursor = %q, want %q", loaded.Cursor, state.Cursor)
	}
	if loaded.NextLink != state.NextLink {
		t.Errorf("NextLink = %q, want %q", loaded.NextLink, state.NextLink)
	}
}

func TestPostgresStateStore_JSON_LargeState(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewPostgresStateStore(db, newTestPostgresLogger())
	ctx := context.Background()
	consumerID := fmt.Sprintf("test-large-state-%d", time.Now().UnixNano())

	// Create large cursor (simulate base64 encoded token)
	largeCursor := make([]byte, 10000)
	for i := range largeCursor {
		largeCursor[i] = byte('A' + (i % 26))
	}

	state := &apiInputState{
		ConsumerID: consumerID,
		Cursor:     string(largeCursor),
	}
	err := store.Save(ctx, consumerID, state)
	if err != nil {
		t.Fatalf("Save() large state error = %v", err)
	}

	loaded, err := store.Load(ctx, consumerID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(loaded.Cursor) != 10000 {
		t.Errorf("Cursor length = %d, want 10000", len(loaded.Cursor))
	}
}
