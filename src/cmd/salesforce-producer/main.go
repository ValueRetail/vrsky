// Command salesforce-producer writes pipeline records into Salesforce, using
// the REST sObject API for small batches and Bulk API 2.0 for batches ≥ 200
// (#79 PR 2). Authenticated with an OAuth grant (#75). It is an SDK Producer:
// the runner owns NATS/JetStream/health/signals/shutdown; this binary
// implements Configure + Deliver.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "salesforce-producer", &salesforceProducer{}); err != nil {
		slog.Error("salesforce-producer exited", "error", err)
		os.Exit(1)
	}
}
