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
func configuredConsumer(t *testing.T) *sapConsumer {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &sapConsumer{} // no httpClient: Configure must build it
	if err := c.Configure(context.Background(), &sdk.Resources{
		DB:     db,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return c
}

func TestConfigure_ClientRefusesLoopback(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_UPSTREAMS", "")
	c := configuredConsumer(t)
	if c.allowPrivate {
		t.Fatal("allowPrivate is on without the env var")
	}
	port, accepted := tcpCounter(t)
	resp, err := c.httpClient.Get("http://127.0.0.1:" + port + "/")
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
	c := configuredConsumer(t)
	if !c.allowPrivate {
		t.Fatal("allowPrivate is off with the env var set")
	}
	port, accepted := tcpCounter(t)
	if resp, err := c.httpClient.Get("http://127.0.0.1:" + port + "/"); err == nil {
		_ = resp.Body.Close()
	}
	if n := accepted.Load(); n == 0 {
		t.Error("with ALLOW_PRIVATE_UPSTREAMS=true the client never reached the listener")
	}
}
