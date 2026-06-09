//go:build integration

// Integration test for the Kafka consumer against a live broker (apache/kafka,
// KRaft single-node, PLAINTEXT). It creates a topic, produces a message, then
// exercises the connector's real consumer-group reader (realReader) + the
// connection-test ping (realKafkaPing). Run:
//
//	docker compose up -d kafka-test
//	KAFKA_TEST_BROKER=localhost:9092 \
//	  go test -tags=integration -run Kafka ./cmd/kafka-consumer/...
//
// Skipped unless KAFKA_TEST_BROKER is set.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

func TestKafka_RoundTrip_Integration(t *testing.T) {
	broker := os.Getenv("KAFKA_TEST_BROKER")
	if broker == "" {
		t.Skip("KAFKA_TEST_BROKER not set; skipping Kafka integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Unique topic + consumer group per run: a fresh topic avoids the
	// "already exists" case (so a CreateTopics error is actionable), and a fresh
	// group reads from the beginning (an existing group's committed offset would
	// skip the produced message).
	stamp := time.Now().UnixNano()
	topic := fmt.Sprintf("vrsky-it-topic-%d", stamp)
	group := fmt.Sprintf("vrsky-it-group-%d", stamp)

	createTopic(t, ctx, broker, topic)

	// Produce one message.
	w := &kafka.Writer{Addr: kafka.TCP(broker), Topic: topic, Balancer: &kafka.LeastBytes{}}
	defer w.Close()
	payload := []byte(`{"hello":"kafka"}`)
	if err := writeWithRetry(ctx, w, kafka.Message{Key: []byte("k1"), Value: payload}); err != nil {
		t.Fatalf("produce: %v", err)
	}

	// --- exercise the connector's real reader ---
	cfg := &KafkaConfig{Brokers: []string{broker}, Topic: topic, ConsumerGroup: group}
	reader, err := realReader(cfg)
	if err != nil {
		t.Fatalf("realReader: %v", err)
	}
	defer reader.Close()

	fctx, fcancel := context.WithTimeout(ctx, 30*time.Second)
	defer fcancel()
	msg, err := reader.Fetch(fctx)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(msg.Value) != string(payload) {
		t.Errorf("Fetch value = %q, want %q", msg.Value, payload)
	}
	if err := msg.commit(ctx); err != nil {
		t.Errorf("commit: %v", err)
	}

	// --- exercise the connection-test ping ---
	parts, err := realKafkaPing(ctx, cfg)
	if err != nil {
		t.Fatalf("realKafkaPing: %v", err)
	}
	if parts < 1 {
		t.Errorf("ping partitions = %d, want >= 1", parts)
	}
}

// createTopic creates the topic via the cluster controller. The topic is unique
// per run, so any CreateTopics error is a real failure (not "already exists").
func createTopic(t *testing.T, ctx context.Context, broker, topic string) {
	t.Helper()
	var conn *kafka.Conn
	var err error
	deadline := time.Now().Add(40 * time.Second)
	for {
		conn, err = (&kafka.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", broker)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial broker %s: %v", broker, err)
		}
		time.Sleep(time.Second)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	ctrlConn, err := kafka.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		t.Fatalf("dial controller: %v", err)
	}
	defer ctrlConn.Close()

	if err := ctrlConn.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("CreateTopics(%q): %v", topic, err)
	}
}

// writeWithRetry retries the produce briefly while leader election settles after
// topic creation.
func writeWithRetry(ctx context.Context, w *kafka.Writer, msg kafka.Message) error {
	deadline := time.Now().Add(30 * time.Second)
	var err error
	for {
		if err = w.WriteMessages(ctx, msg); err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		time.Sleep(time.Second)
	}
}
