// Command db-consumer polls external source databases and publishes the rows
// into the pipeline. It is an SDK Consumer: the runner owns NATS/DB/health/
// signals/shutdown; this binary implements Configure + Run + Stop, subscribes
// to the connection command subjects via the NATS connection the SDK provides,
// and serves its /events, /test-connection and /sample-data endpoints on the
// SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9300 in compose).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "db-consumer", &dbConsumer{}); err != nil {
		slog.Error("db-consumer exited", "error", err)
		os.Exit(1)
	}
}
