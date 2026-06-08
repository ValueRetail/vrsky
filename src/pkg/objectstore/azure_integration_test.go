//go:build integration

// Integration test for the Azure Blob backend against the Azurite emulator. Run:
//
//	docker compose up -d azurite-test
//	AZURE_TEST_CONN="DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;" \
//	  go test -tags=integration -run Azure ./pkg/objectstore/...
//
// Skipped unless AZURE_TEST_CONN is set.
package objectstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

func TestAzure_RoundTrip_Integration(t *testing.T) {
	conn := os.Getenv("AZURE_TEST_CONN")
	if conn == "" {
		t.Skip("AZURE_TEST_CONN not set; skipping Azure integration test")
	}
	const container = "objectstore-it"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Ensure the container exists.
	client, err := azblob.NewClientFromConnectionString(conn, nil)
	if err != nil {
		t.Fatalf("azblob client: %v", err)
	}
	_, _ = client.CreateContainer(ctx, container, nil)

	store, err := New(ctx, &Config{
		Provider:         ProviderAzure,
		Bucket:           container,
		ConnectionString: conn,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	assertRoundTrip(t, ctx, store)
}
