// Command file-consumer watches a directory for files and accepts HTTP
// multipart uploads, publishing file contents into the pipeline. It is an SDK
// Consumer: the runner owns NATS/DB/health/signals/shutdown; this binary
// implements Configure + Run + Stop, subscribes to the connection command
// subjects via the NATS connection the SDK provides, and serves its /upload,
// /events and /sample-data endpoints on the SDK auxiliary HTTP port
// (WORKER_HTTP_PORT, 9200 in compose).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "file-consumer", &fileConsumer{}); err != nil {
		slog.Error("file-consumer exited", "error", err)
		os.Exit(1)
	}
}
