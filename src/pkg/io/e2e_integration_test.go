// +build integration

package io_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	iolib "github.com/ValueRetail/vrsky/pkg/io"
)

// TestE2E_ConsumerToProducerPipeline validates the full flow:
// HTTP → Consumer (HTTP Input → NATS Output) → Producer (NATS Input → HTTP Output)
func TestE2E_ConsumerToProducerPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Track received messages
	receivedMessages := make([]map[string]interface{}, 0)
	mockServer := setupMockHTTPServer(t, &receivedMessages)
	defer mockServer.Close()

	// 1. Create Consumer (HTTP Input → NATS Output)
	consumerInput, err := iolib.NewHTTPInput([]byte(`{"port":8800}`))
	if err != nil {
		t.Fatalf("Failed to create consumer HTTP input: %v", err)
	}

	consumerOutputConfig := `{"url":"nats://localhost:4222","subject":"test.e2e.messages"}`
	consumerOutput, err := iolib.NewNATSOutput([]byte(consumerOutputConfig))
	if err != nil {
		t.Fatalf("Failed to create consumer NATS output: %v", err)
	}

	// 2. Create Producer (NATS Input → HTTP Output)
	producerInputConfig := `{"url":"nats://localhost:4222","topic":"test.e2e.messages"}`
	producerInput, err := iolib.NewNATSInput([]byte(producerInputConfig))
	if err != nil {
		t.Fatalf("Failed to create producer NATS input: %v", err)
	}

	producerOutputConfig := `{"url":"http://localhost:9800/webhook","method":"POST"}`
	producerOutput, err := iolib.NewHTTPOutput([]byte(producerOutputConfig))
	if err != nil {
		t.Fatalf("Failed to create producer HTTP output: %v", err)
	}

	// 3. Start all components
	if err := consumerInput.Start(ctx); err != nil {
		t.Fatalf("Failed to start consumer input: %v", err)
	}
	defer consumerInput.Close()

	if err := consumerOutput.Start(ctx); err != nil {
		t.Fatalf("Failed to start consumer output: %v", err)
	}
	defer consumerOutput.Close()

	if err := producerInput.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer input: %v", err)
	}
	defer producerInput.Close()

	if err := producerOutput.Start(ctx); err != nil {
		t.Fatalf("Failed to start producer output: %v", err)
	}
	defer producerOutput.Close()

	// Give services time to initialize
	time.Sleep(200 * time.Millisecond)

	// 4. Start Consumer processor (reads from HTTP, writes to NATS)
	consumerDone := make(chan error, 1)
	go func() {
		env, err := consumerInput.Read(ctx)
		if err != nil {
			consumerDone <- err
			return
		}
		if env == nil {
			consumerDone <- fmt.Errorf("envelope is nil")
			return
		}
		consumerDone <- consumerOutput.Write(ctx, env)
	}()

	// 5. Start Producer processor (reads from NATS, writes to HTTP)
	producerDone := make(chan error, 1)
	go func() {
		env, err := producerInput.Read(ctx)
		if err != nil {
			producerDone <- err
			return
		}
		if env == nil {
			producerDone <- fmt.Errorf("envelope is nil")
			return
		}
		producerDone <- producerOutput.Write(ctx, env)
	}()

	// 6. Send webhook to consumer HTTP endpoint
	time.Sleep(100 * time.Millisecond) // Wait for consumer to be ready

	testPayload := map[string]interface{}{
		"order_id": "e2e-test-123",
		"status":   "completed",
		"items":    []string{"widget", "gadget"},
	}
	payloadJSON, _ := json.Marshal(testPayload)

	resp, err := http.Post(
		"http://localhost:8800/webhook",
		"application/json",
		bytes.NewReader(payloadJSON),
	)
	if err != nil {
		t.Fatalf("Failed to send webhook: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("Expected 202 Accepted, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 7. Wait for consumer to process and publish to NATS
	select {
	case err := <-consumerDone:
		if err != nil {
			t.Fatalf("Consumer error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Consumer timeout - failed to process webhook")
	}

	// 8. Wait for producer to receive from NATS and send to HTTP
	select {
	case err := <-producerDone:
		if err != nil {
			t.Fatalf("Producer error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Producer timeout - failed to receive from NATS or send to HTTP")
	}

	// 9. Verify message reached HTTP endpoint
	if len(receivedMessages) == 0 {
		t.Fatal("No messages received at HTTP endpoint")
	}

	// 10. Verify payload content
	receivedMsg := receivedMessages[0]

	// The producer will send the entire envelope, so we need to check its structure
	if receivedMsg["id"] == nil {
		t.Errorf("Envelope missing 'id' field")
	}

	if receivedMsg["payload"] == nil {
		t.Errorf("Envelope missing 'payload' field")
	}

	// Decode the original payload from the envelope
	if payloadB64, ok := receivedMsg["payload"].(string); ok {
		// Payload is base64 encoded in JSON
		t.Logf("Received envelope with payload: %s", payloadB64)
	}

	t.Log("✅ Full E2E pipeline test passed: HTTP → NATS → HTTP")
}

// setupMockHTTPServer creates a simple HTTP server that logs POST requests
func setupMockHTTPServer(t *testing.T, messages *[]map[string]interface{}) *http.Server {
	server := &http.Server{Addr: ":9800"}

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var msg map[string]interface{}
		if err := json.Unmarshal(body, &msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		*messages = append(*messages, msg)
		t.Logf("Mock HTTP server received: %v", msg)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("Mock server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	return server
}
