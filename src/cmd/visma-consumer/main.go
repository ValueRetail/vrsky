// Command visma-consumer ingests data from Visma.net (a Nordic cloud ERP) into
// the pipeline. Visma.net exposes REST APIs authenticated with OAuth 2.0
// client-credentials via Visma Connect. Unlike Business Central it is
// multi-service: each API has its own host (e.g. https://salesorder.visma.net,
// the Financials API host, …), so base_url is required per connection, and the
// company context is passed via the ipp-company-id header.
//
// Per active connection it polls a configured resource and publishes the result.
// It is an SDK Consumer: the runner owns NATS/DB/health/signals/shutdown.
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
	if err := sdk.RunConsumer(context.Background(), "visma-consumer", &vismaConsumer{}); err != nil {
		slog.Error("visma-consumer exited", "error", err)
		os.Exit(1)
	}
}
