package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/test/contract"
)

// Consumer half of the Brightpearl contract. See test/contract.
//
// Brightpearl has both paths, and they do not agree: polling publishes the
// unwrapped response, the webhook forwards Brightpearl's callback body. The
// producer POSTs whichever it gets, so the difference is part of the contract
// rather than an implementation detail.
func TestContract_BrightpearlEnvelopes(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "firstResult=1") || r.URL.RawQuery == "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":{"results":[[45001,"2026-09-09T08:14:22Z","SO-4501","1249.00"],[45002,"2026-09-09T08:19:03Z","SO-4502","199.00"]]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"results":[]}}`))
	}))
	defer api.Close()

	c, got, mu := newTestConsumer()
	cfg := &BrightpearlConfig{BaseURL: api.URL, AppRef: "vrsky", StaffToken: "stok", Resource: "/order-service/order-search"}
	if err := c.fetchAndPublish(context.Background(), "conn-bp", "tenant-vr", cfg, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	pollCount := len(*got)

	c.resolveTenant = func(string) (string, error) { return "tenant-vr", nil }
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/brightpearl/events/conn-bp",
		strings.NewReader(`{"lifecycleEvent":"order.created","resourceType":"order","id":"45001"}`))
	c.handleWebhook()(rec, req)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want 202/200", rec.Code)
	}

	mu.Lock()
	envs := make([]contract.Envelope, 0, len(*got))
	for i, env := range *got {
		mode := "poll"
		if i >= pollCount {
			mode = "webhook"
		}
		envs = append(envs, contract.From(mode, env))
	}
	mu.Unlock()

	if len(envs) < 2 {
		t.Fatalf("captured %d envelopes, want at least one poll and one webhook", len(envs))
	}
	contract.AssertRoutable(t, envs)

	// The poll payload must stay parseable as JSON the producer can forward.
	var any1 any
	if err := json.Unmarshal(envs[0].Payload, &any1); err != nil {
		t.Errorf("poll payload is not JSON: %v", err)
	}
	contract.Golden(t, "brightpearl", envs)
}
