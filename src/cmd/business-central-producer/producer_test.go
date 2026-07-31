package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func testProducer() *bcProducer {
	return &bcProducer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
	}
}

func tokenServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok-xyz","expires_in":3600}`)
	}))
}

func cfgFor(apiURL, tokenURL string) *BCProducerConfig {
	return &BCProducerConfig{
		AADTenantID: "t", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: apiURL, TokenURL: tokenURL,
	}
}

// TestWrite_SendsBearerAndBody verifies the POST carries the Bearer token, the
// body, the company-scoped path, and that a 2xx acks.
func TestWrite_Success(t *testing.T) {
	tok := tokenServer(t)
	defer tok.Close()

	var auth, path, body string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer api.Close()

	cfg := cfgFor(api.URL, tok.URL)
	c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
	if err := testProducer().write(context.Background(), cfg, c, []byte(`{"number":"IT-1"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if auth != "Bearer tok-xyz" {
		t.Errorf("auth = %q, want Bearer tok-xyz", auth)
	}
	if path != "/companies(GUID)/items" {
		t.Errorf("path = %q", path)
	}
	if body != `{"number":"IT-1"}` {
		t.Errorf("body = %q", body)
	}
}

// TestWrite_Classification maps HTTP status → SDK retry semantics.
func TestWrite_Classification(t *testing.T) {
	tok := tokenServer(t)
	defer tok.Close()

	cases := []struct {
		status    int
		permanent bool
		wantErr   bool
	}{
		{http.StatusCreated, false, false},
		{http.StatusBadRequest, true, true},
		{http.StatusUnauthorized, true, true},
		{http.StatusInternalServerError, false, true},
		{http.StatusServiceUnavailable, false, true},
	}
	for _, tc := range cases {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		cfg := cfgFor(api.URL, tok.URL)
		c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(http.DefaultClient)
		err := testProducer().write(context.Background(), cfg, c, []byte(`{}`))
		api.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("status %d: err=%v wantErr=%v", tc.status, err, tc.wantErr)
			continue
		}
		if tc.wantErr && sdk.IsPermanent(err) != tc.permanent {
			t.Errorf("status %d: IsPermanent=%v want %v", tc.status, sdk.IsPermanent(err), tc.permanent)
		}
	}
}
