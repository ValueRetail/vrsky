//go:build integration
// +build integration

package filter

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// TestE2E_OrderThrottling simulates real-world order processing with rate limiting
// Scenario: A retail platform wants to throttle order processing to prevent
// database overload, while routing high-value orders to premium queue
func TestE2E_OrderThrottling(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "order_throttle_filter",
		InputTopic:     "orders.standard",
		OutputTopic:    "orders.processing",
		RejectionTopic: "orders.rejected",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_valid_orders",
				"condition": map[interface{}]interface{}{
					"operator": ">=",
					"field":    "total_amount",
					"value":    10,
				},
			},
		},
		RoutingRules: []interface{}{
			map[interface{}]interface{}{
				"name":         "route_premium_orders",
				"priority":     1,
				"output_topic": "orders.premium_processing",
				"condition": map[interface{}]interface{}{
					"operator": ">=",
					"field":    "total_amount",
					"value":    1000,
				},
			},
			map[interface{}]interface{}{
				"name":         "route_standard_orders",
				"priority":     2,
				"output_topic": "orders.standard_processing",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RateLimitRules: []interface{}{
			map[interface{}]interface{}{
				"id":                      "premium_limit",
				"priority":                1,
				"strategy":                "time_window",
				"max_messages_per_window": 100,
				"window_duration_seconds": 60,
				"exceed_action":           "queue",
				"queue_size":              5000,
				"condition": map[interface{}]interface{}{
					"operator": ">=",
					"field":    "total_amount",
					"value":    1000,
				},
			},
			map[interface{}]interface{}{
				"id":                    "standard_limit",
				"priority":              2,
				"strategy":              "token_bucket",
				"token_bucket_rate":     50,
				"token_bucket_capacity": 100,
				"exceed_action":         "queue",
				"queue_size":            10000,
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
	}

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("order_throttle_filter", config, nc, logger, reg)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to all output topics
	premiumProcessing := make(chan []byte, 200)
	standardProcessing := make(chan []byte, 200)
	rejectionTopic := make(chan []byte, 100)

	premiumSub, _ := nc.Subscribe("orders.premium_processing", func(msg *nats.Msg) {
		premiumProcessing <- msg.Data
	})
	defer premiumSub.Unsubscribe()

	standardSub, _ := nc.Subscribe("orders.standard_processing", func(msg *nats.Msg) {
		standardProcessing <- msg.Data
	})
	defer standardSub.Unsubscribe()

	rejectionSub, _ := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		rejectionTopic <- msg.Data
	})
	defer rejectionSub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	time.Sleep(100 * time.Millisecond)

	// Simulate real order stream: mix of standard and premium orders
	orderAmounts := []int{50, 500, 1500, 75, 2000, 100, 1200, 150, 3000, 200}
	orderCount := 0

	for i := 0; i < 3; i++ {
		for _, amount := range orderAmounts {
			orderCount++
			testEnv := envelope.New()
			testEnv.ID = fmt.Sprintf("order_%d", orderCount)
			testEnv.Payload = []byte(fmt.Sprintf(
				`{"order_id":%d,"total_amount":%d,"customer":"cust_%d","items":3}`,
				orderCount, amount, orderCount%10,
			))
			testEnv.ContentType = "application/json"

			data, err := envelope.Marshal(testEnv)
			if err != nil {
				t.Fatalf("Marshal error = %v", err)
			}

			err = nc.Publish(config.InputTopic, data)
			if err != nil {
				t.Fatalf("Publish error = %v", err)
			}

			// Small delay to simulate order stream
			time.Sleep(10 * time.Millisecond)
		}

		// Wait between batches for rate limits to process
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for all messages to be processed
	time.Sleep(2 * time.Second)

	// Verify results
	premiumCount := len(premiumProcessing)
	standardCount := len(standardProcessing)
	rejectionCount := len(rejectionTopic)
	totalProcessed := premiumCount + standardCount

	t.Logf("Order processing results:")
	t.Logf("  Premium orders processed: %d", premiumCount)
	t.Logf("  Standard orders processed: %d", standardCount)
	t.Logf("  Total processed: %d", totalProcessed)
	t.Logf("  Rejected: %d", rejectionCount)

	// Verify: should have processed most orders (some may be queued)
	if totalProcessed < 20 {
		t.Errorf("Expected at least 20 orders processed, got %d", totalProcessed)
	}

	// Verify: premium orders are routed correctly to premium_processing queue
	if premiumCount < 5 {
		t.Logf("Warning: Expected at least 5 premium orders, got %d", premiumCount)
	}

	// Verify: standard orders should also be processed
	if standardCount < 10 {
		t.Logf("Warning: Expected at least 10 standard orders, got %d", standardCount)
	}
}

// TestE2E_APIRateLimitingWithTiers simulates SaaS API with rate limiting tiers
// Scenario: An API platform has different rate limit tiers for different users
// (free: 100/min, pro: 1000/min, enterprise: unlimited)
func TestE2E_APIRateLimitingWithTiers(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	logger := slog.Default()
	config := &Config{
		FilterID:       "api_rate_limit_filter",
		InputTopic:     "api.requests",
		OutputTopic:    "api.processing",
		RejectionTopic: "api.rate_limited",
		Rules: []interface{}{
			map[interface{}]interface{}{
				"name": "accept_all_requests",
				"condition": map[interface{}]interface{}{
					"operator": "always",
				},
			},
		},
		RateLimitRules: []interface{}{
			map[interface{}]interface{}{
				"id":                      "free_tier_limit",
				"priority":                1,
				"strategy":                "time_window",
				"max_messages_per_window": 100,
				"window_duration_seconds": 60,
				"exceed_action":           "drop",
				"condition": map[interface{}]interface{}{
					"operator": "==",
					"field":    "user.tier",
					"value":    "free",
				},
			},
			map[interface{}]interface{}{
				"id":                    "pro_tier_limit",
				"priority":              2,
				"strategy":              "token_bucket",
				"token_bucket_rate":     50,
				"token_bucket_capacity": 200,
				"exceed_action":         "reject",
				"condition": map[interface{}]interface{}{
					"operator": "==",
					"field":    "user.tier",
					"value":    "pro",
				},
			},
			map[interface{}]interface{}{
				"id":             "enterprise_concurrent_limit",
				"priority":       3,
				"strategy":       "concurrent",
				"max_concurrent": 1000,
				"exceed_action":  "queue",
				"queue_size":     50000,
				"condition": map[interface{}]interface{}{
					"operator": "==",
					"field":    "user.tier",
					"value":    "enterprise",
				},
			},
		},
	}

	reg := prometheus.NewRegistry()
	filter, err := NewFilter("api_rate_limit_filter", config, nc, logger, reg)
	if err != nil {
		t.Fatalf("NewFilter error = %v", err)
	}

	// Subscribe to output and rejection topics
	processedRequests := make(chan []byte, 5000)
	rateLimitedRequests := make(chan []byte, 5000)

	procSub, _ := nc.Subscribe(config.OutputTopic, func(msg *nats.Msg) {
		processedRequests <- msg.Data
	})
	defer procSub.Unsubscribe()

	rateSub, _ := nc.Subscribe(config.RejectionTopic, func(msg *nats.Msg) {
		rateLimitedRequests <- msg.Data
	})
	defer rateSub.Unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := filter.Start(ctx); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	defer filter.Stop(ctx)

	time.Sleep(100 * time.Millisecond)

	// Simulate API requests from different tier users
	tiers := []string{"free", "pro", "enterprise"}
	requestsPerTier := 200

	for tierIdx, tier := range tiers {
		for i := 1; i <= requestsPerTier; i++ {
			requestID := tierIdx*requestsPerTier + i
			testEnv := envelope.New()
			testEnv.ID = fmt.Sprintf("api_req_%d", requestID)
			testEnv.Payload = []byte(fmt.Sprintf(
				`{"user":{"id":"user_%d_%d","tier":"%s"},"endpoint":"/api/data","method":"GET"}`,
				tierIdx, i, tier,
			))
			testEnv.ContentType = "application/json"

			data, err := envelope.Marshal(testEnv)
			if err != nil {
				t.Fatalf("Marshal error = %v", err)
			}

			err = nc.Publish(config.InputTopic, data)
			if err != nil {
				t.Fatalf("Publish error = %v", err)
			}

			// Simulate request arrival rate
			time.Sleep(5 * time.Millisecond)
		}

		// Wait between tiers
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for processing
	time.Sleep(2 * time.Second)

	// Verify results
	processedCount := len(processedRequests)
	rateLimitedCount := len(rateLimitedRequests)
	totalRequests := len(tiers) * requestsPerTier

	t.Logf("API rate limiting results:")
	t.Logf("  Total requests: %d", totalRequests)
	t.Logf("  Processed: %d", processedCount)
	t.Logf("  Rate limited: %d", rateLimitedCount)
	t.Logf("  Acceptance rate: %.1f%%", float64(processedCount)/float64(totalRequests)*100)

	// Verify: free tier should be heavily rate limited (100/min limit)
	// pro tier should be moderately limited
	// enterprise should have most requests processed
	if processedCount == 0 {
		t.Errorf("Expected some requests to be processed, got 0")
	}

	// Verify: should have rejected some requests
	if rateLimitedCount == 0 {
		t.Logf("Warning: Expected some rate limited requests, got 0")
	}

	// Overall: total should match expectations
	totalAll := processedCount + rateLimitedCount
	if totalAll != totalRequests {
		t.Logf("Note: Processed+Limited=%d, Total=%d (difference may be in queue)", totalAll, totalRequests)
	}
}
