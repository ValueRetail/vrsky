// Command cloud-storage-consumer watches a cloud object-storage bucket per
// active connection (Amazon S3, Azure Blob, or GCS — chosen via config), fetches
// new objects, publishes their contents into the pipeline, and applies an
// after-action (delete / move / leave). It is an SDK Consumer: the runner owns
// NATS/DB/health/signals/shutdown; this binary subscribes to the connection
// command subjects and drives a poller per active connection.
//
// #80 PR 1: the interface + the S3 backend in poll mode. Azure/GCS backends land
// in PR 2; event-driven ingestion (SQS/Queue/PubSub) lands in PR 3.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "cloud-storage-consumer", &cloudConsumer{}); err != nil {
		slog.Error("cloud-storage-consumer exited", "error", err)
		os.Exit(1)
	}
}
