package main

import (
	"context"
	"io"
	"log/slog"
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

// The CSRF token is bound to the session cookie, so the guarded client must
// keep the cookie jar the producer had before (#678).
func TestConfigure_ClientKeepsTheCookieJar(t *testing.T) {
	p := configuredProducer(t)
	if p.httpClient.Jar == nil {
		t.Error("the configured client has no cookie jar: the CSRF session cookie would be lost")
	}
}
