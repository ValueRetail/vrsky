package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/test/contract"
)

// Consumer half of the SAP S/4HANA contract. See test/contract.
//
// SAP is poll-only but speaks two OData dialects whose responses nest
// differently — v2 wraps records in {"d":{"results":[…]}}, v4 in {"value":[…]}.
// The consumer unwraps both to the same array, and that normalisation is the
// contract: the producer has one write path and cannot tell them apart.
func TestContract_SAPEnvelopes(t *testing.T) {
	var envs []contract.Envelope

	v2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("$skip") == "" || r.URL.Query().Get("$skip") == "0" {
			_, _ = w.Write([]byte(`{"d":{"results":[{"SalesOrder":"4500001","SoldToParty":"17","TotalNetAmount":"1249.00"},` +
				`{"SalesOrder":"4500002","SoldToParty":"17","TotalNetAmount":"199.00"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"d":{"results":[]}}`))
	}))
	defer v2.Close()

	var mu sync.Mutex
	var captured []*envelope.Envelope
	c := testConsumer(func(_ context.Context, env *envelope.Envelope) error {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, env)
		return nil
	})

	cfg2 := &SAPConfig{APIBaseURL: v2.URL, EntitySet: "A_SalesOrder", ODataVersion: "v2",
		AuthType: "basic", Username: "u", Password: "p"}
	if err := c.fetchAndPublish(context.Background(), "conn-sap", "tenant-vr", cfg2, newAuthorizer(cfg2, c.httpClient), c.logger); err != nil {
		t.Fatalf("fetchAndPublish v2: %v", err)
	}
	mu.Lock()
	for _, env := range captured {
		envs = append(envs, contract.From("poll-odata-v2", env))
	}
	captured = nil
	mu.Unlock()

	v4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("$skip") == "" || r.URL.Query().Get("$skip") == "0" {
			_, _ = w.Write([]byte(`{"value":[{"Product":"VR-TSHIRT-M-BLK","ProductType":"FERT","BaseUnit":"PC"},` +
				`{"Product":"VR-CAP-OS-RED","ProductType":"FERT","BaseUnit":"PC"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer v4.Close()

	cfg4 := &SAPConfig{APIBaseURL: v4.URL, EntitySet: "A_Product", ODataVersion: "v4",
		AuthType: "basic", Username: "u", Password: "p"}
	if err := c.fetchAndPublish(context.Background(), "conn-sap", "tenant-vr", cfg4, newAuthorizer(cfg4, c.httpClient), c.logger); err != nil {
		t.Fatalf("fetchAndPublish v4: %v", err)
	}
	mu.Lock()
	for _, env := range captured {
		envs = append(envs, contract.From("poll-odata-v4", env))
	}
	mu.Unlock()

	if len(envs) < 2 {
		t.Fatalf("captured %d envelopes, want both an OData v2 and a v4 case", len(envs))
	}
	contract.AssertRoutable(t, envs)

	// Both dialects must arrive as the same unwrapped array. If either starts
	// carrying SAP's own envelope, the producer POSTs SAP's metadata back to it.
	for _, e := range envs {
		if err := json.Unmarshal(e.Payload, &[]json.RawMessage{}); err != nil {
			t.Errorf("%s payload is not an unwrapped array (%v) — the v2/v4 normalisation is the contract",
				e.Mode, err)
		}
	}
	contract.Golden(t, "sap-s4hana", envs)
}
