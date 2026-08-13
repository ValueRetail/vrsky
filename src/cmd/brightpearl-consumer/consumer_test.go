package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func newTestConsumer() (*brightpearlConsumer, *[]*envelope.Envelope, *sync.Mutex) {
	var mu sync.Mutex
	var got []*envelope.Envelope
	c := &brightpearlConsumer{
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

// TestFetch_UnwrapsResponseAndSendsStaffHeaders verifies the two staff headers
// and that the {"response": …} envelope is unwrapped before publishing.
func TestFetch_UnwrapsResponseAndSendsStaffHeaders(t *testing.T) {
	var appRef, staffTok, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appRef, staffTok, path = r.Header.Get("brightpearl-app-ref"), r.Header.Get("brightpearl-staff-token"), r.URL.Path
		fmt.Fprint(w, `{"response":{"results":[[1,"ORD-1"]]}}`)
	}))
	defer srv.Close()

	c, got, mu := newTestConsumer()
	cfg := &BrightpearlConfig{BaseURL: srv.URL, AppRef: "myapp", StaffToken: "stok", Resource: "/order-service/order-search"}
	if err := c.fetchAndPublish(context.Background(), "conn-1", "tenant-1", cfg, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if appRef != "myapp" || staffTok != "stok" {
		t.Errorf("headers = %q / %q", appRef, staffTok)
	}
	if path != "/order-service/order-search" {
		t.Errorf("path = %q", path)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 1 {
		t.Fatalf("published %d, want 1", len(*got))
	}
	if got := string((*got)[0].Payload); got != `{"results":[[1,"ORD-1"]]}` {
		t.Errorf("payload not unwrapped: %s", got)
	}
}

// TestBaseURL derives the datacenter/account URL, and honours an override.
func TestBaseURL(t *testing.T) {
	if got := (&BrightpearlConfig{Datacenter: "eu1", AccountCode: "acme"}).baseURL(); got != "https://ws-eu1.brightpearl.com/public-api/acme" {
		t.Errorf("derived base = %q", got)
	}
	if got := (&BrightpearlConfig{BaseURL: "https://x/"}).baseURL(); got != "https://x" {
		t.Errorf("override base = %q", got)
	}
	if got := (&BrightpearlConfig{}).baseURL(); got != "" {
		t.Errorf("empty base should be blank, got %q", got)
	}
}

// TestWebhook verifies publish/routing + 404/405.
func TestWebhook(t *testing.T) {
	c, got, mu := newTestConsumer()
	c.resolveTenant = func(id string) (string, error) {
		if id == "c9" {
			return "t9", nil
		}
		return "", fmt.Errorf("unknown")
	}
	h := c.handleWebhook()

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/brightpearl/events/c9", strings.NewReader(`{"e":1}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	mu.Lock()
	n := len(*got)
	mu.Unlock()
	if n != 1 || (*got)[0].TenantID != "t9" {
		t.Errorf("webhook not published/routed: n=%d", n)
	}

	rec2 := httptest.NewRecorder()
	h(rec2, httptest.NewRequest(http.MethodPost, "/brightpearl/events/nope", strings.NewReader(`{}`)))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("unknown conn = %d, want 404", rec2.Code)
	}
	rec3 := httptest.NewRecorder()
	h(rec3, httptest.NewRequest(http.MethodGet, "/brightpearl/events/c9", nil))
	if rec3.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", rec3.Code)
	}
}

// TestSampleData_Brightpearl exercises the pre-deploy /sample-data aux endpoint,
// including unwrapping the {"response": …} envelope.
func TestSampleData_Brightpearl(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"response":[{"id":1,"total":"10.00"},{"id":2,"total":"20.00"}]}`)
	}))
	defer apiSrv.Close()

	c := &brightpearlConsumer{httpClient: http.DefaultClient}
	body := fmt.Sprintf(`{"app_ref":"app","staff_token":"tok","base_url":%q,"resource":"/order-service/order"}`, apiSrv.URL)
	req := httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body))
	w := httptest.NewRecorder()
	c.handleSampleData()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		OK    bool          `json:"ok"`
		Data  []interface{} `json:"data"`
		Error string        `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || len(resp.Data) != 2 {
		t.Fatalf("want ok+2 records, got ok=%v n=%d err=%q", resp.OK, len(resp.Data), resp.Error)
	}
}
