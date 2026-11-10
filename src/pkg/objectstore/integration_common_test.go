//go:build integration

package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"testing/iotest"
	"time"
)

// assertRoundTrip exercises the full ObjectStore surface against a live backend:
// Put -> List -> Get (with content type) -> Copy -> Delete, then a streamed
// round-trip and a streamed upload whose body fails. Shared by the S3, Azure,
// and GCS integration tests.
func assertRoundTrip(t *testing.T, ctx context.Context, store ObjectStore) {
	t.Helper()

	key := "in/order-1.json"
	payload := []byte(`{"id":1,"name":"Acme"}`)
	if err := store.Put(ctx, key, payload, "application/json"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	objs, err := store.List(ctx, "in/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, o := range objs {
		if o.Key == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("List(in/) missing %q: %+v", key, objs)
	}

	// Stat (#672): size, modification time and a version, the same version
	// List reports.
	st, err := store.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Key != key || st.Size != int64(len(payload)) || st.LastModified.IsZero() || st.Version == "" {
		t.Errorf("Stat = %+v, want key %q, size %d, a modification time and a version", st, key, len(payload))
	}
	for _, o := range objs {
		if o.Key == key && o.Version != st.Version {
			t.Errorf("List version %q, Stat version %q", o.Version, st.Version)
		}
	}
	assertStatVersionChanges(t, ctx, store)

	body, ct, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != string(payload) {
		t.Errorf("Get body = %q, want %q", body, payload)
	}
	if ct != "application/json" {
		t.Errorf("Get content-type = %q, want application/json", ct)
	}

	// Copy then delete the original (after_action=move semantics).
	dst := "in/processed/order-1.json"
	if err := store.Copy(ctx, key, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Get(ctx, dst); err != nil {
		t.Fatalf("Get after copy: %v", err)
	}
	if _, _, err := store.Get(ctx, key); err == nil {
		t.Error("Get of deleted key should fail")
	}
	if _, err := store.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat of deleted key = %v, want ErrNotFound", err)
	}

	_ = store.Delete(ctx, dst) // cleanup

	// Streaming round-trip: PutStream -> GetStream. Exercises the provider's
	// native multipart/chunked upload and streamed download (the multi-GB path),
	// here with a small payload so the test stays fast.
	skey := "in/stream-1.bin"
	spayload := []byte("streamed-content-\x00\x01\x02-payload")
	if err := store.PutStream(ctx, skey, bytes.NewReader(spayload), "application/octet-stream"); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	rc, ct, err := store.GetStream(ctx, skey)
	if err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("GetStream read: %v", err)
	}
	if !bytes.Equal(got, spayload) {
		t.Errorf("GetStream body = %q, want %q", got, spayload)
	}
	if ct != "application/octet-stream" {
		t.Errorf("GetStream content-type = %q, want application/octet-stream", ct)
	}
	_ = store.Delete(ctx, skey) // cleanup

	assertFailedPutStreamLeavesNothing(t, ctx, store)
}

var errIntegrationBody = errors.New("body failed partway")

// assertFailedPutStreamLeavesNothing streams bodies that fail partway and
// checks the backend kept nothing: PutStream errors, List doesn't show the key
// and Get fails. A cut-off backup archive must never look like a real one.
// The larger body gets past GCS's first 16 MiB resumable chunk and S3's first
// 5 MiB part, so the abort runs after data reached the server.
func assertFailedPutStreamLeavesNothing(t *testing.T, ctx context.Context, store ObjectStore) {
	t.Helper()
	for _, sent := range []int{64 << 10, 17 << 20} {
		// A fresh key per run: emulator buckets outlive the test.
		key := fmt.Sprintf("in/failed-stream-%d-%d.bin", sent, time.Now().UnixNano())
		body := io.MultiReader(bytes.NewReader(make([]byte, sent)), iotest.ErrReader(errIntegrationBody))

		err := store.PutStream(ctx, key, body, "application/octet-stream")
		if err == nil {
			t.Errorf("PutStream of a body failing after %d bytes returned nil", sent)
		}

		objs, lerr := store.List(ctx, "in/")
		if lerr != nil {
			t.Fatalf("List: %v", lerr)
		}
		for _, o := range objs {
			if o.Key == key {
				t.Errorf("failed PutStream left %q (%d bytes) in List", key, o.Size)
			}
		}
		if _, _, gerr := store.Get(ctx, key); gerr == nil {
			t.Errorf("Get(%q) succeeded after a failed PutStream", key)
		}
		_ = store.Delete(ctx, key) // cleanup if it was committed anyway
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// assertStatVersionChanges writes a key twice and checks that Stat's version
// tells the two apart (#672).
func assertStatVersionChanges(t *testing.T, ctx context.Context, store ObjectStore) {
	t.Helper()
	key := "in/stat-1.txt"
	defer func() { _ = store.Delete(ctx, key) }()
	if err := store.Put(ctx, key, []byte("first"), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a, err := store.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if err := store.Put(ctx, key, []byte("other"), "text/plain"); err != nil {
		t.Fatalf("Put again: %v", err)
	}
	b, err := store.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat again: %v", err)
	}
	if a.Version == b.Version {
		t.Errorf("Stat version %q unchanged after the object was written again", a.Version)
	}
}

// condSupport says which conditions a test store honours. Where one is
// ignored the request still works, unconditionally; the consumer's Stat check
// is the guard then. The real providers honour both.
type condSupport struct {
	// copy: MinIO and Azurite refuse a stale CopyIf.
	copy bool
	// delete: Azurite refuses a stale DeleteIf; MinIO's DeleteObject accepts
	// If-Match and deletes anyway.
	delete bool
	// fake-gcs-server ignores ifGenerationMatch and ifSourceGenerationMatch
	// alike (checked against its request log, #718): it honours neither.
}

// assertConditional checks DeleteIf and CopyIf (#718) against a live backend:
// the version Stat returns works, an older one is refused with
// ErrVersionChanged where the store honours the condition.
func assertConditional(t *testing.T, ctx context.Context, store ObjectStore, honours condSupport) {
	t.Helper()
	c, ok := store.(Conditional)
	if !ok {
		t.Fatal("store does not implement Conditional")
	}
	const key, dst = "cond/a.txt", "cond/done/a.txt"
	version := func() string {
		t.Helper()
		obj, err := store.Stat(ctx, key)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if obj.Version == "" {
			t.Fatal("Stat returned no version")
		}
		return obj.Version
	}
	if err := store.Put(ctx, key, []byte("one"), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	v1 := version()
	if err := store.Put(ctx, key, []byte("two!"), "text/plain"); err != nil {
		t.Fatalf("Put again: %v", err)
	}
	v2 := version()
	if v1 == v2 {
		t.Fatalf("version %q did not change when the object was written again", v1)
	}

	err := c.CopyIf(ctx, key, dst, v1)
	switch {
	case honours.copy && !errors.Is(err, ErrVersionChanged):
		t.Fatalf("CopyIf at the old version = %v, want ErrVersionChanged", err)
	case honours.copy:
		if _, err := store.Stat(ctx, dst); !errors.Is(err, ErrNotFound) {
			t.Fatalf("refused copy left %s (Stat: %v)", dst, err)
		}
	default:
		t.Logf("CopyIf at the old version on a store that ignores it: %v", err)
	}
	if err := c.CopyIf(ctx, key, dst, v2); err != nil {
		t.Fatalf("CopyIf at the current version: %v", err)
	}
	if got, _, err := store.Get(ctx, dst); err != nil || string(got) != "two!" {
		t.Fatalf("copied %q, %v; want the current object", got, err)
	}

	err = c.DeleteIf(ctx, key, v1)
	switch {
	case honours.delete && !errors.Is(err, ErrVersionChanged):
		t.Fatalf("DeleteIf at the old version = %v, want ErrVersionChanged", err)
	case honours.delete:
		if version() != v2 {
			t.Fatal("refused delete changed the object")
		}
	default:
		_, statErr := store.Stat(ctx, key)
		t.Logf("DeleteIf at the old version on a store that ignores it: %v; object gone: %v", err, errors.Is(statErr, ErrNotFound))
		if errors.Is(statErr, ErrNotFound) {
			if err := store.Put(ctx, key, []byte("two!"), "text/plain"); err != nil {
				t.Fatalf("Put back: %v", err)
			}
		}
	}
	if err := c.DeleteIf(ctx, key, version()); err != nil {
		t.Fatalf("DeleteIf at the current version: %v", err)
	}
	if _, err := store.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("object still there after DeleteIf (Stat: %v)", err)
	}
	if err := store.Delete(ctx, dst); err != nil {
		t.Fatalf("Delete %s: %v", dst, err)
	}
}
