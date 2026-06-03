// Command api-consumer polls external HTTP APIs on a schedule and publishes the
// responses into the pipeline. It is an SDK Consumer: the runner owns NATS/DB/
// health/signals/shutdown; this binary implements Configure + Run + Stop,
// subscribes to the connection command subjects via the NATS connection the SDK
// provides, and serves its /sample-data endpoint on the SDK auxiliary HTTP port
// (WORKER_HTTP_PORT, 9800 in compose).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "api-consumer", &apiConsumer{}); err != nil {
		slog.Error("api-consumer exited", "error", err)
		os.Exit(1)
	}
}
