// Command rabbitmq-consumer consumes from a RabbitMQ queue (AMQP 0-9-1) per
// active connection and publishes each message into the pipeline. Deliveries
// are manually acked only after a successful publish, so a crash mid-flight
// re-delivers rather than drops. SDK Consumer: the runner owns NATS/DB/health/
// signals/shutdown.
//
// PR 1 of #78 (RabbitMQ connector): the consumer. The producer lands in PR 2.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "rabbitmq-consumer", &rabbitConsumer{}); err != nil {
		slog.Error("rabbitmq-consumer exited", "error", err)
		os.Exit(1)
	}
}
