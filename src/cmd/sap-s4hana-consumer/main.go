// Command sap-s4hana-consumer ingests data from SAP S/4HANA (Cloud) into the
// pipeline. S/4HANA exposes OData REST APIs (predominantly OData v2 — e.g.
// API_SALES_ORDER_SRV, API_PRODUCT_SRV, API_MATERIAL_STOCK_SRV), authenticated
// either with Basic auth (a Communication User) or OAuth 2.0 client-credentials
// (SAP-hosted authorization server).
//
// Per active connection it polls a configured OData entity set (following the
// server-driven __next / @odata.nextLink $skiptoken pagination) and publishes
// each page. It is an SDK Consumer: the runner owns NATS/DB/health/signals/
// shutdown; this binary subscribes to the command subjects and reads
// per-connection config from the connections table.
//
// Reference: https://api.sap.com/products/SAPS4HANACloud/apis/ODATA
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "sap-s4hana-consumer", &sapConsumer{}); err != nil {
		slog.Error("sap-s4hana-consumer exited", "error", err)
		os.Exit(1)
	}
}
