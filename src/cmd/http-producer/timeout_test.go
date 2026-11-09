package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// A target that accepts the connection and never answers must fail the
// delivery within the request timeout, and the failure must be retriable (#763).

// hangingServer accepts requests and holds them until the test ends.
func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv
}

func timeoutProducer() *httpProducer {
	return &httpProducer{
		logger:          quietLogger(),
		allowPrivate:    true,
		requestTimeout:  200 * time.Millisecond,
		eventSubs:       map[string][]chan HTTPEvent{},
		recentEvents:    map[string][]HTTPEvent{},
		configCache:     map[string][]*HTTPConfig{},
		configCacheTime: map[string]time.Time{},
		configCacheTTL:  time.Hour,
	}
}

func TestSend_NoAnswerFailsWithinTheTimeout(t *testing.T) {
	srv := hangingServer(t)
	p := timeoutProducer()
	start := time.Now()
	err := p.sendHTTPRequest(context.Background(), "conn-1", &HTTPConfig{URL: srv.URL, Method: "POST"}, testEnv(), nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want an error from a target that never answers")
	}
	if elapsed > 3*time.Second {
		t.Errorf("took %v, want about the 200ms timeout", elapsed)
	}
	if sdk.IsPermanent(err) {
		t.Errorf("a timeout must be retried, got permanent error: %v", err)
	}
}

func TestSend_DefaultTimeoutIs30s(t *testing.T) {
	if got := (&httpProducer{}).newClient(nil).Timeout; got != 30*time.Second {
		t.Errorf("default client timeout = %v, want 30s", got)
	}
}

// With two nodes, the one that timed out is retried and the one that answered
// is not sent the message again.
func TestDeliver_TimedOutNodeIsNotMarkedTaken(t *testing.T) {
	hang := hangingServer(t)
	var okHits atomic.Int32
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(ok.Close)

	p := timeoutProducer()
	p.configCache["conn-1"] = []*HTTPConfig{
		{NodeID: "good", URL: ok.URL, Method: "POST"},
		{NodeID: "slow", URL: hang.URL, Method: "POST"},
	}
	p.configCacheTime["conn-1"] = time.Now()
	env := func() *envelope.Envelope { e := testEnv(); return e }()

	err := p.Deliver(context.Background(), env)
	if err == nil {
		t.Fatal("want an error: one node timed out")
	}
	if sdk.IsPermanent(err) || errors.Is(err, context.Canceled) {
		t.Fatalf("want a retriable error, got %v", err)
	}
	if err2 := p.Deliver(context.Background(), env); err2 == nil {
		t.Fatal("the slow node still hangs; want an error on the retry")
	}
	if n := okHits.Load(); n != 1 {
		t.Errorf("good node got %d requests, want 1 (the slow one must be retried, the good one skipped)", n)
	}
}
