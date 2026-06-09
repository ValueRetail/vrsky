//go:build integration

// Integration test for the RabbitMQ consumer against a live broker
// (rabbitmq:3-management). It declares a queue (durable, as the connector does),
// publishes a message, and consumes it with manual ack — the same AMQP surface
// the connector uses. Run:
//
//	docker compose up -d rabbitmq-test
//	RABBITMQ_TEST_URL=amqp://guest:guest@localhost:5672/ \
//	  go test -tags=integration -run RabbitMQ ./cmd/rabbitmq-consumer/...
//
// Skipped unless RABBITMQ_TEST_URL is set.
package main

import (
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestRabbitMQ_RoundTrip_Integration(t *testing.T) {
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL not set; skipping RabbitMQ integration test")
	}

	// rabbitmq:3-management can take a while to accept connections; retry briefly.
	conn := dialRabbitWithRetry(t, url, 60*time.Second)
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	defer ch.Close()

	const queue = "vrsky-it-queue"
	if _, err := ch.QueueDeclare(queue, true /* durable */, false, false, false, nil); err != nil {
		t.Fatalf("QueueDeclare: %v", err)
	}
	// Clean any leftovers from a previous run so we assert on our own message.
	if _, err := ch.QueuePurge(queue, false); err != nil {
		t.Fatalf("QueuePurge: %v", err)
	}

	payload := []byte(`{"hello":"rabbitmq"}`)
	if err := ch.Publish("", queue, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         payload,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Consume with manual ack (the connector acks only after a successful publish).
	deliveries, err := ch.Consume(queue, "", false /* autoAck */, false, false, false, nil)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}

	select {
	case d := <-deliveries:
		if string(d.Body) != string(payload) {
			t.Errorf("delivery body = %q, want %q", d.Body, payload)
		}
		if err := d.Ack(false); err != nil {
			t.Errorf("Ack: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	_, _ = ch.QueueDelete(queue, false, false, false) // cleanup
}

func dialRabbitWithRetry(t *testing.T, url string, timeout time.Duration) *amqp.Connection {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		conn, err := amqp.Dial(url)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("amqp dial: %v", err)
		}
		time.Sleep(time.Second)
	}
}
