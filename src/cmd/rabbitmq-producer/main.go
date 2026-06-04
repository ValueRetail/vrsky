// Command rabbitmq-producer publishes each pipeline message to a RabbitMQ
// exchange/queue (AMQP 0-9-1) as a persistent message (delivery_mode=2). SDK
// Producer: the runner owns NATS/JetStream/health/signals/shutdown; this binary
// implements Configure + Deliver.
//
// PR 2 of #78 (RabbitMQ connector): the producer. The consumer shipped in PR 1.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "rabbitmq-producer", &rabbitProducer{}); err != nil {
		slog.Error("rabbitmq-producer exited", "error", err)
		os.Exit(1)
	}
}
