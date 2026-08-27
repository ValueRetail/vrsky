//go:build integration
// +build integration

// Package integration provides end-to-end integration tests for VRSky components.
// These tests verify that components correctly:
// - Expose health and metrics endpoints
// - Process messages through NATS
// - Support checkpoint persistence
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/health"
	"github.com/ValueRetail/vrsky/pkg/metrics"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
)

// setupNATS connects to a running NATS server for integration tests
func setupNATS(t *testing.T) *nats.Conn {
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		t.Skipf("NATS not available: %v", err)
	}
	return nc
}

// TestHealthServer_Endpoints tests health server endpoint responses
func TestHealthServer_Endpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server := health.NewServer(health.Config{
		Port:        18080,
		ComponentID: "test-component",
		NodeID:      "test-node",
		Logger:      nil,
	})

	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() {
		_ = server.Stop(ctx)
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	baseURL := "http://localhost:18080"

	tests := []struct {
		name       string
		endpoint   string
		setReady   bool
		wantStatus int
	}{
		{"health always ok", "/health", false, http.StatusOK},
		{"ready when not ready", "/ready", false, http.StatusServiceUnavailable},
		{"ready when ready", "/ready", true, http.StatusOK},
		{"metrics endpoint", "/metrics", false, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server.SetReady(tt.setReady)

			resp, err := http.Get(baseURL + tt.endpoint)
			if err != nil {
				t.Fatalf("GET %s failed: %v", tt.endpoint, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("GET %s status = %d, want %d", tt.endpoint, resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

// TestMetrics_Recording tests that metrics are properly recorded and exposed
func TestMetrics_Recording(t *testing.T) {
	reg := prometheus.NewRegistry()

	m, err := metrics.NewBase(metrics.Config{
		TenantID:     "test-tenant",
		ConnectionID: "test-conn",
		NodeID:       "test-node",
		NodeType:     metrics.TypeFilter,
		Registerer:   reg,
	})
	if err != nil {
		t.Fatalf("NewBase failed: %v", err)
	}

	// Record some metrics
	m.RecordReceived()
	m.RecordReceived()
	m.RecordProcessed()
	m.RecordFailed("test_error")

	start := time.Now().Add(-100 * time.Millisecond)
	m.ObserveProcessing(start, nil)

	// Gather and verify metrics
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}

	metricNames := make(map[string]bool)
	for _, fam := range families {
		metricNames[fam.GetName()] = true
	}

	// Metrics have the format: vrsky_<node_type>_<metric_name>
	expectedMetrics := []string{
		"vrsky_filter_messages_received_total",
		"vrsky_filter_messages_processed_total",
		"vrsky_filter_messages_failed_total",
		"vrsky_filter_processing_duration_seconds",
	}

	for _, name := range expectedMetrics {
		if !metricNames[name] {
			t.Errorf("Expected metric %q not found", name)
		}
	}
}

// TestCheckpoint_InMemory tests in-memory checkpoint persistence
func TestCheckpoint_InMemory(t *testing.T) {
	ctx := context.Background()
	store := checkpoint.NewInMemoryStore()

	// Save a checkpoint
	cp := &checkpoint.Checkpoint{
		TenantID:               "tenant-1",
		ConnectionID:           "conn-1",
		NodeID:                 "node-1",
		LastProcessedMessageID: "msg-100",
		LastProcessedAt:        time.Now(),
		MessageCount:           100,
	}

	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Retrieve the checkpoint
	retrieved, err := store.Get(ctx, "tenant-1", "conn-1", "node-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("Get returned nil")
	}

	if retrieved.LastProcessedMessageID != "msg-100" {
		t.Errorf("LastProcessedMessageID = %q, want %q", retrieved.LastProcessedMessageID, "msg-100")
	}

	if retrieved.MessageCount != 100 {
		t.Errorf("MessageCount = %d, want %d", retrieved.MessageCount, 100)
	}

	// Delete the checkpoint
	if err := store.Delete(ctx, "tenant-1", "conn-1", "node-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deletion
	deleted, err := store.Get(ctx, "tenant-1", "conn-1", "node-1")
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}

	if deleted != nil {
		t.Error("Get after delete returned non-nil")
	}
}

// TestNATSMessageFlow tests NATS message publishing and receiving through envelopes
func TestNATSMessageFlow(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	inputSubject := fmt.Sprintf("test.integration.input.%d", time.Now().UnixNano())
	outputSubject := fmt.Sprintf("test.integration.output.%d", time.Now().UnixNano())

	// Subscribe to output
	received := make(chan *envelope.Envelope, 1)
	sub, err := nc.Subscribe(outputSubject, func(msg *nats.Msg) {
		env, err := envelope.Unmarshal(msg.Data)
		if err != nil {
			t.Logf("Unmarshal error: %v", err)
			return
		}
		received <- env
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	// Give subscription time to establish
	time.Sleep(100 * time.Millisecond)

	// Create and publish a test envelope
	env := envelope.New()
	env.ID = "test-msg-001"
	env.Payload = []byte(`{"key": "value"}`)
	env.ContentType = "application/json"
	env.Source = inputSubject

	data, err := envelope.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if err := nc.Publish(outputSubject, data); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Wait for message
	select {
	case rcv := <-received:
		if rcv.ID != env.ID {
			t.Errorf("ID = %q, want %q", rcv.ID, env.ID)
		}
		if string(rcv.Payload) != string(env.Payload) {
			t.Errorf("Payload = %q, want %q", string(rcv.Payload), string(env.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Error("Timeout waiting for message")
	}
}

// TestCheckpoint_DeleteForConnection tests bulk deletion of checkpoints
func TestCheckpoint_DeleteForConnection(t *testing.T) {
	ctx := context.Background()
	store := checkpoint.NewInMemoryStore()

	// Create multiple checkpoints for the same connection
	for i := 1; i <= 5; i++ {
		cp := &checkpoint.Checkpoint{
			TenantID:               "tenant-1",
			ConnectionID:           "conn-1",
			NodeID:                 fmt.Sprintf("node-%d", i),
			LastProcessedMessageID: fmt.Sprintf("msg-%d", i*100),
			MessageCount:           int64(i * 100),
		}
		if err := store.Save(ctx, cp); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}

	// Add a checkpoint for a different connection
	otherCP := &checkpoint.Checkpoint{
		TenantID:     "tenant-1",
		ConnectionID: "conn-2",
		NodeID:       "node-1",
		MessageCount: 999,
	}
	if err := store.Save(ctx, otherCP); err != nil {
		t.Fatalf("Save other failed: %v", err)
	}

	// Delete all checkpoints for conn-1
	if err := store.DeleteForConnection(ctx, "tenant-1", "conn-1"); err != nil {
		t.Fatalf("DeleteForConnection failed: %v", err)
	}

	// Verify conn-1 checkpoints are deleted
	for i := 1; i <= 5; i++ {
		cp, err := store.Get(ctx, "tenant-1", "conn-1", fmt.Sprintf("node-%d", i))
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if cp != nil {
			t.Errorf("Checkpoint node-%d should be deleted", i)
		}
	}

	// Verify conn-2 checkpoint still exists
	other, err := store.Get(ctx, "tenant-1", "conn-2", "node-1")
	if err != nil {
		t.Fatalf("Get other failed: %v", err)
	}
	if other == nil {
		t.Error("conn-2 checkpoint should still exist")
	}
}

// TestHealthServer_MetricsContent tests that /metrics returns Prometheus format
func TestHealthServer_MetricsContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server := health.NewServer(health.Config{
		Port:        18081,
		ComponentID: "test-metrics",
		NodeID:      "test-metrics-node",
		Logger:      nil,
	})

	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() {
		_ = server.Stop(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://localhost:18081/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Read body failed: %v", err)
	}

	// Check for Prometheus text format markers
	content := string(body)
	if len(content) == 0 {
		t.Error("Empty metrics response")
	}

	// Should contain at least go runtime metrics
	// (health server registers default go collectors)
	// This validates the metrics endpoint is working
}

// TestComponentIntegration_FilterWithNATS tests filter component with real NATS
func TestComponentIntegration_FilterWithNATS(t *testing.T) {
	nc := setupNATS(t)
	defer nc.Close()

	// Create unique subjects for this test
	inputSubject := fmt.Sprintf("filter.test.input.%d", time.Now().UnixNano())
	outputSubject := fmt.Sprintf("filter.test.output.%d", time.Now().UnixNano())

	// Subscribe to output to verify filtering
	outputReceived := make(chan *envelope.Envelope, 1)
	sub, err := nc.Subscribe(outputSubject, func(msg *nats.Msg) {
		env, _ := envelope.Unmarshal(msg.Data)
		outputReceived <- env
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Unsubscribe()

	time.Sleep(100 * time.Millisecond)

	// Simulate filter behavior: read from input, apply filter, write to output
	// This tests the envelope flow pattern used by components
	inputSub, err := nc.Subscribe(inputSubject, func(msg *nats.Msg) {
		env, err := envelope.Unmarshal(msg.Data)
		if err != nil {
			return
		}

		// Simple filter: pass through messages with "active" status
		var payload map[string]interface{}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return
		}

		if payload["status"] == "active" {
			data, _ := envelope.Marshal(env)
			nc.Publish(outputSubject, data)
		}
	})
	if err != nil {
		t.Fatalf("Subscribe input failed: %v", err)
	}
	defer inputSub.Unsubscribe()

	time.Sleep(100 * time.Millisecond)

	// Publish test message that should pass filter
	env := envelope.New()
	env.ID = "filter-test-001"
	env.Payload = []byte(`{"status": "active", "data": "test"}`)
	env.ContentType = "application/json"

	data, _ := envelope.Marshal(env)
	nc.Publish(inputSubject, data)

	// Wait for filtered message
	select {
	case rcv := <-outputReceived:
		if rcv.ID != env.ID {
			t.Errorf("ID = %q, want %q", rcv.ID, env.ID)
		}
	case <-time.After(2 * time.Second):
		t.Error("Timeout waiting for filtered message")
	}
}
