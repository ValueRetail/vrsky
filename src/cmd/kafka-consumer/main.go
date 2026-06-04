// Command kafka-consumer subscribes to a Kafka topic per active connection
// (consumer group) and publishes each message into the pipeline. The group
// offset is committed only after a successful publish, so a crash mid-flight
// re-delivers rather than drops. Supports none / SASL-PLAIN / SASL-SCRAM-256 /
// SASL-SCRAM-512 / mTLS auth. SDK Consumer: the runner owns NATS/DB/health/
// signals/shutdown.
//
// PR 1 of #77 (Kafka connector): the consumer. The producer lands in PR 2.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "kafka-consumer", &kafkaConsumer{}); err != nil {
		slog.Error("kafka-consumer exited", "error", err)
		os.Exit(1)
	}
}
