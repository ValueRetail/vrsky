package io

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestGetMetricsHandler_WithValidGatherer tests that GetMetricsHandler returns a handler
// for a valid Gatherer
func TestGetMetricsHandler_WithValidGatherer(t *testing.T) {
	registry := prometheus.NewRegistry()
	handler, err := GetMetricsHandler(registry)

	if err != nil {
		t.Errorf("GetMetricsHandler(validRegistry) returned error: %v", err)
	}

	if handler == nil {
		t.Error("GetMetricsHandler(validRegistry) returned nil handler")
	}

	// Verify handler works by making a test request
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Handler returned status %d, want %d", w.Code, http.StatusOK)
	}
}

// TestGetMetricsHandler_WithNilGatherer tests that GetMetricsHandler returns an error
// when given a nil gatherer
func TestGetMetricsHandler_WithNilGatherer(t *testing.T) {
	handler, err := GetMetricsHandler(nil)

	if err == nil {
		t.Error("GetMetricsHandler(nil) should return an error, got nil")
	}

	if handler != nil {
		t.Error("GetMetricsHandler(nil) should return nil handler, got non-nil")
	}

	if err.Error() != "metrics gatherer cannot be nil" {
		t.Errorf("GetMetricsHandler(nil) returned unexpected error message: %v", err)
	}
}

// TestGetMetricsHandler_WithDefaultRegisterer tests that GetMetricsHandler works
// with prometheus.DefaultRegisterer (which implements Gatherer)
func TestGetMetricsHandler_WithDefaultRegisterer(t *testing.T) {
	// DefaultRegisterer implements both Registerer and Gatherer interfaces
	gatherer := prometheus.DefaultRegisterer.(prometheus.Gatherer)
	handler, err := GetMetricsHandler(gatherer)

	if err != nil {
		t.Errorf("GetMetricsHandler(DefaultRegisterer) returned error: %v", err)
	}

	if handler == nil {
		t.Error("GetMetricsHandler(DefaultRegisterer) returned nil handler")
	}
}

// TestGetMetricsHandler_ServesCorrectMetrics tests that GetMetricsHandler serves
// metrics from the provided registry, not a fallback
func TestGetMetricsHandler_ServesCorrectMetrics(t *testing.T) {
	// Create a custom registry with a specific metric
	registry := prometheus.NewRegistry()
	testCounter := prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "test_custom_metric",
			Help: "Test metric for validation",
		},
	)
	registry.MustRegister(testCounter)
	testCounter.Inc()

	// Get handler for this registry
	handler, err := GetMetricsHandler(registry)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Make a request to the handler
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Handler returned status %d, want %d", w.Code, http.StatusOK)
	}

	// Verify the response contains our custom metric (not default metrics)
	body := w.Body.String()
	if body == "" {
		t.Error("Handler returned empty metrics body")
	}

	if !stringContains(body, "test_custom_metric") {
		t.Error("Metrics output does not contain expected custom metric")
	}

	// Verify the metric value is correct (incremented once)
	if !stringContains(body, "test_custom_metric 1") {
		t.Error("Custom metric value not found or incorrect in metrics output")
	}
}

// stringContains is a helper to check if a string contains a substring
func stringContains(haystack, needle string) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
