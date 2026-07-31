package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func newTestConsumer() (*frontSystemsConsumer, *[]*envelope.Envelope, *sync.Mutex) {
	var mu sync.Mutex
	var got []*envelope.Envelope
	c := &frontSystemsConsumer{
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

// TestWebhook_PublishesAndExtractsEvent verifies the webhook path resolves the
// tenant, publishes the body, extracts the event type, and 202s.
func TestWebhook_PublishesAndExtractsEvent(t *testing.T) {
	c, got, mu := newTestConsumer()
	c.resolveTenant = func(connID string) (string, error) {
		if connID == "conn-1" {
			return "tenant-1", nil
		}
		return "", errUnknown
	}
	h := c.handleWebhook()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/frontsystems/events/conn-1",
		strings.NewReader(`{"event":"SaleCreated","saleId":42}`))
	h(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 1 {
		t.Fatalf("published %d, want 1", len(*got))
	}
	env := (*got)[0]
	if env.TenantID != "tenant-1" || env.IntegrationID != "conn-1" {
		t.Errorf("bad routing: %+v", env)
	}
	if env.Metadata["event_type"] != "SaleCreated" {
		t.Errorf("event_type = %v, want SaleCreated", env.Metadata["event_type"])
	}
}

func TestWebhook_UnknownConnAnd405(t *testing.T) {
	c, _, _ := newTestConsumer()
	c.resolveTenant = func(string) (string, error) { return "", errUnknown }
	h := c.handleWebhook()

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/frontsystems/events/nope", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown conn = %d, want 404", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	h(rec2, httptest.NewRequest(http.MethodGet, "/frontsystems/events/x", nil))
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", rec2.Code)
	}
}

// TestRegisterWebhooks_SendsDualHeaders verifies webhook registration sends both
// auth headers and one POST /api/webhooks per configured event.
func TestRegisterWebhooks_SendsDualHeaders(t *testing.T) {
	var subKey, apiKey string
	var registered []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subKey = r.Header.Get("Ocp-Apim-Subscription-Key")
		apiKey = r.Header.Get("x-api-key")
		b, _ := io.ReadAll(r.Body)
		registered = append(registered, string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _, _ := newTestConsumer()
	cfg := &FrontSystemsConfig{
		BaseURL: srv.URL, SubscriptionKey: "sub-123", APIKey: "key-456",
		CallbackURL: "https://cb.example/frontsystems/events/conn-1",
		Events:      []string{"SaleCreated", "StockMovementCreated"},
	}
	if err := c.registerWebhooks(context.Background(), cfg, c.logger); err != nil {
		t.Fatalf("registerWebhooks: %v", err)
	}
	if subKey != "sub-123" || apiKey != "key-456" {
		t.Errorf("auth headers = %q / %q, want sub-123 / key-456", subKey, apiKey)
	}
	if len(registered) != 2 {
		t.Fatalf("registered %d events, want 2", len(registered))
	}
	if !strings.Contains(registered[0], "SaleCreated") || !strings.Contains(registered[0], "cb.example") {
		t.Errorf("registration payload = %s", registered[0])
	}
}

var errUnknown = errString("unknown")

type errString string

func (e errString) Error() string { return string(e) }
