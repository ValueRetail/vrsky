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

// Consumer half of the Business Central contract. See test/contract.
//
// BC is poll-only over OData, and the consumer unwraps the OData envelope: the
// producer receives the `value` array, not the `{"@odata.context":…}` wrapper.
// That unwrapping is the contract — a change to it silently alters what BC
// receives on the write side.
func TestContract_BusinessCentralEnvelopes(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok-abc","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer tokenSrv.Close()

	page := `{"@odata.context":"https://api.businesscentral.dynamics.com/$metadata#items",` +
		`"value":[{"id":"9f1e-1","number":"1896-S","displayName":"ATHENS Desk","unitPrice":1000.8},` +
		`{"id":"9f1e-2","number":"1900-S","displayName":"PARIS Guest Chair","unitPrice":193.7}]}`
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("$skip") == "" || r.URL.Query().Get("$skip") == "0" {
			_, _ = w.Write([]byte(page))
			return
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer apiSrv.Close()

	c, got, mu := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiSrv.URL, TokenURL: tokenSrv.URL,
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
		WithHTTPClient(http.DefaultClient)
	if err := c.fetchAndPublish(context.Background(), "conn-bc", "tenant-vr", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}

	mu.Lock()
	envs := make([]contract.Envelope, 0, len(*got))
	for _, env := range *got {
		envs = append(envs, contract.From("poll", env))
	}
	mu.Unlock()

	if len(envs) == 0 {
		t.Fatal("captured no envelopes")
	}
	contract.AssertRoutable(t, envs)

	// The OData wrapper must not survive into the payload: the producer POSTs
	// this verbatim, and BC will not accept its own metadata envelope back.
	if err := json.Unmarshal(envs[0].Payload, &[]json.RawMessage{}); err != nil {
		t.Errorf("payload is not the unwrapped OData value array (%v) — the producer would POST BC's "+
			"metadata wrapper back to it", err)
	}
	contract.Golden(t, "business-central", envs)
}
