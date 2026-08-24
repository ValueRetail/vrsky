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
	ctByKey map[string]string // optional per-key content type
	puts    int
	gets    int
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
	m.gets++
	b, ok := m.objects[key]
	if !ok {
		return nil, "", io.ErrUnexpectedEOF
	}
	return io.NopCloser(bytes.NewReader(b)), m.ctByKey[key], nil
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
	if err := rehydrate(context.Background(), store, env, defaultRehydrateMaxBytes); err != nil {
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
	err := rehydrate(context.Background(), nil, env, defaultRehydrateMaxBytes)
	if err == nil {
		t.Fatal("expected error when a ref is set but no store is configured")
	}
	if !strings.Contains(err.Error(), "no offload store") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRehydrate_FillsContentTypeFromStore(t *testing.T) {
	store := newMemStore()
	// Store an object under a ref; envelope has no ContentType of its own.
	const ref = "spill/tenant-x/conn-1/env-1"
	store.ctByKey = map[string]string{ref: "text/csv"}
	store.objects[ref] = []byte("a,b\n1,2\n")
	env := &envelope.Envelope{ID: "env-1", PayloadRef: ref}

	if err := rehydrate(context.Background(), store, env, defaultRehydrateMaxBytes); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if env.ContentType != "text/csv" {
		t.Errorf("ContentType = %q, want text/csv (from store)", env.ContentType)
	}
}

func TestOffload_StampsChecksum(t *testing.T) {
	store := newMemStore()
	payload := bytes.Repeat([]byte("A"), 1000)
	env := mkEnv(payload)
	if _, err := offloadIfLarge(context.Background(), store, env, 256, slog.Default()); err != nil {
		t.Fatalf("offloadIfLarge: %v", err)
	}
	want := payloadChecksum(payload)
	if env.Checksum != want {
		t.Errorf("Checksum = %q, want %q", env.Checksum, want)
	}
	if !strings.HasPrefix(env.Checksum, "sha256:") {
		t.Errorf("checksum should be sha256-prefixed, got %q", env.Checksum)
	}
}

func TestRehydrate_RejectsOverCapBeforeDownload(t *testing.T) {
	store := newMemStore()
	env := mkEnv(bytes.Repeat([]byte("A"), 1000))
	if _, err := offloadIfLarge(context.Background(), store, env, 256, slog.Default()); err != nil {
		t.Fatalf("offloadIfLarge: %v", err)
	}
	// Cap below the payload size → rejected without reading the object.
	before := store.gets
	err := rehydrate(context.Background(), store, env, 500)
	if err == nil {
		t.Fatal("expected an over-cap rehydrate to fail")
	}
	if !strings.Contains(err.Error(), "rehydrate cap") {
		t.Errorf("error should explain the cap, got: %v", err)
	}
	if store.gets != before {
		t.Errorf("over-cap payload should be rejected without downloading (gets went %d→%d)", before, store.gets)
	}
	// The envelope must stay intact so the DLQ entry still points at the object.
	if env.PayloadRef == "" {
		t.Error("PayloadRef should be preserved on rejection")
	}
}

func TestRehydrate_CapDisabledAllowsAnySize(t *testing.T) {
	store := newMemStore()
	env := mkEnv(bytes.Repeat([]byte("A"), 1000))
	if _, err := offloadIfLarge(context.Background(), store, env, 256, slog.Default()); err != nil {
		t.Fatalf("offloadIfLarge: %v", err)
	}
	if err := rehydrate(context.Background(), store, env, 0); err != nil {
		t.Fatalf("cap<=0 disables the limit, got: %v", err)
	}
	if len(env.Payload) != 1000 {
		t.Errorf("payload not restored: %d bytes", len(env.Payload))
	}
}

func TestRehydrate_BoundsReadWhenPayloadSizeUnderstatesObject(t *testing.T) {
	store := newMemStore()
	const ref = "spill/tenant-x/conn-1/env-1"
	store.objects[ref] = bytes.Repeat([]byte("A"), 1000)
	// Envelope lies: claims 10 bytes, object is 1000. The cap must still hold.
	env := &envelope.Envelope{ID: "env-1", PayloadRef: ref, PayloadSize: 10}

	err := rehydrate(context.Background(), store, env, 500)
	if err == nil {
		t.Fatal("expected the read-side bound to reject an understated payload")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRehydrate_ChecksumMismatchIsDetected(t *testing.T) {
	store := newMemStore()
	env := mkEnv(bytes.Repeat([]byte("A"), 1000))
	if _, err := offloadIfLarge(context.Background(), store, env, 256, slog.Default()); err != nil {
		t.Fatalf("offloadIfLarge: %v", err)
	}
	// Corrupt the stored object behind the envelope's back.
	store.objects[env.PayloadRef] = bytes.Repeat([]byte("B"), 1000)

	err := rehydrate(context.Background(), store, env, defaultRehydrateMaxBytes)
	if err == nil {
		t.Fatal("expected a checksum mismatch to be detected")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRehydrate_NoChecksumStillWorks(t *testing.T) {
	// Envelopes published before checksums existed (or by an older worker during
	// a rollout) carry no checksum — they must still rehydrate.
	store := newMemStore()
	const ref = "spill/tenant-x/conn-1/legacy"
	store.objects[ref] = []byte("legacy-payload")
	env := &envelope.Envelope{ID: "legacy", PayloadRef: ref, PayloadSize: 14}

	if err := rehydrate(context.Background(), store, env, defaultRehydrateMaxBytes); err != nil {
		t.Fatalf("rehydrate without checksum should succeed: %v", err)
	}
	if string(env.Payload) != "legacy-payload" {
		t.Errorf("payload = %q", env.Payload)
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
