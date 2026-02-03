package io

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestHTTPInput_NewHTTPInput(t *testing.T) {
	config := []byte(`{"port":"8080"}`)
	input, err := NewHTTPInput(config)
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}
	if input == nil {
		t.Error("NewHTTPInput() returned nil")
	}
}

func TestHTTPInput_Start_Close(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	input, err := NewHTTPInput([]byte(`{"port":"8765"}`))
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}

	// Start should not block
	go func() {
		_ = input.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Close should work
	err = input.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestHTTPInput_ReceiveWebhook(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	input, err := NewHTTPInput([]byte(`{"port":"8766"}`))
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}

	go func() {
		_ = input.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Send a test webhook
	payload := map[string]string{"test": "data"}
	payloadBytes, _ := json.Marshal(payload)

	resp, err := http.Post(
		"http://localhost:8766/webhook",
		"application/json",
		bytes.NewReader(payloadBytes),
	)
	if err != nil {
		t.Fatalf("Failed to send webhook: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("Expected status %d, got %d", http.StatusAccepted, resp.StatusCode)
	}

	input.Close()
}

func TestHTTPInput_Read_ReturnsEnvelope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	input, err := NewHTTPInput([]byte(`{"port":"8767"}`))
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}

	go func() {
		_ = input.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Send a webhook
	payload := []byte(`{"test":"message"}`)
	resp, err := http.Post(
		"http://localhost:8767/webhook",
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		t.Fatalf("Failed to send webhook: %v", err)
	}
	resp.Body.Close()

	// Read should return an envelope
	env, err := input.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if env == nil {
		t.Error("Read() returned nil envelope")
	}
	if env.ID == "" {
		t.Error("Envelope ID is empty")
	}
	if len(env.Payload) == 0 {
		t.Error("Envelope payload is empty")
	}

	input.Close()
}

func TestHTTPInput_ParsesPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	input, err := NewHTTPInput([]byte(`{"port":"8768"}`))
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}

	go func() {
		_ = input.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Send a webhook with specific data
	testData := map[string]interface{}{
		"message": "hello",
		"value":   42,
	}
	payloadBytes, _ := json.Marshal(testData)

	resp, err := http.Post(
		"http://localhost:8768/webhook",
		"application/json",
		bytes.NewReader(payloadBytes),
	)
	if err != nil {
		t.Fatalf("Failed to send webhook: %v", err)
	}
	resp.Body.Close()

	// Read envelope
	env, err := input.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	// Verify payload was preserved
	var received map[string]interface{}
	err = json.Unmarshal(env.Payload, &received)
	if err != nil {
		t.Fatalf("Failed to unmarshal payload: %v", err)
	}

	if received["message"] != "hello" {
		t.Errorf("Expected message='hello', got %v", received["message"])
	}
	if int(received["value"].(float64)) != 42 {
		t.Errorf("Expected value=42, got %v", received["value"])
	}

	input.Close()
}

func TestHTTPInput_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	input, err := NewHTTPInput([]byte(`{"port":"8769"}`))
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}

	errChan := make(chan error)
	go func() {
		errChan <- input.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for Start to return
	select {
	case <-errChan:
		// Expected behavior
	case <-time.After(2 * time.Second):
		t.Error("Start() did not return after context cancellation")
	}

	input.Close()
}
