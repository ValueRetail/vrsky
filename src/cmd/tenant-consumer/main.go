// Command tenant-consumer bridges data from one tenant's pipeline into
// another's, subject to an approved tenant_data_connection (field filters
// included). It is an SDK Consumer: the runner owns NATS/DB/health/signals/
// shutdown; this binary implements Configure + Run + Stop and uses the control
// plane (command subjects + a per-bridge durable JetStream subscription) via
// the NATS connection the SDK hands it.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "tenant-consumer", &tenantConsumer{}); err != nil {
		slog.Error("tenant-consumer exited", "error", err)
		os.Exit(1)
	}
}
