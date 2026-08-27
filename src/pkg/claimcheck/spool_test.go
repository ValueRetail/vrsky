package claimcheck

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// spoolStore is a minimal in-memory ObjectStore for spool tests.
type spoolStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	deleted []string
}

func newSpoolStore() *spoolStore { return &spoolStore{objects: map[string][]byte{}} }

func (m *spoolStore) List(context.Context, string) ([]objectstore.Object, error) { return nil, nil }
func (m *spoolStore) Get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[key], "", nil
}
func (m *spoolStore) Put(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), body...)
	return nil
}
func (m *spoolStore) GetStream(_ context.Context, key string) (io.ReadCloser, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return io.NopCloser(bytes.NewReader(m.objects[key])), "", nil
}
func (m *spoolStore) PutStream(_ context.Context, key string, body io.Reader, _ string) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = b
	return nil
}
func (m *spoolStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	m.deleted = append(m.deleted, key)
	return nil
}
func (m *spoolStore) Copy(context.Context, string, string) error { return nil }
func (m *spoolStore) Close() error                               { return nil }

func TestSpool_SmallOutputStaysInline(t *testing.T) {
	store := newSpoolStore()
	sp := NewSpool(context.Background(), store, "spill/t/c/x", "application/json", 1024)
	if _, err := sp.Write([]byte(`{"small":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := sp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	res := sp.Result()
	if res.Inline == nil || string(res.Inline) != `{"small":true}` {
		t.Errorf("expected inline result, got %+v", res)
	}
	if len(store.objects) != 0 {
		t.Error("nothing should have been written to the store")
	}
}

func TestSpool_LargeOutputSpillsWithPrefixIntact(t *testing.T) {
	store := newSpoolStore()
	sp := NewSpool(context.Background(), store, "spill/t/c/x", "application/json", 64)

	// Cross the threshold over many small writes: the buffered prefix must be
	// replayed before the later chunks.
	var want bytes.Buffer
	for i := 0; i < 20; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i)}, 10)
		want.Write(chunk)
		if _, err := sp.Write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := sp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	res := sp.Result()
	if res.Inline != nil {
		t.Fatal("200 bytes over a 64-byte threshold must spill")
	}
	if res.Ref != "spill/t/c/x" || res.Size != 200 {
		t.Errorf("result = %+v", res)
	}
	if got := store.objects["spill/t/c/x"]; !bytes.Equal(got, want.Bytes()) {
		t.Errorf("stored %d bytes, want %d; prefix corrupted?", len(got), want.Len())
	}
	if res.Checksum != Checksum(want.Bytes()) {
		t.Errorf("checksum mismatch: %s", res.Checksum)
	}
}

func TestSpool_ThresholdZeroSpillsImmediately(t *testing.T) {
	store := newSpoolStore()
	sp := NewSpool(context.Background(), store, "spill/t/c/x", "", 0)
	if _, err := sp.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := sp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if res := sp.Result(); res.Inline != nil {
		t.Error("threshold 0 must spill from the first byte")
	}
}

func TestSpool_OverflowWithoutStoreErrors(t *testing.T) {
	sp := NewSpool(context.Background(), nil, "spill/t/c/x", "", 4)
	if _, err := sp.Write([]byte("over the threshold")); err == nil {
		t.Fatal("expected an error when overflow has no store to spill to")
	}
}

func TestSpool_AbortDeletesSpilledObject(t *testing.T) {
	store := newSpoolStore()
	sp := NewSpool(context.Background(), store, "spill/t/c/x", "", 4)
	if _, err := sp.Write([]byte("spills immediately over threshold")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sp.Abort(captureLogs(&bytes.Buffer{}))
	if len(store.objects) != 0 {
		t.Error("abort should have deleted the partial object")
	}
	if len(store.deleted) != 1 {
		t.Errorf("expected one delete, got %v", store.deleted)
	}
}

// An empty output must come back as inline-but-empty, not as a nil Inline —
// callers read Inline != nil as "did not spill", so nil would make an empty
// result look like a spill with no reference and publish a broken envelope.
func TestSpool_EmptyOutputIsInlineNotNil(t *testing.T) {
	sp := NewSpool(context.Background(), newSpoolStore(), "spill/t/c/x", "", 1024)
	if err := sp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	res := sp.Result()
	if res.Inline == nil {
		t.Fatal("empty output must be a non-nil empty slice, not nil")
	}
	if len(res.Inline) != 0 || res.Ref != "" {
		t.Errorf("unexpected result: %+v", res)
	}
}
