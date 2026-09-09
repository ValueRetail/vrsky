package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/test/contract"
)

// Consumer half of the Visma contract. See test/contract.
//
// Visma's API answers some resources with an array and others with a single
// object; the consumer wraps the latter so the producer always receives an
// array. Both shapes are pinned here, because that normalisation is the whole
// reason the producer can treat every Visma payload the same way.
func TestContract_VismaEnvelopes(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok-abc","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer tokenSrv.Close()

	var envs []contract.Envelope

	// Collection response.
	arraySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"customerNumber":"10001","name":"Value Retail AS","currencyId":"NOK"},` +
			`{"customerNumber":"10002","name":"Bicester Village Ltd","currencyId":"GBP"}]`))
	}))
	defer arraySrv.Close()

	c, got, mu := newTestConsumer()
	cfg := &VismaConfig{
		BaseURL: arraySrv.URL + "/api/v3", TokenURL: tokenSrv.URL, Scope: "visma-net",
		ClientID: "cid", ClientSecret: "sec", CompanyID: "7", Resource: "customer",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).
		WithHTTPClient(http.DefaultClient)
	if err := c.fetchAndPublish(context.Background(), "conn-visma", "tenant-vr", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish (array): %v", err)
	}
	mu.Lock()
	for _, env := range *got {
		envs = append(envs, contract.From("poll-array", env))
	}
	*got = nil
	mu.Unlock()

	// Single-object response — the consumer wraps it.
	objSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"customerNumber":"10003","name":"La Vallee Village","currencyId":"EUR"}`))
	}))
	defer objSrv.Close()

	cfg2 := &VismaConfig{
		BaseURL: objSrv.URL + "/api/v3", TokenURL: tokenSrv.URL, Scope: "visma-net",
		ClientID: "cid", ClientSecret: "sec", CompanyID: "7", Resource: "customer",
	}
	if err := c.fetchAndPublish(context.Background(), "conn-visma", "tenant-vr", cfg2, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish (object): %v", err)
	}
	mu.Lock()
	for _, env := range *got {
		envs = append(envs, contract.From("poll-object", env))
	}
	mu.Unlock()

	if len(envs) < 2 {
		t.Fatalf("captured %d envelopes, want both an array and a wrapped-object case", len(envs))
	}
	contract.AssertRoutable(t, envs)

	// Both shapes must reach the producer as arrays; that normalisation is what
	// lets one write path serve both.
	for _, e := range envs {
		if err := json.Unmarshal(e.Payload, &[]json.RawMessage{}); err != nil {
			t.Errorf("%s payload is not a JSON array (%v) — the single-object wrap is the contract", e.Mode, err)
		}
	}
	contract.Golden(t, "visma", envs)
}
