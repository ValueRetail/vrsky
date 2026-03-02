//go:build integration
// +build integration

package filter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

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

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("test_filter", config, nc, logger, reg)
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
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

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

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("test_filter", config, nc, logger, reg)
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
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

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

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("test_filter", config, nc, logger, reg)
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
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

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

	reg1 := prometheus.NewRegistry()
	filter1, _ := NewFilter("filter1", filter1Config, nc, logger, reg1)

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

	reg2 := prometheus.NewRegistry()
	filter2, _ := NewFilter("filter2", filter2Config, nc, logger, reg2)

	// Subscribe to final output
	outputReceived := make(chan []byte, 1)
	sub, _ := nc.Subscribe(filter2Config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
	})
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter1.Start(ctx)
	filter2.Start(ctx)
	defer filter1.Stop(ctx)
	defer filter2.Stop(ctx)

	// Wait for subscriptions to be established
	time.Sleep(100 * time.Millisecond)

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

	reg := prometheus.NewRegistry()
	filter, _ := NewFilter("test_filter", config, nc, logger, reg)

	// Subscribe to rejection topic to verify structure
	rejectionReceived := make(chan *envelope.Envelope, 1)
	sub, _ := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		rejectionReceived <- env
	})
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter.Start(ctx)
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

	testEnv := envelope.New()
	testEnv.ID = "test_msg_5"
	testEnv.Payload = []byte(`{"data":"test"}`)
	testEnv.ContentType = "application/json"

	data, _ := envelope.Marshal(testEnv)
	nc.Publish(config.InputTopic, data)

	// Wait and verify rejection message received
	select {
	case rejEnv := <-rejectionReceived:
		if rejEnv == nil {
			t.Errorf("Rejection message not received")
		}
		// Verify message was routed to rejection topic
		if rejEnv.ID != testEnv.ID {
			t.Errorf("Wrong envelope received, expected ID %s, got %s", testEnv.ID, rejEnv.ID)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("Timeout waiting for rejection message")
	}
}

func TestIntegration_RejectionHandlerMetadata(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	rejectionHandler := NewRejectionHandler(nc, "test.rejection.handler", "test.dlq", logger)

	testEnv := envelope.New()
	testEnv.ID = "test_msg_metadata"
	testEnv.Payload = []byte(`{"data":"test"}`)
	testEnv.ContentType = "application/json"

	rejectionReceived := make(chan *envelope.Envelope, 1)
	sub, _ := nc.Subscribe("test.rejection.handler", func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		rejectionReceived <- env
	})
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Handle rejection with metadata
	err := rejectionHandler.HandleRejection(ctx, testEnv, "test rejection reason", "test_rule")
	if err != nil {
		t.Fatalf("HandleRejection error = %v", err)
	}

	// Verify metadata was added
	select {
	case rejEnv := <-rejectionReceived:
		if rejEnv == nil || rejEnv.Metadata == nil {
			t.Errorf("Rejection metadata not found")
		}
		if reason, ok := rejEnv.Metadata["rejection_reason"]; !ok || reason != "test rejection reason" {
			t.Errorf("Rejection reason not set correctly, got: %v", reason)
		}
		if ruleID, ok := rejEnv.Metadata["rejected_by_rule"]; !ok || ruleID != "test_rule" {
			t.Errorf("Rule ID not set correctly, got: %v", ruleID)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("Timeout waiting for rejection with metadata")
	}
}

func TestIntegration_DeadLetterQueue(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	dlq := NewDeadLetterQueue(nc, "test.dlq", logger)

	testEnv := envelope.New()
	testEnv.ID = "test_msg_dlq"
	testEnv.Payload = []byte(`{"data":"dlq_test"}`)
	testEnv.ContentType = "application/json"
	testEnv.RetryCount = 3

	dlqReceived := make(chan *envelope.Envelope, 1)
	sub, _ := nc.Subscribe("test.dlq", func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		dlqReceived <- env
	})
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Publish to DLQ
	err := dlq.PublishMessage(ctx, testEnv, "test dlq reason")
	if err != nil {
		t.Fatalf("PublishMessage error = %v", err)
	}

	// Verify DLQ metadata
	select {
	case dlqEnv := <-dlqReceived:
		if dlqEnv == nil || dlqEnv.Metadata == nil {
			t.Errorf("DLQ metadata not found")
		}
		if reason, ok := dlqEnv.Metadata["dlq_reason"]; !ok || reason != "test dlq reason" {
			t.Errorf("DLQ reason not set correctly, got: %v", reason)
		}
		// retry_count is stored in Metadata when it's the envelope's RetryCount
		if _, ok := dlqEnv.Metadata["retry_count"]; !ok {
			t.Errorf("Retry count not set in metadata")
		}
	case <-time.After(2 * time.Second):
		t.Errorf("Timeout waiting for DLQ message")
	}
}

func TestIntegration_ErrorRecovery(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	rejectionHandler := NewRejectionHandler(nc, "test.rejection.recovery", "test.dlq.recovery", logger)
	dlq := NewDeadLetterQueue(nc, "test.dlq.recovery", logger)
	errorRecovery := NewErrorRecovery(rejectionHandler, dlq, logger)

	testEnv := envelope.New()
	testEnv.ID = "test_msg_error"
	testEnv.Payload = []byte(`{"data":"error_test"}`)
	testEnv.ContentType = "application/json"
	testEnv.RetryCount = 0

	dlqReceived := make(chan *envelope.Envelope, 1)
	sub, _ := nc.Subscribe("test.dlq.recovery", func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		dlqReceived <- env
	})
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	time.Sleep(100 * time.Millisecond)

	// Simulate multiple errors to exceed retry limit
	testErr := fmt.Errorf("test processing error")
	for i := 0; i < 3; i++ {
		err := errorRecovery.HandleProcessingError(ctx, testEnv, testErr)
		if i < 2 {
			// First two attempts should return nil (no error)
			if err != nil {
				t.Fatalf("HandleProcessingError should not error on retry %d, got: %v", i, err)
			}
		}
	}

	// After 3 retries, message should be in DLQ
	select {
	case dlqEnv := <-dlqReceived:
		if dlqEnv == nil {
			t.Errorf("Message not found in DLQ after max retries")
		}
	case <-time.After(2 * time.Second):
		t.Errorf("Timeout waiting for message in DLQ")
	}
}

// ============================================================================
// Priority 2: Conditional Routing Integration Tests
// ============================================================================

func TestIntegration_Priority2_BasicRouting(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "routing_filter",
		InputTopic:     "test.routing.input",
		OutputTopic:    "test.routing.default",
		RejectionTopic: "test.routing.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RoutingRules: []interface{}{
			map[interface{}]interface{}{
				"id":           "premium_route",
				"name":         "Premium Orders",
				"priority":     1,
				"condition":    map[interface{}]interface{}{"operator": "==", "field": "tier", "value": "premium"},
				"output_topic": "test.routing.premium",
			},
			map[interface{}]interface{}{
				"id":           "standard_route",
				"name":         "Standard Orders",
				"priority":     2,
				"condition":    map[interface{}]interface{}{"operator": "always"},
				"output_topic": "test.routing.standard",
			},
		},
	}

	f, err := NewFilter("routing_filter", config, nc, logger, prometheus.DefaultRegisterer)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer f.Stop(ctx)

	// Subscribe to routing topics
	premiumMsgs := make(chan *envelope.Envelope, 1)
	standardMsgs := make(chan *envelope.Envelope, 1)

	premiumSub, _ := nc.Subscribe("test.routing.premium", func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		premiumMsgs <- env
	})
	defer premiumSub.Unsubscribe()

	standardSub, _ := nc.Subscribe("test.routing.standard", func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		standardMsgs <- env
	})
	defer standardSub.Unsubscribe()

	time.Sleep(100 * time.Millisecond)

	// Send premium order
	premiumOrder := envelope.New()
	premiumOrder.ID = "premium_order_1"
	premiumOrder.Payload = []byte(`{"order_id":"p1","tier":"premium","amount":1500}`)
	premiumOrder.ContentType = "application/json"

	data, _ := envelope.Marshal(premiumOrder)
	nc.Publish("test.routing.input", data)

	time.Sleep(200 * time.Millisecond)

	// Send standard order
	standardOrder := envelope.New()
	standardOrder.ID = "standard_order_1"
	standardOrder.Payload = []byte(`{"order_id":"s1","tier":"standard","amount":50}`)
	standardOrder.ContentType = "application/json"

	data, _ = envelope.Marshal(standardOrder)
	nc.Publish("test.routing.input", data)

	time.Sleep(200 * time.Millisecond)

	// Verify routing
	select {
	case msg := <-premiumMsgs:
		if msg == nil || msg.ID != "premium_order_1" {
			t.Errorf("Premium message not routed correctly")
		}
	case <-time.After(1 * time.Second):
		t.Errorf("Premium message not received in routing topic")
	}

	select {
	case msg := <-standardMsgs:
		if msg == nil || msg.ID != "standard_order_1" {
			t.Errorf("Standard message not routed correctly")
		}
	case <-time.After(1 * time.Second):
		t.Errorf("Standard message not received in routing topic")
	}
}

func TestIntegration_Priority2_TransformationsWithRouting(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "transform_filter",
		InputTopic:     "test.transform.input",
		OutputTopic:    "test.transform.default",
		RejectionTopic: "test.transform.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RoutingRules: []interface{}{
			map[interface{}]interface{}{
				"id":           "with_transforms",
				"name":         "Add Metadata",
				"priority":     1,
				"condition":    map[interface{}]interface{}{"operator": "always"},
				"output_topic": "test.transform.routed",
				"transformations": []interface{}{
					map[interface{}]interface{}{
						"action": "add_field",
						"field":  "routed_by",
						"value":  "priority2_filter",
					},
					map[interface{}]interface{}{
						"action": "add_field",
						"field":  "trace_id",
						"value":  "${uuid()}",
					},
				},
			},
		},
	}

	f, err := NewFilter("transform_filter", config, nc, logger, prometheus.DefaultRegisterer)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer f.Stop(ctx)

	// Subscribe to routed topic
	routedMsgs := make(chan *envelope.Envelope, 1)
	routedSub, _ := nc.Subscribe("test.transform.routed", func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		routedMsgs <- env
	})
	defer routedSub.Unsubscribe()

	time.Sleep(100 * time.Millisecond)

	// Send message
	testOrder := envelope.New()
	testOrder.ID = "transform_test_1"
	testOrder.Payload = []byte(`{"order_id":"t1","status":"pending"}`)
	testOrder.ContentType = "application/json"

	data, _ := envelope.Marshal(testOrder)
	nc.Publish("test.transform.input", data)

	time.Sleep(200 * time.Millisecond)

	// Verify transformations were applied
	select {
	case msg := <-routedMsgs:
		if msg == nil || msg.ID != "transform_test_1" {
			t.Errorf("Message not routed correctly")
			return
		}

		if msg.Metadata["routed_by"] != "priority2_filter" {
			t.Errorf("routed_by field not set correctly: %v", msg.Metadata["routed_by"])
		}

		if _, exists := msg.Metadata["trace_id"]; !exists {
			t.Errorf("trace_id field not added")
		}

		if traceID, ok := msg.Metadata["trace_id"].(string); ok {
			if len(traceID) != 36 { // UUID length
				t.Errorf("trace_id not in correct format: %s", traceID)
			}
		} else {
			t.Errorf("trace_id not a string: %T", msg.Metadata["trace_id"])
		}

	case <-time.After(1 * time.Second):
		t.Errorf("Message not received in routed topic")
	}
}

func TestIntegration_Priority2_NestedFieldRouting(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "nested_filter",
		InputTopic:     "test.nested.input",
		OutputTopic:    "test.nested.default",
		RejectionTopic: "test.nested.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RoutingRules: []interface{}{
			map[interface{}]interface{}{
				"id":           "high_value",
				"priority":     1,
				"condition":    map[interface{}]interface{}{"operator": ">", "field": "order.amount", "value": 1000},
				"output_topic": "test.nested.high_value",
			},
			map[interface{}]interface{}{
				"id":           "standard",
				"priority":     2,
				"condition":    map[interface{}]interface{}{"operator": "always"},
				"output_topic": "test.nested.standard",
			},
		},
	}

	f, err := NewFilter("nested_filter", config, nc, logger, prometheus.DefaultRegisterer)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer f.Stop(ctx)

	// Subscribe to routing topics
	highValueMsgs := make(chan *envelope.Envelope, 1)
	standardMsgs := make(chan *envelope.Envelope, 1)

	highValueSub, _ := nc.Subscribe("test.nested.high_value", func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		highValueMsgs <- env
	})
	defer highValueSub.Unsubscribe()

	standardSub, _ := nc.Subscribe("test.nested.standard", func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		standardMsgs <- env
	})
	defer standardSub.Unsubscribe()

	time.Sleep(100 * time.Millisecond)

	// Send high-value order
	highOrder := envelope.New()
	highOrder.ID = "high_order_1"
	highOrder.Payload = []byte(`{"order":{"amount":1500,"currency":"USD"}}`)
	highOrder.ContentType = "application/json"

	data, _ := envelope.Marshal(highOrder)
	nc.Publish("test.nested.input", data)

	time.Sleep(200 * time.Millisecond)

	// Verify routing
	select {
	case msg := <-highValueMsgs:
		if msg == nil || msg.ID != "high_order_1" {
			t.Errorf("High-value message not routed correctly")
		}
	case <-time.After(1 * time.Second):
		t.Errorf("High-value message not received")
	}
}
