//go:build integration

// Integration test for the GCS backend against the fake-gcs-server emulator. The
// Go GCS client talks to the emulator via the STORAGE_EMULATOR_HOST env var
// (which routes every operation — including media reads — to the emulator;
// option.WithEndpoint alone does not cover the read path). Run:
//
//	docker compose up -d fake-gcs-test
//	GCS_TEST_EMULATOR_HOST=localhost:4443 go test -tags=integration -run GCS ./pkg/objectstore/...
//
// Skipped unless GCS_TEST_EMULATOR_HOST is set.
package objectstore

import (
	"context"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/storage"
)

func TestGCS_RoundTrip_Integration(t *testing.T) {
	host := os.Getenv("GCS_TEST_EMULATOR_HOST")
	if host == "" {
		t.Skip("GCS_TEST_EMULATOR_HOST not set; skipping GCS integration test")
	}
	// STORAGE_EMULATOR_HOST puts the client in emulator mode (no auth, all ops
	// routed to the emulator). The backend's New() then needs no endpoint/creds.
	t.Setenv("STORAGE_EMULATOR_HOST", host)
	const bucket = "objectstore-it"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Ensure the bucket exists on the emulator.
	client, err := storage.NewClient(ctx)
	if err != nil {
		t.Fatalf("gcs client: %v", err)
	}
	_ = client.Bucket(bucket).Create(ctx, "test-project", nil)

	store, err := New(ctx, &Config{Provider: ProviderGCS, Bucket: bucket})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	assertRoundTrip(t, ctx, store)
}
