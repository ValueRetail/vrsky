// Command brightpearl-consumer ingests data from Brightpearl (a retail OMS /
// retail-ops platform) into the pipeline. Brightpearl exposes an account-scoped
// REST API; a private/staff app authenticates with two headers
// (brightpearl-app-ref + brightpearl-staff-token). It supports polling
// (order/product/warehouse search) and webhooks for near-real-time updates.
//
// This connector:
//   - per active connection, polls a configured resource and publishes the
//     response, and
//   - serves POST /brightpearl/events/{connectionID} on the SDK aux HTTP port
//     for Brightpearl webhooks.
//
// It is an SDK Consumer: the runner owns NATS/DB/health/signals/shutdown.
//
// Reference: https://api-docs.brightpearl.com/
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "brightpearl-consumer", &brightpearlConsumer{}); err != nil {
		slog.Error("brightpearl-consumer exited", "error", err)
		os.Exit(1)
	}
}
