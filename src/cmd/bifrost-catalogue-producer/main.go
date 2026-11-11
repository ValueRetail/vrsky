// Command bifrost-catalogue-producer delivers catalogue items to Bifrost's
// catalogue-api through its staged import: begin, items (repeated), commit,
// and abort on any failure (ValueRetail/Bifrost, docs/catalogue-doors.md).
// Nothing a till can see changes until commit, so a failed delivery never
// leaves a half-filled catalogue.
//
// It is an SDK Producer: the runner owns the durable JetStream subscription
// and hands each envelope to Deliver. One envelope is one import (see
// plans/issue-815-bifrost-catalogue-producer.md).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "bifrost-catalogue-producer", &catalogueProducer{}); err != nil {
		slog.Error("bifrost-catalogue-producer exited", "error", err)
		os.Exit(1)
	}
}
