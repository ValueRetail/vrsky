package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Configure is where the production HTTP client is built. These tests run it
// with no client injected and look at what that client does, so replacing the
// safe client with a plain http.Client fails here and not only in prod (#678).
func configuredProducer(t *testing.T) *sapProducer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &sapProducer{} // no httpClient: Configure must build it
	if err := p.Configure(context.Background(), &sdk.Resources{
		DB:     db,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return p
}

func TestConfigure_ClientRefusesLoopback(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	p := configuredProducer(t)
	if p.allowPrivate {
		t.Fatal("allowPrivate is on without the env var")
	}
	port, accepted := tcpCounter(t)
	resp, err := p.httpClient.Get("http://127.0.0.1:" + port + "/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the configured client connected to loopback")
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
}

func TestConfigure_ClientReachesLoopbackWithDevFlag(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	p := configuredProducer(t)
	if !p.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	port, accepted := tcpCounter(t)
	if resp, err := p.httpClient.Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
	}
	if n := accepted.Load(); n == 0 {
		t.Error("with ALLOW_PRIVATE_UPSTREAMS=true the client never reached the listener")
	}
}

// The CSRF token is bound to the session cookie the fetch sets, so a delivery
// needs a cookie jar, and it must be its own: the configured client has none,
// and each session client gets a new jar on the shared transport with the
// same no-redirect policy (#678, #682).
func TestConfigure_SessionClientHasItsOwnJar(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "true")
	p := configuredProducer(t)
	if p.httpClient.Jar != nil {
		t.Error("the shared client has a cookie jar: every connection on a host would share SAP sessions")
	}
	a, b := p.sessionClient(), p.sessionClient()
	if a.Jar == nil || b.Jar == nil {
		t.Fatal("a session client has no cookie jar: the CSRF session cookie would be lost")
	}
	if a.Jar == b.Jar {
		t.Error("two session clients share one cookie jar")
	}
	if a.Transport != p.httpClient.Transport || b.Transport != p.httpClient.Transport {
		t.Error("a session client does not use the configured transport (and its safeurl dialer)")
	}
	if a.Timeout != p.httpClient.Timeout {
		t.Errorf("session client timeout = %v, want %v", a.Timeout, p.httpClient.Timeout)
	}

	// Redirects are still not followed.
	followed := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Add(1)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	resp, err := a.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || followed.Load() != 0 {
		t.Errorf("status %d, %d requests to the redirect target; want the 302 returned and none", resp.StatusCode, followed.Load())
	}
}
