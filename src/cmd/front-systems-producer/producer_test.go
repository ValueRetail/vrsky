package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func testProducer() *frontSystemsProducer {
	return &frontSystemsProducer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
	}
}

func cfgFor(url string) *FrontSystemsProducerConfig {
	return &FrontSystemsProducerConfig{BaseURL: url, SubscriptionKey: "sub", APIKey: "key"}
}

// TestWrite_SendsDualHeadersAndDefaultPath verifies both auth headers and the
// default bulk-upsert path, and that a 2xx is a clean ack.
func TestWrite_Success(t *testing.T) {
	var subKey, apiKey, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subKey, apiKey, path = r.Header.Get("Ocp-Apim-Subscription-Key"), r.Header.Get("x-api-key"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := testProducer().write(context.Background(), cfgFor(srv.URL), []byte(`[{"extId":"A1"}]`))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if subKey != "sub" || apiKey != "key" {
		t.Errorf("auth headers = %q / %q", subKey, apiKey)
	}
	if path != "/api/Products/bulk-upsert" {
		t.Errorf("path = %q, want /api/Products/bulk-upsert", path)
	}
	if body != `[{"extId":"A1"}]` {
		t.Errorf("body = %q", body)
	}
}

// TestWrite_Classification maps HTTP status → SDK retry semantics.
func TestWrite_Classification(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
		wantErr   bool
	}{
		{http.StatusOK, false, false},
		{http.StatusBadRequest, true, true},
		{http.StatusUnauthorized, true, true},
		{http.StatusInternalServerError, false, true},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		err := testProducer().write(context.Background(), cfgFor(srv.URL), []byte(`{}`))
		srv.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("status %d: err=%v, wantErr=%v", tc.status, err, tc.wantErr)
			continue
		}
		if tc.wantErr && sdk.IsPermanent(err) != tc.permanent {
			t.Errorf("status %d: IsPermanent=%v, want %v", tc.status, sdk.IsPermanent(err), tc.permanent)
		}
	}
}

func TestWrite_429IsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	err := testProducer().write(context.Background(), cfgFor(srv.URL), []byte(`{}`))
	if err == nil || sdk.IsPermanent(err) {
		t.Fatalf("429 should be a retriable error, got %v", err)
	}
	if d, ok := sdk.RetryAfter(err); !ok || d <= 0 {
		t.Errorf("429 should carry a retry-after hint, got ok=%v d=%v", ok, d)
	}
}
