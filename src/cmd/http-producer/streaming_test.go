package main

import (
	"bytes"
	"context"
	"errors"
	iolib "io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// newStreamingProducer builds a producer whose config lookup is stubbed, so the
// streaming decisions can be exercised without a management DB.
func newStreamingProducer(t *testing.T, configs []*HTTPConfig) *httpProducer {
	t.Helper()
	p := &httpProducer{
		logger:          slog.New(slog.NewTextHandler(iolib.Discard, nil)),
		configCache:     map[string][]*HTTPConfig{"conn-1": configs},
		configCacheTime: map[string]time.Time{"conn-1": time.Now()},
		configCacheTTL:  time.Hour,
		eventSubs:       map[string][]chan HTTPEvent{},
		recentEvents:    map[string][]HTTPEvent{},
	}
	return p
}

func streamEnv(payloadSize int64) *envelope.Envelope {
	return &envelope.Envelope{
		ID:            "env-1",
		TenantID:      "tenant-x",
		IntegrationID: "conn-1",
		ContentType:   "text/csv",
		PayloadSize:   payloadSize,
	}
}

func TestDeliverStream_StreamsBodyWithContentLength(t *testing.T) {
	payload := bytes.Repeat([]byte("S"), 4096)

	var (
		mu       sync.Mutex
		gotBody  []byte
		gotLen   int64
		gotType  string
		gotMsgID string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := iolib.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotLen, gotType, gotMsgID = b, r.ContentLength, r.Header.Get("Content-Type"), r.Header.Get("X-Message-ID")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newStreamingProducer(t, []*HTTPConfig{{URL: srv.URL, Method: "POST"}})
	env := streamEnv(int64(len(payload)))

	if err := p.DeliverStream(context.Background(), env, bytes.NewReader(payload)); err != nil {
		t.Fatalf("DeliverStream: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !bytes.Equal(gotBody, payload) {
		t.Errorf("server received %d bytes, want %d", len(gotBody), len(payload))
	}
	// Content-Length rather than chunked encoding: some endpoints reject chunked.
	if gotLen != int64(len(payload)) {
		t.Errorf("Content-Length = %d, want %d", gotLen, len(payload))
	}
	if gotType != "text/csv" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if gotMsgID != "env-1" {
		t.Errorf("X-Message-ID = %q", gotMsgID)
	}
}

// Fan-out needs several passes over a body that reads once — decline so the SDK
// falls back to buffered delivery.
func TestDeliverStream_DeclinesFanOut(t *testing.T) {
	p := newStreamingProducer(t, []*HTTPConfig{
		{URL: "http://a.invalid", Method: "POST"},
		{URL: "http://b.invalid", Method: "POST"},
	})
	err := p.DeliverStream(context.Background(), streamEnv(10), bytes.NewReader([]byte("x")))
	if !errors.Is(err, sdk.ErrStreamUnsupported) {
		t.Fatalf("expected ErrStreamUnsupported for fan-out, got %v", err)
	}
}

// The oauth 401 handler re-sends the request; a stream cannot be replayed, so
// oauth endpoints stay on the buffered path.
func TestDeliverStream_DeclinesOAuth(t *testing.T) {
	p := newStreamingProducer(t, []*HTTPConfig{
		{URL: "http://a.invalid", Method: "POST", AuthType: "oauth", OAuthGrantID: "g1"},
	})
	err := p.DeliverStream(context.Background(), streamEnv(10), bytes.NewReader([]byte("x")))
	if !errors.Is(err, sdk.ErrStreamUnsupported) {
		t.Fatalf("expected ErrStreamUnsupported for oauth, got %v", err)
	}
}

// A 5xx is retriable: the SDK NAKs and JetStream redelivery re-opens the stream.
func TestDeliverStream_UpstreamErrorIsRetriable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = iolib.Copy(iolib.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newStreamingProducer(t, []*HTTPConfig{{URL: srv.URL, Method: "POST"}})
	err := p.DeliverStream(context.Background(), streamEnv(1), bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected an error for a 5xx response")
	}
	if sdk.IsPermanent(err) {
		t.Error("a 5xx must stay retriable so redelivery can re-open the stream")
	}
}

// No matching endpoint is not an error — this binary just isn't the producer.
func TestDeliverStream_NoEligibleConfigIsNoOp(t *testing.T) {
	p := newStreamingProducer(t, []*HTTPConfig{
		{URL: "http://a.invalid", Method: "POST", PredecessorID: "other-node"},
	})
	if err := p.DeliverStream(context.Background(), streamEnv(1), bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("expected a no-op, got %v", err)
	}
}
