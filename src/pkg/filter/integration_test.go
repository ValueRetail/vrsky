//go:build integration
// +build integration

package filter

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func setupNATS(t *testing.T) *nats.Conn {
	// Connect to NATS (assumes NATS is running on localhost:4222)
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Skipf("NATS not available: %v", err)
	}
	return nc
}

func TestIntegration_FilterAcceptsMessage(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_filter",
		InputTopic:     "test.input",
		OutputTopic:    "test.output",
		RejectionTopic: "test.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_active",
				"condition": map[interface{}]interface{}{
					"operator": "==",
					"field":    "status",
					"value":    "active",
				},
			},
		},
	}

	filter, err := NewFilter("test_filter", config, nc, logger, nil)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to output topic to verify message
	outputReceived := make(chan []byte, 1)
	sub, err := nc.Subscribe(config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe error = %v", err)
	}
	defer sub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Publish test message
	testEnv := envelope.New()
	testEnv.ID = "test_msg_1"
	testEnv.Payload = []byte(`{"status":"active"}`)
	testEnv.ContentType = "application/json"

	data, err := envelope.Marshal(testEnv)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}

	err = nc.Publish(config.InputTopic, data)
	if err != nil {
		t.Fatalf("Publish error = %v", err)
	}

	// Wait for message on output topic
	select {
	case <-outputReceived:
		// Message received as expected
	case <-time.After(2 * time.Second):
		t.Errorf("Timeout waiting for output message")
	}
}

func TestIntegration_FilterRejectsMessage(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_filter",
		InputTopic:     "test.input2",
		OutputTopic:    "test.output2",
		RejectionTopic: "test.rejection2",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_active",
				"condition": map[interface{}]interface{}{
					"operator": "==",
					"field":    "status",
					"value":    "active",
				},
			},
		},
	}

	filter, err := NewFilter("test_filter", config, nc, logger, nil)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to rejection topic
	rejectionReceived := make(chan []byte, 1)
	sub, err := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		rejectionReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe error = %v", err)
	}
	defer sub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Publish test message with inactive status
	testEnv := envelope.New()
	testEnv.ID = "test_msg_2"
	testEnv.Payload = []byte(`{"status":"inactive"}`)
	testEnv.ContentType = "application/json"

	data, err := envelope.Marshal(testEnv)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}

	err = nc.Publish(config.InputTopic, data)
	if err != nil {
		t.Fatalf("Publish error = %v", err)
	}

	// Wait for message on rejection topic
	select {
	case <-rejectionReceived:
		// Message rejected as expected
	case <-time.After(2 * time.Second):
		t.Errorf("Timeout waiting for rejection message")
	}
}

func TestIntegration_FilterWithComplexCondition(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_filter",
		InputTopic:     "test.input3",
		OutputTopic:    "test.output3",
		RejectionTopic: "test.rejection3",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_high_value_orders",
				"condition": map[interface{}]interface{}{
					"operator": ">",
					"field":    "order.total",
					"value":    100,
				},
			},
		},
	}

	filter, err := NewFilter("test_filter", config, nc, logger, nil)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	outputReceived := make(chan []byte, 1)
	sub, err := nc.Subscribe(config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe error = %v", err)
	}
	defer sub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Publish test message with nested field
	testEnv := envelope.New()
	testEnv.ID = "test_msg_3"
	payload := map[string]interface{}{
		"order": map[string]interface{}{
			"total": 150,
		},
	}
	payloadBytes, _ := json.Marshal(payload)
	testEnv.Payload = payloadBytes
	testEnv.ContentType = "application/json"

	data, err := envelope.Marshal(testEnv)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}

	err = nc.Publish(config.InputTopic, data)
	if err != nil {
		t.Fatalf("Publish error = %v", err)
	}

	// Wait for message on output topic
	select {
	case <-outputReceived:
		// Message accepted as expected
	case <-time.After(2 * time.Second):
		t.Errorf("Timeout waiting for output message")
	}
}

func TestIntegration_MultipleFilters(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()

	// Create two filters in sequence
	filter1Config := &Config{
		FilterID:       "filter1",
		InputTopic:     "test.input4",
		OutputTopic:    "test.intermediate",
		RejectionTopic: "test.rejection4",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "first_rule",
				"condition": map[interface{}]interface{}{
					"operator": "!=",
					"field":    "status",
					"value":    "error",
				},
			},
		},
	}

	filter1, _ := NewFilter("filter1", filter1Config, nc, logger, nil)

	filter2Config := &Config{
		FilterID:       "filter2",
		InputTopic:     "test.intermediate",
		OutputTopic:    "test.output4",
		RejectionTopic: "test.rejection4b",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "second_rule",
				"condition": map[interface{}]interface{}{
					"operator": ">",
					"field":    "priority",
					"value":    3,
				},
			},
		},
	}

	filter2, _ := NewFilter("filter2", filter2Config, nc, logger, nil)

	// Subscribe to final output
	outputReceived := make(chan []byte, 1)
	sub, _ := nc.Subscribe(filter2Config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
	})
	defer sub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter1.Start(ctx)
	filter2.Start(ctx)
	defer filter1.Stop(ctx)
	defer filter2.Stop(ctx)

	// Publish test message
	testEnv := envelope.New()
	testEnv.ID = "test_msg_4"
	payload := map[string]interface{}{
		"status":   "ok",
		"priority": 5,
	}
	payloadBytes, _ := json.Marshal(payload)
	testEnv.Payload = payloadBytes
	testEnv.ContentType = "application/json"

	data, _ := envelope.Marshal(testEnv)
	nc.Publish(filter1Config.InputTopic, data)

	// Wait for message to pass through both filters
	select {
	case <-outputReceived:
		// Message passed both filters as expected
	case <-time.After(3 * time.Second):
		t.Errorf("Timeout waiting for final output message")
	}
}

func TestIntegration_RejectionHandling(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_filter",
		InputTopic:     "test.input5",
		OutputTopic:    "test.output5",
		RejectionTopic: "test.rejection5",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "always_reject",
				"condition": map[interface{}]interface{}{
					"operator": "==",
					"field":    "impossible",
					"value":    "true",
				},
			},
		},
	}

	filter, _ := NewFilter("test_filter", config, nc, logger, nil)

	// Subscribe to rejection topic to verify structure
	rejectionReceived := make(chan *envelope.Envelope, 1)
	sub, _ := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		rejectionReceived <- env
	})
	defer sub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter.Start(ctx)
	defer filter.Stop(ctx)

	testEnv := envelope.New()
	testEnv.ID = "test_msg_5"
	testEnv.Payload = []byte(`{"data":"test"}`)
	testEnv.ContentType = "application/json"

	data, _ := envelope.Marshal(testEnv)
	nc.Publish(config.InputTopic, data)

	// Wait and verify rejection metadata
	select {
	case rejEnv := <-rejectionReceived:
		if rejEnv.Metadata["rejection_reason"] == nil {
			t.Errorf("Rejection reason not set in metadata")
		}
	case <-time.After(2 * time.Second):
		t.Errorf("Timeout waiting for rejection message")
	}
}
