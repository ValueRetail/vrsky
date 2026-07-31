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

func testProducer() *sitooProducer {
	return &sitooProducer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
	}
}

func cfgFor(url string) *SitooProducerConfig {
	return &SitooProducerConfig{AccountID: 1, SiteID: 2, APIID: "id", APIPassword: "pw", BaseURL: url, Resource: "warehouseitems"}
}

// TestWrite_SendsBasicAuthAndBody verifies the POST carries Basic auth, the
// JSON body, and the right URL, and that a 2xx is a clean ack (nil error).
func TestWrite_Success(t *testing.T) {
	var gotAuth, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok {
			gotAuth = u + ":" + p
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := testProducer().write(context.Background(), cfgFor(srv.URL), []byte(`[{"itemid":9,"quantity":5}]`))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if gotAuth != "id:pw" {
		t.Errorf("basic auth = %q, want id:pw", gotAuth)
	}
	if gotBody != `[{"itemid":9,"quantity":5}]` {
		t.Errorf("body = %q", gotBody)
	}
	if gotPath != "/accounts/1/sites/2/warehouseitems" {
		t.Errorf("path = %q", gotPath)
	}
}

// TestWrite_Classification maps HTTP status → SDK retry semantics.
func TestWrite_Classification(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool // true → Permanent, false → Retriable
		wantErr   bool
	}{
		{http.StatusOK, false, false},
		{http.StatusBadRequest, true, true},           // 400 poison
		{http.StatusUnauthorized, true, true},         // 401 poison (static creds)
		{http.StatusUnprocessableEntity, true, true},  // 422 poison
		{http.StatusInternalServerError, false, true}, // 5xx retry
		{http.StatusBadGateway, false, true},          // 5xx retry
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		err := testProducer().write(context.Background(), cfgFor(srv.URL), []byte(`{}`))
		srv.Close()
		if tc.wantErr && err == nil {
			t.Errorf("status %d: expected error", tc.status)
			continue
		}
		if !tc.wantErr && err != nil {
			t.Errorf("status %d: unexpected error %v", tc.status, err)
			continue
		}
		if tc.wantErr {
			if got := sdk.IsPermanent(err); got != tc.permanent {
				t.Errorf("status %d: IsPermanent=%v, want %v", tc.status, got, tc.permanent)
			}
		}
	}
}

// TestWrite_429IsRateLimited verifies a 429 is retriable with a backoff hint.
func TestWrite_429IsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	err := testProducer().write(context.Background(), cfgFor(srv.URL), []byte(`{}`))
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if sdk.IsPermanent(err) {
		t.Error("429 should be retriable, not permanent")
	}
	if d, ok := sdk.RetryAfter(err); !ok || d <= 0 {
		t.Errorf("429 should carry a retry-after hint, got ok=%v d=%v", ok, d)
	}
}
