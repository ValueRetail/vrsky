//go:build integration

package io

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func TestNATSOutput_New(t *testing.T) {
	config := []byte(`{"url":"nats://localhost:4222","subject":"test.messages"}`)
	output, err := NewNATSOutput(config)
	if err != nil {
		t.Fatalf("NewNATSOutput() error = %v", err)
	}
	if output == nil {
		t.Error("NewNATSOutput() returned nil")
	}
}

func TestNATSOutput_InvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{
			name:    "missing url",
			config:  `{"subject":"test"}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			config:  `{invalid}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewNATSOutput([]byte(tt.config))
			if (err != nil) != tt.wantErr {
				t.Errorf("NewNATSOutput() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNATSOutput_Integration_PublishesToNATS(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect to NATS to verify it's running
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Skipf("NATS not available: %v", err)
	}
	defer nc.Close()

	// Subscribe to receive published message
	received := make(chan *nats.Msg, 1)
	sub, err := nc.ChanSubscribe("test.output", received)
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Create NATS output
	output, err := NewNATSOutput([]byte(fmt.Sprintf(`{"url":"%s","subject":"test.output"}`, nats.DefaultURL)))
	if err != nil {
		t.Fatalf("NewNATSOutput() error = %v", err)
	}

	// Start output
	go func() {
		_ = output.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Create and write envelope
	env := envelope.New()
	env.ID = "test-123"
	env.Payload = []byte(`{"test":"data"}`)

	err = output.Write(ctx, env)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Wait for message
	select {
	case msg := <-received:
		if msg == nil {
			t.Error("Received nil message")
		}
	case <-ctx.Done():
		t.Error("Timeout waiting for published message")
	}

	output.Close()
}
