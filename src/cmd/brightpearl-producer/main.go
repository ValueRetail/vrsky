// Command brightpearl-producer writes pipeline envelopes into Brightpearl (a
// retail OMS) via its account-scoped REST API — e.g. creating orders or
// adjusting stock. A private/staff app authenticates with two headers
// (brightpearl-app-ref + brightpearl-staff-token).
//
// It is an SDK Producer: the runner owns the durable JetStream subscription and
// hands each envelope to Deliver. Deliver classifies failures (transient →
// retry, client/auth errors → poison) so the runner acks/NAKs/DLQs correctly.
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
	if err := sdk.RunProducer(context.Background(), "brightpearl-producer", &brightpearlProducer{}); err != nil {
		slog.Error("brightpearl-producer exited", "error", err)
		os.Exit(1)
	}
}
