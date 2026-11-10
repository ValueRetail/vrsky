package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err := p.sendHTTPRequest(ctx, "conn-1", &HTTPConfig{URL: srv.URL, Method: "POST"}, testEnv(), nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want an error from a target that never answers")
	}
	if elapsed > 3*time.Second {
		t.Errorf("took %v, want about the 200ms timeout", elapsed)
	}
	var ne net.Error
	if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &ne) && ne.Timeout()) {
		t.Errorf("want a timeout error, got %v", err)
	}
}

// The client carries no overall Timeout any more (#774): it counts the time
// spent writing the body, which cut off large streamed uploads that were making
// progress. Each request is bounded by its own guard instead, 30 s without
// progress by default.
func TestSend_DefaultTimeoutIs30sWithoutClientTimeout(t *testing.T) {
	p := &httpProducer{}
	if got := p.newClient(nil).Timeout; got != 0 {
		t.Errorf("client Timeout = %v, want 0 (requests are bounded by reqGuard)", got)
	}
	if got := p.timeout(); got != 30*time.Second {
		t.Errorf("default request timeout = %v, want 30s", got)
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
	env := testEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := p.Deliver(ctx, env)
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Errorf("first Deliver took %v, want about the 200ms timeout", elapsed)
	}
	if err == nil {
		t.Fatal("want an error: one node timed out")
	}
	// The SDK acks a Permanent error and NAKs anything else, so "not
	// permanent" and "caused by the timeout" is what makes it retried.
	if sdk.IsPermanent(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a retriable timeout error, got %v", err)
	}
	if err2 := p.Deliver(ctx, env); err2 == nil {
		t.Fatal("the slow node still hangs; want an error on the retry")
	}
	if n := okHits.Load(); n != 1 {
		t.Errorf("good node got %d requests, want 1 (the slow one must be retried, the good one skipped)", n)
	}
}

// A target that sends its headers and part of the body and then goes quiet is
// cut off too: the guard stays armed while the answer is read. The status is
// what counts, so a 200 is still a delivery.
func TestSend_StalledResponseBodyIsCutOff(t *testing.T) {
	cases := []struct {
		name     string
		streamed bool
	}{
		{"buffered", false},
		{"streamed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("partial"))
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(release) }) // runs first, so Close does not wait

			p := timeoutProducer()
			env := testEnv()
			var body io.Reader
			if tc.streamed {
				p.streamMinRate = 1000
				env.PayloadSize = int64(len(env.Payload))
				body = bytes.NewReader(env.Payload)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			done := make(chan error, 1)
			go func() {
				done <- p.sendHTTPRequest(ctx, "conn-1", &HTTPConfig{URL: srv.URL, Method: "POST"}, env, body)
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("a 200 is a delivery even if its body stalls, got %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("still reading a stalled response body after 3s; want about the 200ms timeout")
			}
		})
	}
}

// Fetching the OAuth token does not use up the request's timeout: the guard
// starts once the token is there (#774 review).
func TestSend_TokenFetchDoesNotCountAgainstTheTimeout(t *testing.T) {
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p := timeoutProducer() // 200 ms
	p.resolveToken = func(ctx context.Context, _, _ string, _ bool) (string, error) {
		select {
		case <-time.After(400 * time.Millisecond):
			return "tok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := &HTTPConfig{URL: srv.URL, Method: "POST", AuthType: "oauth", OAuthGrantID: "g1"}
	if err := p.sendHTTPRequest(ctx, "conn-1", cfg, testEnv(), nil); err != nil {
		t.Fatalf("a slow token fetch must not time out the request: %v", err)
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tok")
	}
}

// The error from a request the guard cut off keeps the (redacted) transport
// error and says what ran out.
func TestReqGuard_WrapKeepsTheCause(t *testing.T) {
	cause := errors.New(`Post "http://example.test/x": context canceled`)
	cases := []struct {
		name     string
		streamed bool
		limit    bool
		want     string
	}{
		{"buffered", false, false, "timed out after 10ms: context deadline exceeded"},
		{"streamed", true, false, "timed out after 10ms without progress: context deadline exceeded"},
		{"limit", true, true, "upload not finished within its 20ms limit: context deadline exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, limit := 10*time.Millisecond, time.Duration(0)
			if tc.limit {
				d, limit = time.Hour, 20*time.Millisecond
			}
			ctx, g := newReqGuard(context.Background(), d, limit, tc.streamed)
			defer g.stop()
			<-ctx.Done()
			err := g.wrap(cause)
			if want := cause.Error() + ": " + tc.want; err.Error() != want {
				t.Errorf("got %q, want %q", err, want)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("want errors.Is DeadlineExceeded, got %v", err)
			}
		})
	}
}
