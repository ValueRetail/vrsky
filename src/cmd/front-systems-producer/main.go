// Command front-systems-producer writes pipeline envelopes into Front Systems
// via its REST API — master data such as products (/api/Products,
// /api/Products/bulk-upsert) and prices (/api/PriceListV2). Together with
// front-systems-consumer it gives two-way sync.
//
// It is an SDK Producer: the runner owns the durable JetStream subscription and
// hands each envelope to Deliver. Deliver classifies failures (transient →
// retry, client/auth errors → poison) so the runner acks/NAKs/DLQs correctly.
//
// Auth is two headers (Azure APIM): Ocp-Apim-Subscription-Key + x-api-key.
// Reference: https://developer.frontsystems.com/guides/masterdata
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "front-systems-producer", &frontSystemsProducer{}); err != nil {
		slog.Error("front-systems-producer exited", "error", err)
		os.Exit(1)
	}
}
