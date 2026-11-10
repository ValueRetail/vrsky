// Command nats-consumer reads from a NATS JetStream server that VRSky does not
// run (#814): one durable pull consumer per active connection, explicit ack.
// Each message is published into the pipeline with an envelope id derived from
// its Nats-Msg-Id, and the source message is acked only after VRSky's publish
// was acknowledged, so a crash in between redelivers it and VRSky's JetStream
// drops the repeat. Built for Retailhero's per-tenant partner accounts
// (ValueRetail/Retailhero#2675). SDK Consumer: the runner owns VRSky's own
// NATS, the database, health, signals and shutdown.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "nats-consumer", &natsConsumer{}); err != nil {
		slog.Error("nats-consumer exited", "error", err)
		os.Exit(1)
	}
}
