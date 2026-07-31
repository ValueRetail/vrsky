// Command sitoo-consumer ingests data from the Sitoo Retail Platform into the
// pipeline. Sitoo exposes a public REST API (JSON, HTTP Basic auth) plus SPI
// Event webhooks; this connector supports both:
//
//   - Poll mode: for each active connection it periodically fetches a Sitoo
//     collection (transactions/orders, warehouse stock, products, …) with
//     start/num pagination and publishes each page as an envelope.
//   - Webhook mode: it serves POST /sitoo/events/{connectionID} on the SDK
//     auxiliary HTTP port so Sitoo's SPI Events (Orders, Warehouse
//     Transactions, …) push in real time.
//
// It is an SDK Consumer: the runner owns NATS/DB/health/signals/shutdown; this
// binary implements Configure + Run + Stop, subscribes to the connection
// command subjects, and reads per-connection config from the connections table.
//
// Reference: https://developer.sitoo.com/  (REST API + SPI Events).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "sitoo-consumer", &sitooConsumer{}); err != nil {
		slog.Error("sitoo-consumer exited", "error", err)
		os.Exit(1)
	}
}
