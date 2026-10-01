package checkpoint

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestInMemoryStore_SaveAndGet(t *testing.T) {
	store := NewInMemoryStore()
	ctx := context.Background()

	cp := &Checkpoint{
		TenantID:               "tenant-1",
		ConnectionID:           "conn-1",
		NodeID:                 "node-1",
		LastProcessedMessageID: "msg-123",
		LastProcessedAt:        time.Now(),
		MessageCount:           100,
	}

	// Save checkpoint
	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Get checkpoint
	got, err := store.Get(ctx, "tenant-1", "conn-1", "node-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if got == nil {
		t.Fatal("Get() returned nil, expected checkpoint")
	}

	if got.LastProcessedMessageID != "msg-123" {
		t.Errorf("LastProcessedMessageID = %v, want msg-123", got.LastProcessedMessageID)
	}

	if got.MessageCount != 100 {
		t.Errorf("MessageCount = %v, want 100", got.MessageCount)
	}

	// Update checkpoint
	cp.LastProcessedMessageID = "msg-456"
	cp.MessageCount = 200
	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("Save() update error = %v", err)
	}

	got, err = store.Get(ctx, "tenant-1", "conn-1", "node-1")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}

	if got.LastProcessedMessageID != "msg-456" {
		t.Errorf("LastProcessedMessageID after update = %v, want msg-456", got.LastProcessedMessageID)
	}

	if got.MessageCount != 200 {
		t.Errorf("MessageCount after update = %v, want 200", got.MessageCount)
	}
}

func TestInMemoryStore_GetNonExistent(t *testing.T) {
	store := NewInMemoryStore()
	ctx := context.Background()

	got, err := store.Get(ctx, "tenant-1", "conn-1", "node-nonexistent")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if got != nil {
		t.Errorf("Get() = %v, want nil for non-existent checkpoint", got)
	}
}

func TestInMemoryStore_Delete(t *testing.T) {
	store := NewInMemoryStore()
	ctx := context.Background()

	// Save checkpoint
	cp := &Checkpoint{
		TenantID:               "tenant-1",
		ConnectionID:           "conn-1",
		NodeID:                 "node-1",
		LastProcessedMessageID: "msg-123",
		LastProcessedAt:        time.Now(),
		MessageCount:           100,
	}
	_ = store.Save(ctx, cp)

	// Delete checkpoint
	if err := store.Delete(ctx, "tenant-1", "conn-1", "node-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify deleted
	got, _ := store.Get(ctx, "tenant-1", "conn-1", "node-1")
	if got != nil {
		t.Error("Get() after Delete() should return nil")
	}
}

func TestInMemoryStore_DeleteForConnection(t *testing.T) {
	store := NewInMemoryStore()
	ctx := context.Background()

	// Save multiple checkpoints for same connection
	for i := 1; i <= 3; i++ {
		cp := &Checkpoint{
			TenantID:               "tenant-1",
			ConnectionID:           "conn-1",
			NodeID:                 string(rune('a' + i - 1)),
			LastProcessedMessageID: "msg-123",
			LastProcessedAt:        time.Now(),
			MessageCount:           int64(i * 100),
		}
		_ = store.Save(ctx, cp)
	}

	// Save checkpoint for different connection
	cp2 := &Checkpoint{
		TenantID:               "tenant-1",
		ConnectionID:           "conn-2",
		NodeID:                 "node-x",
		LastProcessedMessageID: "msg-other",
		LastProcessedAt:        time.Now(),
		MessageCount:           999,
	}
	_ = store.Save(ctx, cp2)

	// Delete all checkpoints for conn-1
	if err := store.DeleteForConnection(ctx, "tenant-1", "conn-1"); err != nil {
		t.Fatalf("DeleteForConnection() error = %v", err)
	}

	// Verify conn-1 checkpoints are deleted
	for i := 1; i <= 3; i++ {
		got, _ := store.Get(ctx, "tenant-1", "conn-1", string(rune('a'+i-1)))
		if got != nil {
			t.Errorf("Checkpoint for node %d should be deleted", i)
		}
	}

	// Verify conn-2 checkpoint still exists
	got, _ := store.Get(ctx, "tenant-1", "conn-2", "node-x")
	if got == nil {
		t.Error("Checkpoint for conn-2 should still exist")
	}
}

func TestCheckpoint_UpdatedAtIsSet(t *testing.T) {
	store := NewInMemoryStore()
	ctx := context.Background()

	before := time.Now()

	cp := &Checkpoint{
		TenantID:               "tenant-1",
		ConnectionID:           "conn-1",
		NodeID:                 "node-1",
		LastProcessedMessageID: "msg-123",
		LastProcessedAt:        time.Now(),
		MessageCount:           100,
	}
	_ = store.Save(ctx, cp)

	after := time.Now()

	got, _ := store.Get(ctx, "tenant-1", "conn-1", "node-1")
	if got.UpdatedAt.Before(before) || got.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt = %v, should be between %v and %v", got.UpdatedAt, before, after)
	}
}

// TestInMemoryStore_StateRoundTrip: free-form state is saved and read back
// beside the watermark (the BC consumer keeps its seen pictures there).
func TestInMemoryStore_StateRoundTrip(t *testing.T) {
	store := NewInMemoryStore()
	ctx := context.Background()
	if err := store.Save(ctx, &Checkpoint{TenantID: "t", ConnectionID: "c", NodeID: "n",
		LastProcessedMessageID: "2026-10-01T00:00:00Z", State: json.RawMessage(`{"pictures":{"r1":{"picture_id":"p1","number":"HBB-1000"}}}`)}); err != nil {
		t.Fatalf("save: %v", err)
	}
	cp, err := store.Get(ctx, "t", "c", "n")
	if err != nil || cp == nil {
		t.Fatalf("get: %v %v", cp, err)
	}
	var got struct {
		Pictures map[string]struct {
			Number string `json:"number"`
		} `json:"pictures"`
	}
	if err := json.Unmarshal(cp.State, &got); err != nil {
		t.Fatalf("state does not parse: %v (%s)", err, cp.State)
	}
	if got.Pictures["r1"].Number != "HBB-1000" {
		t.Errorf("state = %s", cp.State)
	}
}

// TestPostgresStore_StateColumn pins the SQL: state must be written on
// insert AND on the conflict update, else a second save silently keeps the
// old state, and read back in Get. An empty State is stored as {} because
// the column is NOT NULL.
func TestPostgresStore_StateColumn(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	ctx := context.Background()

	mock.ExpectExec(`INSERT INTO connection_node_checkpoints[\s\S]*state[\s\S]*DO UPDATE SET[\s\S]*state = EXCLUDED\.state`).
		WithArgs("t", "c", "n", "cursor", sqlmock.AnyArg(), int64(3), []byte(`{"pictures":{}}`), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := store.Save(ctx, &Checkpoint{TenantID: "t", ConnectionID: "c", NodeID: "n",
		LastProcessedMessageID: "cursor", MessageCount: 3, State: json.RawMessage(`{"pictures":{}}`)}); err != nil {
		t.Fatalf("save: %v", err)
	}
	mock.ExpectExec(`INSERT INTO connection_node_checkpoints`).
		WithArgs("t", "c", "n", "cursor", sqlmock.AnyArg(), int64(0), []byte(`{}`), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := store.Save(ctx, &Checkpoint{TenantID: "t", ConnectionID: "c", NodeID: "n", LastProcessedMessageID: "cursor"}); err != nil {
		t.Fatalf("save without state: %v", err)
	}

	mock.ExpectQuery(`SELECT tenant_id, connection_id, node_id,[\s\S]*state, updated_at`).
		WithArgs("t", "c", "n").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "connection_id", "node_id", "last_processed_message_id", "last_processed_at", "message_count", "state", "updated_at"}).
			AddRow("t", "c", "n", "cursor", time.Now(), int64(3), []byte(`{"pictures":{"r1":{}}}`), time.Now()))
	cp, err := store.Get(ctx, "t", "c", "n")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(cp.State) != `{"pictures":{"r1":{}}}` {
		t.Errorf("state read back = %s", cp.State)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
