package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/test/contract"
)

// Consumer half of the Front Systems contract. See test/contract for why these
// exist; cmd/front-systems-producer replays what this records.
//
// Front Systems is webhook-only: the consumer has no polling path, so unlike
// the other retail connectors there is exactly one shape to pin.
func TestContract_FrontSystemsEnvelopes(t *testing.T) {
	c, got, mu := newTestConsumer()
	c.resolveTenant = func(string) (string, error) { return "tenant-vr", nil }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/frontsystems/events/conn-fs",
		strings.NewReader(`{"event":"SaleCreated","saleId":8842,"storeId":17,"totalAmount":1249.00,"currency":"NOK"}`))
	c.handleWebhook()(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d, want 202", rec.Code)
	}

	mu.Lock()
	envs := make([]contract.Envelope, 0, len(*got))
	for _, env := range *got {
		envs = append(envs, contract.From("webhook", env))
	}
	mu.Unlock()

	if len(envs) != 1 {
		t.Fatalf("captured %d envelopes, want 1", len(envs))
	}
	contract.AssertRoutable(t, envs)
	contract.Golden(t, "front-systems", envs)
}
