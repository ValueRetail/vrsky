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
	"sync/atomic"
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
		allowPrivate:    true, // the httptest servers are on 127.0.0.1
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

// slowReader hands out chunks of `chunk` bytes with `gap` between them, up to
// total bytes. With stallAfter > 0 it blocks forever (until release is closed)
// once that many bytes are out.
type slowReader struct {
	total, chunk, sent, stallAfter int
	gap                            time.Duration
	release                        <-chan struct{}
}

func (r *slowReader) Read(b []byte) (int, error) {
	if r.stallAfter > 0 && r.sent >= r.stallAfter {
		<-r.release
		return 0, iolib.ErrUnexpectedEOF
	}
	if r.sent >= r.total {
		return 0, iolib.EOF
	}
	if r.sent > 0 {
		time.Sleep(r.gap)
	}
	n := min(r.chunk, r.total-r.sent, len(b))
	for i := 0; i < n; i++ {
		b[i] = 'x'
	}
	r.sent += n
	return n, nil
}

func streamingTimeoutProducer(t *testing.T, url string) *httpProducer {
	p := newStreamingProducer(t, []*HTTPConfig{{NodeID: "n1", URL: url, Method: "POST"}})
	p.requestTimeout = 200 * time.Millisecond
	return p
}

// An upload that takes several times the request timeout, but keeps moving, is
// not cut off (#774).
func TestDeliverStream_SlowUploadThatKeepsMovingSucceeds(t *testing.T) {
	var got atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := iolib.Copy(iolib.Discard, r.Body)
		got.Store(n)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p := streamingTimeoutProducer(t, srv.URL)
	const total = 12 * 100
	body := &slowReader{total: total, chunk: 100, gap: 60 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := p.DeliverStream(ctx, streamEnv(total), body); err != nil {
		t.Fatalf("slow but moving upload failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("upload took %v; the test must outlast the 200ms request timeout", elapsed)
	}
	if got.Load() != total {
		t.Errorf("server got %d bytes, want %d", got.Load(), total)
	}
}

// An upload that stops moving fails after the timeout, as a retriable error.
func TestDeliverStream_StalledUploadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = iolib.Copy(iolib.Discard, r.Body)
	}))
	t.Cleanup(srv.Close)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	p := streamingTimeoutProducer(t, srv.URL)
	body := &slowReader{total: 1000, chunk: 100, stallAfter: 100, release: release}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := p.DeliverStream(ctx, streamEnv(1000), body)
	if err == nil {
		t.Fatal("want an error from a stalled upload")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v, want about the 200ms timeout", elapsed)
	}
	if sdk.IsPermanent(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want a retriable timeout error, got %v", err)
	}
}

// A target that takes the whole body and never answers fails within the
// timeout on the streaming path too.
func TestDeliverStream_NoAnswerAfterUploadFails(t *testing.T) {
	srv := hangingServer(t)
	p := streamingTimeoutProducer(t, srv.URL)
	body := bytes.NewReader(bytes.Repeat([]byte("y"), 512))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := p.DeliverStream(ctx, streamEnv(512), body)
	if err == nil {
		t.Fatal("want an error from a target that never answers")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v, want about the 200ms timeout", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want a timeout error, got %v", err)
	}
}

// The delivery context still bounds a stream that keeps moving.
func TestDeliverStream_DeliveryContextBoundsTheWholeUpload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = iolib.Copy(iolib.Discard, r.Body)
	}))
	t.Cleanup(srv.Close)

	p := streamingTimeoutProducer(t, srv.URL)
	body := &slowReader{total: 100 * 100, chunk: 100, gap: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := p.DeliverStream(ctx, streamEnv(100*100), body); err == nil {
		t.Fatal("want an error once the delivery context ends")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v, want about the 400ms delivery context", elapsed)
	}
}
