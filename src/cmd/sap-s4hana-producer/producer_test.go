package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func testProducer() *sapProducer {
	jar, _ := cookiejar.New(nil)
	return &sapProducer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: &http.Client{Jar: jar},
	}
}

func basicCfg(apiURL string) *SAPProducerConfig {
	return &SAPProducerConfig{APIBaseURL: apiURL, EntitySet: "A_SalesOrder", AuthType: "basic", Username: "u", Password: "p"}
}

// TestWrite_CSRF_Success verifies the two-step SAP write: a GET fetches the
// CSRF token + session cookie, then the POST carries both plus the body.
func TestWrite_CSRF_Success(t *testing.T) {
	var gotToken, gotCookie, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.Header.Get("X-CSRF-Token") != "Fetch" {
				t.Errorf("fetch header = %q, want Fetch", r.Header.Get("X-CSRF-Token"))
			}
			http.SetCookie(w, &http.Cookie{Name: "SAP_SESSIONID", Value: "sess1"})
			w.Header().Set("X-CSRF-Token", "csrf-abc")
			w.WriteHeader(http.StatusOK)
			return
		}
		gotToken = r.Header.Get("X-CSRF-Token")
		gotCT = r.Header.Get("Content-Type")
		if ck, err := r.Cookie("SAP_SESSIONID"); err == nil {
			gotCookie = ck.Value
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	cfg := basicCfg(srv.URL)
	p := testProducer()
	auth := newAuthorizer(cfg, p.httpClient)
	if err := p.write(context.Background(), cfg, auth, []byte(`{"SalesOrderType":"OR"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if gotToken != "csrf-abc" {
		t.Errorf("POST X-CSRF-Token = %q, want csrf-abc", gotToken)
	}
	if gotCookie != "sess1" {
		t.Errorf("POST session cookie = %q, want sess1", gotCookie)
	}
	if gotBody != `{"SalesOrderType":"OR"}` {
		t.Errorf("body = %q", gotBody)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
}

// TestWrite_CSRF_Retry verifies a 403 "x-csrf-token: Required" triggers a
// re-fetch and a successful retry.
func TestWrite_CSRF_Retry(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("X-CSRF-Token", "tok")
			w.WriteHeader(http.StatusOK)
			return
		}
		posts++
		if posts == 1 {
			w.Header().Set("X-CSRF-Token", "Required")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	cfg := basicCfg(srv.URL)
	p := testProducer()
	auth := newAuthorizer(cfg, p.httpClient)
	if err := p.write(context.Background(), cfg, auth, []byte(`{}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if posts != 2 {
		t.Errorf("POST attempts = %d, want 2 (retry after 403 Required)", posts)
	}
}

// TestWrite_Classification maps HTTP status → SDK retry semantics.
func TestWrite_Classification(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
		wantErr   bool
	}{
		{http.StatusCreated, false, false},
		{http.StatusBadRequest, true, true},
		{http.StatusUnauthorized, true, true},
		{http.StatusTooManyRequests, false, true},
		{http.StatusInternalServerError, false, true},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.Header().Set("X-CSRF-Token", "t")
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(tc.status)
		}))
		cfg := basicCfg(srv.URL)
		p := testProducer()
		auth := newAuthorizer(cfg, p.httpClient)
		err := p.write(context.Background(), cfg, auth, []byte(`{}`))
		srv.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("status %d: err=%v wantErr=%v", tc.status, err, tc.wantErr)
			continue
		}
		if tc.wantErr && sdk.IsPermanent(err) != tc.permanent {
			t.Errorf("status %d: IsPermanent=%v want %v", tc.status, sdk.IsPermanent(err), tc.permanent)
		}
	}
}
