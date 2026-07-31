// Command front-systems-consumer ingests data from Front Systems (a Nordic
// mobile/omnichannel POS, part of EG) into the pipeline. Front Systems is
// webhook-first: it pushes events (SaleCreated, StockMovementCreated,
// DeliveryItemsReceived, …) to a registered callback URL.
//
// This connector:
//   - serves POST /frontsystems/events/{connectionID} on the SDK aux HTTP port
//     to receive those events (returning 2xx as Front Systems requires), and
//   - on connection-start, optionally registers its callback URL for the
//     configured event types via POST /api/webhooks.
//
// It is an SDK Consumer: the runner owns NATS/DB/health/signals/shutdown; this
// binary implements Configure + Run + Stop and reads per-connection config from
// the connections table.
//
// Auth is two headers (Azure APIM): Ocp-Apim-Subscription-Key + x-api-key.
// Reference: https://developer.frontsystems.com/guides/webhooks
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "front-systems-consumer", &frontSystemsConsumer{}); err != nil {
		slog.Error("front-systems-consumer exited", "error", err)
		os.Exit(1)
	}
}
