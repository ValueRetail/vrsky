// Command sitoo-producer writes pipeline envelopes back into the Sitoo Retail
// Platform via its REST API (JSON, HTTP Basic auth) — e.g. updating warehouse
// stock, prices, or products. Together with sitoo-consumer it gives two-way
// sync (the omnichannel inventory use case).
//
// It is an SDK Producer: the runner owns the durable JetStream subscription and
// hands each envelope to Deliver; this binary implements Configure + Deliver.
// Deliver classifies failures — transient (5xx/network/429) → retry, client
// errors (4xx/bad payload) → poison — so the runner acks/NAKs/DLQs correctly.
//
// Reference: https://developer.sitoo.com/  (REST API).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "sitoo-producer", &sitooProducer{}); err != nil {
		slog.Error("sitoo-producer exited", "error", err)
		os.Exit(1)
	}
}
