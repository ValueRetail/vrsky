// Command remote-agent is the gateway for VRSky remote agents (#266): small
// binaries on customer machines that dial out over HTTPS and make that machine
// usable as a pipeline input (watch a directory) or output (write a file).
//
// It is one standing connector service (ADR 0004) serving both directions of
// the remote_agent node type:
//
//   - input: an agent uploads a file; the gateway publishes it into the
//     pipeline through the SDK's publish closure (claim-check included).
//   - output: for each running pipeline that ends in an agent, the gateway owns
//     a per-connection JetStream durable and hands each message to the agent,
//     acknowledging it only once the agent confirms the file is written. While
//     the agent is offline the message simply waits in the stream.
//
// Agents never touch NATS. They speak the HTTP protocol in pkg/agentproto,
// authenticated by a per-agent credential that resolves to exactly one agent in
// exactly one tenant.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "remote-agent", newGateway()); err != nil {
		slog.Error("remote-agent exited", "error", err)
		os.Exit(1)
	}
}
