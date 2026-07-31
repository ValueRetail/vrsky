package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// newTestConsumer returns a consumer wired with a capturing publish func.
func newTestConsumer() (*sitooConsumer, *[]*envelope.Envelope, *sync.Mutex) {
	var mu sync.Mutex
	var got []*envelope.Envelope
	c := &sitooConsumer{
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

// TestFetchAndPublish_PaginatesAndAuthenticates verifies Basic auth is sent, the
// start/num pagination walks all pages, and each page is published.
func TestFetchAndPublish_PaginatesAndAuthenticates(t *testing.T) {
	const total = 5
	const pageSize = 2
	var authOK bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == "apiid" && p == "secretpw" {
			authOK = true
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		num, _ := strconv.Atoi(r.URL.Query().Get("num"))
		items := []json.RawMessage{}
		for i := start; i < start+num && i < total; i++ {
			items = append(items, json.RawMessage(fmt.Sprintf(`{"id":%d}`, i)))
		}
		_ = json.NewEncoder(w).Encode(sitooCollection{TotalCount: total, Items: items})
	}))
	defer srv.Close()

	c, got, mu := newTestConsumer()
	cfg := &SitooConfig{
		AccountID: 1, SiteID: 2, APIID: "apiid", APIPassword: "secretpw",
		BaseURL: srv.URL, Resource: "transactions", PageSize: pageSize,
	}
	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if !authOK {
		t.Error("Basic auth credentials were not sent")
	}
	// 5 items over pages of 2 → pages of 2,2,1 = 3 published envelopes, 5 records.
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 3 {
		t.Fatalf("published %d envelopes, want 3 (pages)", len(*got))
	}
	records := 0
	for _, env := range *got {
		var items []json.RawMessage
		if err := json.Unmarshal(env.Payload, &items); err != nil {
			t.Fatalf("payload not a JSON array: %v", err)
		}
		records += len(items)
		if env.TenantID != "tenant-1" || env.IntegrationID != "conn-1" || env.Source != "sitoo-consumer" {
			t.Errorf("bad envelope routing: %+v", env)
		}
	}
	if records != total {
		t.Errorf("published %d records, want %d", records, total)
	}
}

// TestGet_RetriesOn429 verifies the rate-limit backoff retries and then succeeds.
func TestGet_RetriesOn429(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(sitooCollection{TotalCount: 0, Items: nil})
	}))
	defer srv.Close()

	c, _, _ := newTestConsumer()
	cfg := &SitooConfig{APIID: "x", APIPassword: "y"}
	if _, err := c.get(context.Background(), cfg, srv.URL); err != nil {
		t.Fatalf("get after 429 retry: %v", err)
	}
	if calls != 2 {
		t.Errorf("server called %d times, want 2 (one 429, one success)", calls)
	}
}

// TestWebhook_PublishesBody verifies the SPI-Event webhook path resolves the
// tenant, publishes the body, and 202s — and 404s an unknown connection.
func TestWebhook_PublishesBody(t *testing.T) {
	c, got, mu := newTestConsumer()
	c.resolveTenant = func(connID string) (string, error) {
		if connID == "conn-9" {
			return "tenant-9", nil
		}
		return "", fmt.Errorf("unknown")
	}
	h := c.handleWebhook()

	// Valid connection → 202 + published.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sitoo/events/conn-9", strings.NewReader(`{"event":"transaction.created"}`))
	h(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	mu.Lock()
	n := len(*got)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("published %d, want 1", n)
	}
	if (*got)[0].TenantID != "tenant-9" || (*got)[0].IntegrationID != "conn-9" {
		t.Errorf("bad webhook routing: %+v", (*got)[0])
	}

	// Unknown connection → 404, nothing published.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/sitoo/events/nope", strings.NewReader(`{}`))
	h(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("unknown connection status = %d, want 404", rec2.Code)
	}

	// GET → 405.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/sitoo/events/conn-9", nil)
	h(rec3, req3)
	if rec3.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec3.Code)
	}
}
