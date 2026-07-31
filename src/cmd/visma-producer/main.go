// Command visma-producer writes pipeline envelopes into Visma.net (a Nordic
// cloud ERP) via its REST APIs — e.g. creating customers or sales orders. Auth
// is OAuth 2.0 client-credentials via Visma Connect.
//
// It is an SDK Producer: the runner owns the durable JetStream subscription and
// hands each envelope to Deliver. Deliver classifies failures (transient →
// retry, client/auth errors → poison) so the runner acks/NAKs/DLQs correctly.
//
// Reference: https://docs.vismasoftware.no/vismanetapi/
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "visma-producer", &vismaProducer{}); err != nil {
		slog.Error("visma-producer exited", "error", err)
		os.Exit(1)
	}
}
