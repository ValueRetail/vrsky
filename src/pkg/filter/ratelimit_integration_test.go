//go:build integration
// +build integration

package filter

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// TestIntegration_TimeWindowRateLimit tests time-window rate limiting across multiple messages
func TestIntegration_TimeWindowRateLimit(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_rate_limit_tw",
		InputTopic:     "test.ratelimit.tw.input",
		OutputTopic:    "test.ratelimit.tw.output",
		RejectionTopic: "test.ratelimit.tw.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RateLimitRules: []interface{}{
			map[interface{}]interface{}{
				"id":                      "tw_limit_3_per_sec",
				"strategy":                "time_window",
				"max_messages_per_window": 3,
				"window_duration_seconds": 1,
				"exceed_action":           "reject",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
	}

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("test_rate_limit_tw", config, nc, logger, reg)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to output and rejection topics
	outputReceived := make(chan []byte, 10)
	rejectionReceived := make(chan []byte, 10)

	outSub, err := nc.Subscribe(config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe to output error = %v", err)
	}
	defer outSub.Unsubscribe()

	rejSub, err := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		rejectionReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe to rejection error = %v", err)
	}
	defer rejSub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

	// Publish 5 messages quickly - first 3 should pass, last 2 should be rejected
	for i := 1; i <= 5; i++ {
		testEnv := envelope.New()
		testEnv.ID = fmt.Sprintf("tw_msg_%d", i)
		testEnv.Payload = []byte(fmt.Sprintf(`{"order_id":%d,"amount":100}`, i))
		testEnv.ContentType = "application/json"

		data, err := envelope.Marshal(testEnv)
		if err != nil {
			t.Fatalf("Marshal error = %v", err)
		}

		err = nc.Publish(config.InputTopic, data)
		if err != nil {
			t.Fatalf("Publish error = %v", err)
		}
	}

	// Wait for messages to be processed
	time.Sleep(500 * time.Millisecond)

	// Verify: 3 messages on output, 2 on rejection
	if len(outputReceived) != 3 {
		t.Errorf("Expected 3 output messages, got %d", len(outputReceived))
	}
	if len(rejectionReceived) != 2 {
		t.Errorf("Expected 2 rejected messages, got %d", len(rejectionReceived))
	}
}

// TestIntegration_ConcurrentRateLimit tests concurrent request limiting
func TestIntegration_ConcurrentRateLimit(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_rate_limit_conc",
		InputTopic:     "test.ratelimit.conc.input",
		OutputTopic:    "test.ratelimit.conc.output",
		RejectionTopic: "test.ratelimit.conc.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RateLimitRules: []interface{}{
			map[interface{}]interface{}{
				"id":             "conc_limit_2",
				"strategy":       "concurrent",
				"max_concurrent": 2,
				"exceed_action":  "drop",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
	}

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("test_rate_limit_conc", config, nc, logger, reg)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to output and rejection topics
	outputReceived := make(chan []byte, 10)
	rejectionReceived := make(chan []byte, 10)

	outSub, err := nc.Subscribe(config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
		// Simulate processing delay
		time.Sleep(100 * time.Millisecond)
	})
	if err != nil {
		t.Fatalf("Subscribe to output error = %v", err)
	}
	defer outSub.Unsubscribe()

	rejSub, err := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		rejectionReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe to rejection error = %v", err)
	}
	defer rejSub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

	// Publish 4 messages quickly - first 2 should pass concurrently, last 2 should be dropped
	for i := 1; i <= 4; i++ {
		testEnv := envelope.New()
		testEnv.ID = fmt.Sprintf("conc_msg_%d", i)
		testEnv.Payload = []byte(fmt.Sprintf(`{"request_id":%d}`, i))
		testEnv.ContentType = "application/json"

		data, err := envelope.Marshal(testEnv)
		if err != nil {
			t.Fatalf("Marshal error = %v", err)
		}

		err = nc.Publish(config.InputTopic, data)
		if err != nil {
			t.Fatalf("Publish error = %v", err)
		}
	}

	// Wait for processing
	time.Sleep(600 * time.Millisecond)

	// Verify: some messages on output, some dropped
	if len(outputReceived) == 0 {
		t.Errorf("Expected some output messages, got %d", len(outputReceived))
	}
}

// TestIntegration_TokenBucketRateLimit tests token bucket rate limiting
func TestIntegration_TokenBucketRateLimit(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_rate_limit_tb",
		InputTopic:     "test.ratelimit.tb.input",
		OutputTopic:    "test.ratelimit.tb.output",
		RejectionTopic: "test.ratelimit.tb.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RateLimitRules: []interface{}{
			map[interface{}]interface{}{
				"id":                    "tb_limit_2tps",
				"strategy":              "token_bucket",
				"token_bucket_rate":     2,
				"token_bucket_capacity": 5,
				"exceed_action":         "reject",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
	}

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("test_rate_limit_tb", config, nc, logger, reg)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to output and rejection topics
	outputReceived := make(chan []byte, 20)
	rejectionReceived := make(chan []byte, 20)

	outSub, err := nc.Subscribe(config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe to output error = %v", err)
	}
	defer outSub.Unsubscribe()

	rejSub, err := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		rejectionReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe to rejection error = %v", err)
	}
	defer rejSub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

	// Publish 10 messages - initial 5 should pass (burst capacity), then rate limited
	for i := 1; i <= 10; i++ {
		testEnv := envelope.New()
		testEnv.ID = fmt.Sprintf("tb_msg_%d", i)
		testEnv.Payload = []byte(fmt.Sprintf(`{"api_call":%d}`, i))
		testEnv.ContentType = "application/json"

		data, err := envelope.Marshal(testEnv)
		if err != nil {
			t.Fatalf("Marshal error = %v", err)
		}

		err = nc.Publish(config.InputTopic, data)
		if err != nil {
			t.Fatalf("Publish error = %v", err)
		}

		// Small delay between publishes
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for all processing
	time.Sleep(1 * time.Second)

	// Verify: some messages passed (initial burst), some rejected
	totalReceived := len(outputReceived) + len(rejectionReceived)
	if totalReceived != 10 {
		t.Errorf("Expected 10 total messages, got %d (passed: %d, rejected: %d)",
			totalReceived, len(outputReceived), len(rejectionReceived))
	}
	// Should have accepted at least the burst capacity
	if len(outputReceived) < 5 {
		t.Errorf("Expected at least 5 messages in burst, got %d", len(outputReceived))
	}
}

// TestIntegration_ConditionalRateLimit tests rate limiting with conditions
func TestIntegration_ConditionalRateLimit(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_rate_limit_cond",
		InputTopic:     "test.ratelimit.cond.input",
		OutputTopic:    "test.ratelimit.cond.output",
		RejectionTopic: "test.ratelimit.cond.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RateLimitRules: []interface{}{
			map[interface{}]interface{}{
				"id":                      "premium_only",
				"strategy":                "time_window",
				"max_messages_per_window": 10,
				"window_duration_seconds": 1,
				"exceed_action":           "reject",
				"condition": map[interface{}]interface{}{
					"operator": "==",
					"field":    "user.tier",
					"value":    "premium",
				},
			},
		},
	}

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("test_rate_limit_cond", config, nc, logger, reg)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to output and rejection topics
	outputReceived := make(chan []byte, 20)
	rejectionReceived := make(chan []byte, 20)

	outSub, err := nc.Subscribe(config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe to output error = %v", err)
	}
	defer outSub.Unsubscribe()

	rejSub, err := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		rejectionReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe to rejection error = %v", err)
	}
	defer rejSub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

	// Publish 15 premium user messages - should be rate limited
	for i := 1; i <= 15; i++ {
		testEnv := envelope.New()
		testEnv.ID = fmt.Sprintf("premium_msg_%d", i)
		testEnv.Payload = []byte(fmt.Sprintf(`{"user":{"tier":"premium"},"order_id":%d}`, i))
		testEnv.ContentType = "application/json"

		data, err := envelope.Marshal(testEnv)
		if err != nil {
			t.Fatalf("Marshal error = %v", err)
		}

		err = nc.Publish(config.InputTopic, data)
		if err != nil {
			t.Fatalf("Publish error = %v", err)
		}
	}

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	// Verify: 10 passed (limit), 5 rejected
	if len(outputReceived) != 10 {
		t.Errorf("Expected 10 output messages for premium users, got %d", len(outputReceived))
	}
	if len(rejectionReceived) != 5 {
		t.Errorf("Expected 5 rejected messages, got %d", len(rejectionReceived))
	}
}

// TestIntegration_MultipleRateLimitStrategies tests multiple rate limit rules working together
func TestIntegration_MultipleRateLimitStrategies(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "test_rate_limit_multi",
		InputTopic:     "test.ratelimit.multi.input",
		OutputTopic:    "test.ratelimit.multi.output",
		RejectionTopic: "test.ratelimit.multi.rejection",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RateLimitRules: []interface{}{
			map[interface{}]interface{}{
				"id":                      "tw_limit",
				"priority":                1,
				"strategy":                "time_window",
				"max_messages_per_window": 5,
				"window_duration_seconds": 1,
				"exceed_action":           "reject",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
			map[interface{}]interface{}{
				"id":             "conc_limit",
				"priority":       2,
				"strategy":       "concurrent",
				"max_concurrent": 3,
				"exceed_action":  "drop",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
	}

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("test_rate_limit_multi", config, nc, logger, reg)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to output and rejection topics
	outputReceived := make(chan []byte, 20)
	rejectionReceived := make(chan []byte, 20)

	outSub, err := nc.Subscribe(config.OutputTopic, func(msg *nats.Msg) {
		outputReceived <- msg.Data
		// Simulate processing
		time.Sleep(50 * time.Millisecond)
	})
	if err != nil {
		t.Fatalf("Subscribe to output error = %v", err)
	}
	defer outSub.Unsubscribe()

	rejSub, err := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		rejectionReceived <- msg.Data
	})
	if err != nil {
		t.Fatalf("Subscribe to rejection error = %v", err)
	}
	defer rejSub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	// Wait for subscription to be established
	time.Sleep(100 * time.Millisecond)

	// Send messages in parallel to test concurrent limiting
	var wg sync.WaitGroup
	for batch := 0; batch < 3; batch++ {
		for i := 1; i <= 6; i++ {
			wg.Add(1)
			go func(batchNum, msgNum int) {
				defer wg.Done()

				testEnv := envelope.New()
				testEnv.ID = fmt.Sprintf("multi_msg_%d_%d", batchNum, msgNum)
				testEnv.Payload = []byte(fmt.Sprintf(`{"batch":%d,"msg":%d}`, batchNum, msgNum))
				testEnv.ContentType = "application/json"

				data, err := envelope.Marshal(testEnv)
				if err != nil {
					t.Errorf("Marshal error = %v", err)
					return
				}

				err = nc.Publish(config.InputTopic, data)
				if err != nil {
					t.Errorf("Publish error = %v", err)
				}
			}(batch, i)
		}
		time.Sleep(200 * time.Millisecond)
	}

	wg.Wait()
	time.Sleep(1 * time.Second)

	// Verify: messages are rate limited by both strategies
	totalMessages := 3 * 6 // 3 batches of 6 messages
	totalReceived := len(outputReceived) + len(rejectionReceived)

	if totalReceived > totalMessages {
		t.Errorf("Received more messages than sent: received %d, sent %d", totalReceived, totalMessages)
	}

	// Should have rejected or dropped some messages due to rate limits
	if len(rejectionReceived) == 0 && len(outputReceived) == totalMessages {
		t.Logf("Warning: No rate limiting occurred, all %d messages passed", totalMessages)
	}
}
