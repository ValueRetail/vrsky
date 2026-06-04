// Command kafka-producer publishes each pipeline message to a Kafka topic,
// waiting for acks=all (all in-sync replicas). Supports none / SASL-PLAIN /
// SASL-SCRAM-256 / SASL-SCRAM-512 / mTLS auth. SDK Producer: the runner owns
// NATS/JetStream/health/signals/shutdown; this binary implements Configure +
// Deliver.
//
// PR 2 of #77 (Kafka connector): the producer. The consumer shipped in PR 1.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "kafka-producer", &kafkaProducer{}); err != nil {
		slog.Error("kafka-producer exited", "error", err)
		os.Exit(1)
	}
}
