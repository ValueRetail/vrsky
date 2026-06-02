// Command salesforce-consumer ingests Salesforce records into the pipeline by
// running a SOQL query against the org's REST API, authenticated with an OAuth
// grant (#75). It is an SDK Consumer: the runner owns NATS/DB/health/signals/
// shutdown; this binary subscribes to the connection command subjects, and for
// each active connection polls Salesforce and publishes the records.
//
// PR 1 of #79 (Salesforce connector): SOQL poll only. CDC / platform events and
// the REST/Bulk producer come in later PRs.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "salesforce-consumer", &salesforceConsumer{}); err != nil {
		slog.Error("salesforce-consumer exited", "error", err)
		os.Exit(1)
	}
}
