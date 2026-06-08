// Command cloud-storage-producer uploads pipeline messages as objects to a cloud
// object store (Amazon S3, Azure Blob, or GCS — chosen via config), naming each
// object from a configurable key template (e.g. orders/{{.id}}_{{.timestamp}}.json).
// It is an SDK Producer: the runner owns NATS/JetStream/health/signals/shutdown;
// this binary implements Configure + Deliver.
//
// #80 PR 1: the interface + the S3 backend. Azure/GCS land in PR 2; per-bucket
// server-side encryption lands in PR 3.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "cloud-storage-producer", &cloudProducer{}); err != nil {
		slog.Error("cloud-storage-producer exited", "error", err)
		os.Exit(1)
	}
}
