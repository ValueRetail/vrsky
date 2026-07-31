// Command business-central-consumer ingests data from Microsoft Dynamics 365
// Business Central (and LS Central / LS Retail, which runs on BC) into the
// pipeline. BC exposes OData v4 REST APIs (API v2.0: items, customers,
// salesOrders, inventory, …), authenticated with OAuth 2.0 client-credentials
// via Microsoft Entra ID.
//
// Per active connection it polls a configured OData entity (following
// @odata.nextLink pagination) and publishes each page. It is an SDK Consumer:
// the runner owns NATS/DB/health/signals/shutdown; this binary subscribes to
// the command subjects and reads per-connection config from the connections
// table.
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
	if err := sdk.RunConsumer(context.Background(), "business-central-consumer", &bcConsumer{}); err != nil {
		slog.Error("business-central-consumer exited", "error", err)
		os.Exit(1)
	}
}
