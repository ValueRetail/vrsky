//go:build integration

package objectstore

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
)

// assertRoundTrip exercises the full ObjectStore surface against a live backend:
// Put -> List -> Get (with content type) -> Copy -> Delete. Shared by the S3,
// Azure, and GCS integration tests.
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
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
