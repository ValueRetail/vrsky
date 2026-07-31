// Command business-central-producer writes pipeline envelopes into Microsoft
// Dynamics 365 Business Central (and LS Central) via its OData v4 REST API —
// e.g. creating items, customers, or sales orders. Auth is OAuth 2.0
// client-credentials via Microsoft Entra ID.
//
// It is an SDK Producer: the runner owns the durable JetStream subscription and
// hands each envelope to Deliver. Deliver classifies failures (transient →
// retry, client/auth errors → poison) so the runner acks/NAKs/DLQs correctly.
//
// Reference: https://learn.microsoft.com/dynamics365/business-central/dev-itpro/api-reference/v2.0/
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "business-central-producer", &bcProducer{}); err != nil {
		slog.Error("business-central-producer exited", "error", err)
		os.Exit(1)
	}
}
