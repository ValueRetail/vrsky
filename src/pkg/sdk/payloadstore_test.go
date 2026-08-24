package sdk

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// memStore is an in-memory objectstore.ObjectStore for testing the claim-check
// offload/rehydrate path without a live backend.
type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (m *memStore) List(context.Context, string) ([]objectstore.Object, error) { return nil, nil }
func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
func (m *memStore) Copy(context.Context, string, string) error { return nil }
func (m *memStore) Close() error                               { return nil }

func (m *memStore) Get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[key], "", nil
}
func (m *memStore) Put(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), body...)
	return nil
}
func (m *memStore) GetStream(_ context.Context, key string) (io.ReadCloser, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, "", io.ErrUnexpectedEOF
	}
	return io.NopCloser(bytes.NewReader(b)), "", nil
}
func (m *memStore) PutStream(_ context.Context, key string, body io.Reader, _ string) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = b
	m.puts++
	return nil
}

func mkEnv(payload []byte) *envelope.Envelope {
	return &envelope.Envelope{
		ID:            "env-1",
		TenantID:      "tenant-x",
		IntegrationID: "conn-1",
		Payload:       payload,
		ContentType:   "application/json",
	}
}

func TestOffloadRehydrate_LargePayloadRoundTrips(t *testing.T) {
	store := newMemStore()
	logger := slog.Default()
	big := bytes.Repeat([]byte("A"), 1000)
	env := mkEnv(big)

	offloaded, err := offloadIfLarge(context.Background(), store, env, 256, logger)
	if err != nil {
		t.Fatalf("offloadIfLarge: %v", err)
	}
	if !offloaded {
		t.Fatal("expected payload to be offloaded")
	}
	if env.Payload != nil {
		t.Errorf("payload should be cleared after offload, got %d bytes", len(env.Payload))
	}
	if env.PayloadRef == "" {
		t.Fatal("PayloadRef should be set after offload")
	}
	if env.PayloadSize != int64(len(big)) {
		t.Errorf("PayloadSize = %d, want %d", env.PayloadSize, len(big))
	}

	// The (small) envelope is what would ride NATS — verify the offloaded object
	// is retrievable and rehydrate restores the exact bytes.
	if err := rehydrate(context.Background(), store, env); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if !bytes.Equal(env.Payload, big) {
		t.Errorf("rehydrated payload mismatch: got %d bytes", len(env.Payload))
	}
	if env.PayloadRef != "" {
		t.Error("PayloadRef should be cleared after rehydrate")
	}
}

func TestOffloadIfLarge_SmallPayloadStaysInline(t *testing.T) {
	store := newMemStore()
	env := mkEnv([]byte("small"))
	offloaded, err := offloadIfLarge(context.Background(), store, env, 256, slog.Default())
	if err != nil {
		t.Fatalf("offloadIfLarge: %v", err)
	}
	if offloaded {
		t.Fatal("small payload should not be offloaded")
	}
	if env.PayloadRef != "" || string(env.Payload) != "small" {
		t.Errorf("small payload should be untouched: ref=%q payload=%q", env.PayloadRef, env.Payload)
	}
	if store.puts != 0 {
		t.Errorf("store should not be written for small payload, got %d puts", store.puts)
	}
}

func TestOffloadIfLarge_NoStoreKeepsInline(t *testing.T) {
	env := mkEnv(bytes.Repeat([]byte("A"), 1000))
	offloaded, err := offloadIfLarge(context.Background(), nil, env, 256, slog.Default())
	if err != nil {
		t.Fatalf("offloadIfLarge with nil store should not error: %v", err)
	}
	if offloaded {
		t.Fatal("cannot offload without a store")
	}
	if len(env.Payload) != 1000 || env.PayloadRef != "" {
		t.Error("payload should be left inline when no store is configured")
	}
}

func TestRehydrate_RefButNoStoreErrors(t *testing.T) {
	env := &envelope.Envelope{ID: "e", PayloadRef: "spill/t/c/e"}
	err := rehydrate(context.Background(), nil, env)
	if err == nil {
		t.Fatal("expected error when a ref is set but no store is configured")
	}
	if !strings.Contains(err.Error(), "no offload store") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOffloadIfLarge_ThresholdDisabled(t *testing.T) {
	store := newMemStore()
	env := mkEnv(bytes.Repeat([]byte("A"), 1000))
	// inlineMax <= 0 disables offload entirely.
	offloaded, err := offloadIfLarge(context.Background(), store, env, 0, slog.Default())
	if err != nil {
		t.Fatalf("offloadIfLarge: %v", err)
	}
	if offloaded {
		t.Fatal("offload should be disabled when inlineMax <= 0")
	}
}
