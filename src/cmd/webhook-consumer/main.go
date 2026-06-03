// Command webhook-consumer receives inbound HTTP webhooks (with optional HMAC
// signature verification, #67) and publishes their bodies into the pipeline. It
// also manages an on-demand cloudflared quick tunnel so local webhooks are
// publicly reachable during development. It is an SDK Consumer: the runner owns
// NATS/DB/health/signals/shutdown; this binary implements Configure + Run +
// Stop, subscribes to the connection command subjects via the NATS connection
// the SDK provides, and serves /webhook, /sample-data and the /tunnel/* control
// endpoints on the SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9100 in compose).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "webhook-consumer", &webhookConsumer{}); err != nil {
		slog.Error("webhook-consumer exited", "error", err)
		os.Exit(1)
	}
}
