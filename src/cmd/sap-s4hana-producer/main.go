// Command sap-s4hana-producer writes pipeline envelopes into SAP S/4HANA (Cloud)
// via its OData REST API — e.g. creating sales orders (a deep insert with items
// under to_Item) or posting goods movements to adjust inventory. Auth is Basic
// (a Communication User) or OAuth 2.0 client-credentials. Writes require an SAP
// CSRF token, fetched via a GET and echoed with the session cookie on the write.
//
// It is an SDK Producer: the runner owns the durable JetStream subscription and
// hands each envelope to Deliver. Deliver classifies failures (transient →
// retry, client/auth errors → poison) so the runner acks/NAKs/DLQs correctly.
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
	if err := sdk.RunProducer(context.Background(), "sap-s4hana-producer", &sapProducer{}); err != nil {
		slog.Error("sap-s4hana-producer exited", "error", err)
		os.Exit(1)
	}
}
