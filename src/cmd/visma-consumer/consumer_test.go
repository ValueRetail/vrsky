package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

func newTestConsumer() (*vismaConsumer, *[]*envelope.Envelope, *sync.Mutex) {
	var mu sync.Mutex
	var got []*envelope.Envelope
	c := &vismaConsumer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
		publish: func(_ context.Context, env *envelope.Envelope) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, env)
			return nil
		},
	}
	return c, &got, &mu
}

// TestFetchAndPublish_ArrayWithBearerAndCompanyHeader verifies the Bearer token,
// the ipp-company-id header, and that a JSON-array body is published as-is.
func TestFetchAndPublish_ArrayWithBearerAndCompanyHeader(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok-1","expires_in":3600}`)
	}))
	defer tokenSrv.Close()

	var auth, company, path string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, company, path = r.Header.Get("Authorization"), r.Header.Get("ipp-company-id"), r.URL.Path
		fmt.Fprint(w, `[{"orderNumber":"SO1"},{"orderNumber":"SO2"}]`)
	}))
	defer apiSrv.Close()

	c, got, mu := newTestConsumer()
	cfg := &VismaConfig{
		BaseURL: apiSrv.URL + "/api/v3", TokenURL: tokenSrv.URL, Scope: "visma-net", ClientID: "cid",
		ClientSecret: "sec", CompanyID: "99", Resource: "SalesOrders",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(http.DefaultClient)
	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if auth != "Bearer tok-1" {
		t.Errorf("auth = %q", auth)
	}
	if company != "99" {
		t.Errorf("ipp-company-id = %q, want 99", company)
	}
	if path != "/api/v3/SalesOrders" {
		t.Errorf("path = %q", path)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 1 {
		t.Fatalf("published %d, want 1", len(*got))
	}
	var recs []map[string]any
	if err := json.Unmarshal((*got)[0].Payload, &recs); err != nil || len(recs) != 2 {
		t.Errorf("payload not the 2-record array: %v / %d", err, len(recs))
	}
}

// TestFetchAndPublish_SingleObjectWrapped verifies a single-object body is
// wrapped in a one-element array so downstream always sees a list.
func TestFetchAndPublish_SingleObjectWrapped(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"t","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"orderNumber":"SO9"}`)
	}))
	defer apiSrv.Close()

	c, got, mu := newTestConsumer()
	cfg := &VismaConfig{BaseURL: apiSrv.URL, TokenURL: tokenSrv.URL, Scope: "s", ClientID: "c", ClientSecret: "x", Resource: "customer"}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(http.DefaultClient)
	if err := c.fetchAndPublish(context.Background(), "conn", "tenant", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var recs []map[string]any
	if err := json.Unmarshal((*got)[0].Payload, &recs); err != nil || len(recs) != 1 {
		t.Errorf("single object not wrapped into a 1-element array: %v / %d", err, len(recs))
	}
}
