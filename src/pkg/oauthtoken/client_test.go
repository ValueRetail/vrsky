package oauthtoken

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTokenServer mimics management-api's token endpoint and records hits.
type fakeTokenServer struct {
	srv         *httptest.Server
	hits        int64
	refreshHits int64
	expiresIn   time.Duration // how far in the future the returned expiry is
	status      int           // override status (0 = 200)
}

func newFakeTokenServer(t *testing.T) *fakeTokenServer {
	t.Helper()
	fs := &fakeTokenServer{expiresIn: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth/grants/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&fs.hits, 1)
		if r.URL.Query().Get("refresh") == "1" {
			atomic.AddInt64(&fs.refreshHits, 1)
		}
		if fs.status != 0 {
			w.WriteHeader(fs.status)
			return
		}
		exp := time.Now().Add(fs.expiresIn)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "tok-" + r.PathValue("id"),
			"expires_at":   exp,
		})
	})
	fs.srv = httptest.NewServer(mux)
	t.Cleanup(fs.srv.Close)
	return fs
}

func TestClient_NotConfigured(t *testing.T) {
	c := New("", "")
	if c.Configured() {
		t.Fatal("empty base URL + token should not be Configured")
	}
	if _, err := c.Token(context.Background(), "t1", "g1"); err == nil {
		t.Error("expected error when not configured")
	}
}

func TestClient_TokenFetchesAndCaches(t *testing.T) {
	fs := newFakeTokenServer(t)
	c := New(fs.srv.URL, "secret")

	tok, err := c.Token(context.Background(), "tenant-1", "g-1")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "tok-g-1" {
		t.Errorf("got %q want tok-g-1", tok)
	}
	// Second call should hit the cache (token not near expiry).
	if _, err := c.Token(context.Background(), "tenant-1", "g-1"); err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if got := atomic.LoadInt64(&fs.hits); got != 1 {
		t.Errorf("expected 1 HTTP hit (second served from cache), got %d", got)
	}
}

func TestClient_RefetchesWhenNearExpiry(t *testing.T) {
	fs := newFakeTokenServer(t)
	fs.expiresIn = 10 * time.Second // inside the 30s skew → always considered stale
	c := New(fs.srv.URL, "secret")

	for i := 0; i < 3; i++ {
		if _, err := c.Token(context.Background(), "tenant-1", "g-1"); err != nil {
			t.Fatalf("Token: %v", err)
		}
	}
	if got := atomic.LoadInt64(&fs.hits); got != 3 {
		t.Errorf("near-expiry token should re-fetch each call; got %d hits want 3", got)
	}
}

func TestClient_ForceTokenSendsRefreshParam(t *testing.T) {
	fs := newFakeTokenServer(t)
	c := New(fs.srv.URL, "secret")

	if _, err := c.ForceToken(context.Background(), "tenant-1", "g-1"); err != nil {
		t.Fatalf("ForceToken: %v", err)
	}
	if got := atomic.LoadInt64(&fs.refreshHits); got != 1 {
		t.Errorf("ForceToken should send refresh=1; refreshHits=%d", got)
	}
}

func TestClient_SurfacesEndpointError(t *testing.T) {
	fs := newFakeTokenServer(t)
	fs.status = http.StatusConflict // e.g. ReconnectRequired
	c := New(fs.srv.URL, "secret")

	if _, err := c.Token(context.Background(), "tenant-1", "g-1"); err == nil {
		t.Error("expected error when endpoint returns non-200")
	}
}

func TestClient_SendsAuthAndTenantHeaders(t *testing.T) {
	var gotServiceToken, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotServiceToken = r.Header.Get("X-Service-Token")
		gotTenant = r.Header.Get("X-Tenant-ID")
		exp := time.Now().Add(time.Hour)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "x", "expires_at": exp})
	}))
	defer srv.Close()

	c := New(srv.URL, "the-secret")
	if _, err := c.Token(context.Background(), "tenant-xyz", "g-1"); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if gotServiceToken != "the-secret" {
		t.Errorf("X-Service-Token = %q", gotServiceToken)
	}
	if gotTenant != "tenant-xyz" {
		t.Errorf("X-Tenant-ID = %q", gotTenant)
	}
}

func TestClient_InvalidateForcesRefetch(t *testing.T) {
	fs := newFakeTokenServer(t)
	c := New(fs.srv.URL, "secret")
	_, _ = c.Token(context.Background(), "tenant-1", "g-1") // hit 1, cached
	c.Invalidate("g-1")
	_, _ = c.Token(context.Background(), "tenant-1", "g-1") // hit 2 (cache cleared)
	if got := atomic.LoadInt64(&fs.hits); got != 2 {
		t.Errorf("Invalidate should force re-fetch; hits=%d want 2", got)
	}
}
