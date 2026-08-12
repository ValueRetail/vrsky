package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func testConsumer(pub sdk.PublishFunc) *sapConsumer {
	return &sapConsumer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
		publish:    pub,
	}
}

// TestConsumer_V2_Pagination covers the OData v2 envelope (d.results + __next),
// relative next-link resolution ($skiptoken), the Basic-auth header, and that
// every page is published as its own envelope.
func TestConsumer_V2_Pagination(t *testing.T) {
	var mu sync.Mutex
	var gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth, gotAccept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		mu.Unlock()
		if r.URL.Query().Get("$skiptoken") == "" {
			// page 1: two records + a RELATIVE next link (as SAP returns)
			fmt.Fprint(w, `{"d":{"results":[{"SalesOrder":"1"},{"SalesOrder":"2"}],"__next":"A_SalesOrder?$skiptoken=P2"}}`)
			return
		}
		// page 2: one record, no next
		fmt.Fprint(w, `{"d":{"results":[{"SalesOrder":"3"}]}}`)
	}))
	defer srv.Close()

	var got []*envelope.Envelope
	pub := func(_ context.Context, env *envelope.Envelope) error { got = append(got, env); return nil }
	cfg := &SAPConfig{APIBaseURL: srv.URL, EntitySet: "A_SalesOrder", ODataVersion: "v2", AuthType: "basic", Username: "u", Password: "p"}
	c := testConsumer(pub)
	auth := newAuthorizer(cfg, c.httpClient)

	if err := c.fetchAndPublish(context.Background(), "conn", "tenant", cfg, auth, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("published %d pages, want 2", len(got))
	}
	total := 0
	for _, env := range got {
		var recs []map[string]any
		if err := json.Unmarshal(env.Payload, &recs); err != nil {
			t.Fatalf("payload: %v", err)
		}
		total += len(recs)
		if env.Source != "sap-s4hana-consumer" || env.TenantID != "tenant" || env.IntegrationID != "conn" {
			t.Errorf("bad envelope: %+v", env)
		}
	}
	if total != 3 {
		t.Errorf("total records = %d, want 3", total)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p")); gotAuth != want {
		t.Errorf("auth = %q, want %q", gotAuth, want)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
}

// TestConsumer_V4 covers the OData v4 envelope (value + @odata.nextLink).
func TestConsumer_V4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"value":[{"Product":"A"},{"Product":"B"}]}`)
	}))
	defer srv.Close()

	var got []*envelope.Envelope
	pub := func(_ context.Context, env *envelope.Envelope) error { got = append(got, env); return nil }
	cfg := &SAPConfig{APIBaseURL: srv.URL, EntitySet: "A_Product", ODataVersion: "v4", AuthType: "basic", Username: "u", Password: "p"}
	c := testConsumer(pub)
	auth := newAuthorizer(cfg, c.httpClient)

	if err := c.fetchAndPublish(context.Background(), "conn", "tenant", cfg, auth, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("published %d, want 1", len(got))
	}
	var recs []map[string]any
	if err := json.Unmarshal(got[0].Payload, &recs); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("records = %d, want 2", len(recs))
	}
}

// TestEntityURL checks URL construction incl. sap-client and $filter.
func TestEntityURL(t *testing.T) {
	cfg := &SAPConfig{Host: "my1.s4hana.ondemand.com", Service: "API_SALES_ORDER_SRV", EntitySet: "A_SalesOrder", SAPClient: "100", Filter: "SalesOrder eq '1'"}
	got := cfg.entityURL()
	const wantPrefix = "https://my1.s4hana.ondemand.com/sap/opu/odata/sap/API_SALES_ORDER_SRV/A_SalesOrder?"
	if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("entityURL = %q, want prefix %q", got, wantPrefix)
	}
	// query params present (order-independent)
	for _, sub := range []string{"sap-client=100", "%24filter="} {
		if !contains(got, sub) {
			t.Errorf("entityURL %q missing %q", got, sub)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
