package io

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// freePort returns a port that was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return fmt.Sprintf("%d", port)
}

// TestHTTPInput_StartIsNonBlocking pins the component.Input contract: Start
// "initializes the input ... must be called before Read()", so it MUST return.
//
// Regression guard for the bug found during the #15 scaled-topology load run:
// Start served inline via ListenAndServe and blocked forever, so callers
// (cmd/consumer) never reached output.Start() and never launched their
// read→write loop. The HTTP server still accepted webhooks and answered 202,
// so the failure was silent: every message was dropped once the 100-slot
// buffer filled. A 3,000 req/s run delivered 0 messages end-to-end.
//
// The pre-existing tests all call Start() inside a goroutine, which
// accommodates a blocking Start and therefore cannot catch this — hence this
// test calls it directly and fails if it does not return.
func TestHTTPInput_StartIsNonBlocking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port := freePort(t)
	input, err := NewHTTPInput([]byte(`{"port":"` + port + `"}`))
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}
	defer input.Close()

	done := make(chan error, 1)
	go func() { done <- input.Start(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start() blocked: it must return so callers can start the output and Read()")
	}
}

// TestHTTPInput_DeliversAfterStartReturns proves the whole path works once
// Start has returned — a webhook POSTed after Start is readable via Read(),
// which is exactly what the consumer's read→write loop depends on.
func TestHTTPInput_DeliversAfterStartReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	port := freePort(t)
	input, err := NewHTTPInput([]byte(`{"port":"` + port + `"}`))
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}
	defer input.Close()

	if err := input.Start(ctx); err != nil { // called directly — must not block
		t.Fatalf("Start() error = %v", err)
	}

	url := "http://127.0.0.1:" + port + "/webhook"
	var resp *http.Response
	// The listener is bound before Start returns, but give the serve goroutine
	// a moment to begin accepting.
	for i := 0; i < 20; i++ {
		resp, err = http.Post(url, "application/json", bytes.NewReader([]byte(`{"hello":"world"}`)))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	env, err := input.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if env == nil || len(env.Payload) == 0 {
		t.Fatal("Read() returned an empty envelope")
	}
	if got := string(env.Payload); got != `{"hello":"world"}` {
		t.Errorf("payload = %s, want %s", got, `{"hello":"world"}`)
	}
}

// TestHTTPInput_StartReportsBindFailure ensures binding stays synchronous, so a
// port conflict is still surfaced as a Start() error rather than swallowed by
// the background serve goroutine.
func TestHTTPInput_StartReportsBindFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Hold the port on ALL interfaces — the input binds ":<port>", which does
	// not necessarily conflict with a 127.0.0.1-only listener on macOS.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := fmt.Sprintf("%d", ln.Addr().(*net.TCPAddr).Port)

	input, err := NewHTTPInput([]byte(`{"port":"` + port + `"}`))
	if err != nil {
		t.Fatalf("NewHTTPInput() error = %v", err)
	}
	defer input.Close()

	if err := input.Start(ctx); err == nil {
		t.Fatal("Start() = nil, want an error when the port is already in use")
	}
}
