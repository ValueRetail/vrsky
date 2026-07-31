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

func testProducer() *vismaProducer {
	return &vismaProducer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
	}
}

func tokenServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok-9","expires_in":3600}`)
	}))
}

func cfgFor(apiURL, tokenURL string) *VismaProducerConfig {
	return &VismaProducerConfig{
		BaseURL: apiURL + "/api/v3", TokenURL: tokenURL, Scope: "s", ClientID: "c", ClientSecret: "x",
		CompanyID: "7", Resource: "SalesOrders",
	}
}

func TestWrite_Success(t *testing.T) {
	tok := tokenServer()
	defer tok.Close()
	var auth, company, path, body string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, company, path = r.Header.Get("Authorization"), r.Header.Get("ipp-company-id"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer api.Close()

	cfg := cfgFor(api.URL, tok.URL)
	c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(http.DefaultClient)
	if err := testProducer().write(context.Background(), cfg, c, []byte(`{"orderNumber":"SO1"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if auth != "Bearer tok-9" {
		t.Errorf("auth = %q", auth)
	}
	if company != "7" {
		t.Errorf("ipp-company-id = %q, want 7", company)
	}
	if path != "/api/v3/SalesOrders" {
		t.Errorf("path = %q", path)
	}
	if body != `{"orderNumber":"SO1"}` {
		t.Errorf("body = %q", body)
	}
}

func TestWrite_Classification(t *testing.T) {
	tok := tokenServer()
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
		c := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(http.DefaultClient)
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
