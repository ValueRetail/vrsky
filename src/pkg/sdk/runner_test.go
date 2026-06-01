package sdk_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// --- test connectors ---

type testProducer struct {
	sdk.BaseProducer
	delivered int64
	result    func() error // per-delivery result
}

func (p *testProducer) Configure(ctx context.Context, res *sdk.Resources) error { return nil }
func (p *testProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	atomic.AddInt64(&p.delivered, 1)
	if p.result != nil {
		return p.result()
	}
	return nil
}

type testConsumer struct {
	sdk.BaseConsumer
}

func (c *testConsumer) Configure(ctx context.Context, res *sdk.Resources) error { return nil }
func (c *testConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	env := envelope.New()
	env.ID = "c-1"
	env.TenantID = "tenant-1"
	env.IntegrationID = "conn-1"
	env.Payload = []byte("hello")
	if err := publish(ctx, env); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

type testFilter struct {
	sdk.BaseFilter
	keep bool
}

func (f *testFilter) Configure(ctx context.Context, res *sdk.Resources) error { return nil }
func (f *testFilter) Evaluate(ctx context.Context, env *envelope.Envelope) (bool, *envelope.Envelope, error) {
	return f.keep, env, nil
}

type testConverter struct {
	sdk.BaseConverter
}

func (c *testConverter) Configure(ctx context.Context, res *sdk.Resources) error { return nil }
func (c *testConverter) Convert(ctx context.Context, env *envelope.Envelope) (*envelope.Envelope, error) {
	env.ContentType = "application/json"
	if env.Metadata == nil {
		env.Metadata = map[string]interface{}{}
	}
	env.Metadata["converted"] = true
	return env, nil
}

// --- tests ---

func TestProducer_DeliversAndAcks(t *testing.T) {
	p := &testProducer{}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "test-producer"})
	h.Publish(t, &envelope.Envelope{Payload: []byte("x")})

	harness.Eventually(t, 3*time.Second, "Deliver called once", func() bool {
		return atomic.LoadInt64(&p.delivered) == 1
	})
	// A successful delivery acks — no redelivery should bump the count.
	time.Sleep(500 * time.Millisecond)
	if got := atomic.LoadInt64(&p.delivered); got != 1 {
		t.Errorf("successful delivery should ack (deliver once); got %d", got)
	}
}

func TestProducer_PermanentErrorIsDropped(t *testing.T) {
	p := &testProducer{result: func() error { return sdk.Permanent(errors.New("poison")) }}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "test-producer-perm"})
	h.Publish(t, &envelope.Envelope{Payload: []byte("x")})

	harness.Eventually(t, 3*time.Second, "Deliver called", func() bool {
		return atomic.LoadInt64(&p.delivered) >= 1
	})
	// Permanent acks (drops) — must NOT be redelivered.
	time.Sleep(1500 * time.Millisecond)
	if got := atomic.LoadInt64(&p.delivered); got != 1 {
		t.Errorf("Permanent error must not be retried; delivered %d times", got)
	}
}

func TestProducer_RetriableIsRedelivered(t *testing.T) {
	p := &testProducer{result: func() error { return sdk.Retriable(errors.New("try again")) }}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "test-producer-retry"})
	h.Publish(t, &envelope.Envelope{Payload: []byte("x")})

	// First backoff is ~1s, so a redelivery should bump the count to ≥2.
	harness.Eventually(t, 6*time.Second, "Retriable error redelivered", func() bool {
		return atomic.LoadInt64(&p.delivered) >= 2
	})
}

func TestConsumer_PublishesEnvelope(t *testing.T) {
	c := &testConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "test-consumer"})
	got := h.ExpectEnvelope(t, harness.MatchTenant("tenant-1"), 3*time.Second)
	if got.ID != "c-1" || string(got.Payload) != "hello" {
		t.Errorf("unexpected published envelope: %+v", got)
	}
}

func TestConverter_TransformsAndRepublishes(t *testing.T) {
	c := &testConverter{}
	h := harness.NewConverterHarness(t, c, harness.Options{Name: "test-converter"})

	in := envelope.New()
	in.ID = "conv-in-1"
	in.TenantID = "tenant-conv"
	in.IntegrationID = "conn-1"
	in.Payload = []byte("raw")
	h.PublishInput(t, in)

	// The converter sets metadata["converted"]=true on its output; match on it
	// so we don't pick up the raw input echoed on the same subject.
	got := h.ExpectEnvelope(t, func(e *envelope.Envelope) bool {
		_, ok := e.Metadata["converted"]
		return ok
	}, 4*time.Second)
	if got.ContentType != "application/json" {
		t.Errorf("converter output content-type = %q", got.ContentType)
	}
	// Republished with a fresh ID (the JetStream dedup footgun).
	if got.ID == "conv-in-1" {
		t.Error("converter must republish with a fresh ID, not the inbound one")
	}
}

func TestFilter_DropSuppressesRepublish(t *testing.T) {
	f := &testFilter{keep: false}
	h := harness.NewFilterHarness(t, f, harness.Options{Name: "test-filter-drop"})

	in := envelope.New()
	in.ID = "filt-in-1"
	in.TenantID = "tenant-filt"
	in.IntegrationID = "conn-1"
	h.PublishInput(t, in)

	// A dropped envelope must NOT be republished. The only envelope on the
	// stream should be the input itself (same ID); no fresh-ID republish.
	h.ExpectNone(t, func(e *envelope.Envelope) bool {
		return e.ID != "filt-in-1" // a republish would carry a fresh ID
	}, 1500*time.Millisecond)
}

// httpProducer registers a custom HTTP handler in Configure, exercising the
// SDK's auxiliary HTTP server (the file-producer /files hook in miniature).
type httpProducer struct {
	sdk.BaseProducer
	delivered int64
}

func (p *httpProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	p.RegisterHTTPHandler("/probe", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	res.Health.SetReady(true)
	return nil
}
func (p *httpProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	atomic.AddInt64(&p.delivered, 1)
	return nil
}

// TestRun_ProductionPath drives the full runner: it dials NATS from NATS_URL
// (not an injected conn), starts the health server, starts the auxiliary HTTP
// server for the registered handler, subscribes, and shuts down on ctx cancel.
// Covers connectNATS / health start+stop / startAuxHTTP / run()'s env branches.
func TestRun_ProductionPath(t *testing.T) {
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()
	if err := messaging.EnsureStreams(js); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}
	t.Setenv("NATS_URL", nc.ConnectedUrl())
	t.Setenv("HEALTH_PORT", "18195")
	t.Setenv("WORKER_HTTP_PORT", "18196")

	p := &httpProducer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sdk.RunProducer(ctx, "prod-path", p) }()

	// Wait for the health server to come up.
	harness.Eventually(t, 5*time.Second, "health server up", func() bool {
		resp, err := http.Get("http://127.0.0.1:18195/health")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	// The auxiliary HTTP server serves the registered handler.
	resp, err := http.Get("http://127.0.0.1:18196/probe")
	if err != nil {
		t.Fatalf("probe aux server: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("aux /probe = %d, want 204", resp.StatusCode)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunProducer did not shut down on ctx cancel")
	}
}

// stoppableProducer overrides Stop to record that the runner invoked it during
// graceful shutdown (db-producer relies on this to close its target pools).
type stoppableProducer struct {
	sdk.BaseProducer
	stopped int64
}

func (p *stoppableProducer) Configure(ctx context.Context, res *sdk.Resources) error { return nil }
func (p *stoppableProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	return nil
}
func (p *stoppableProducer) Stop(ctx context.Context) error {
	atomic.AddInt64(&p.stopped, 1)
	return nil
}

// TestRun_CallsConnectorStop verifies the runner calls a connector's Stop on
// shutdown so it can release resources opened in Configure.
func TestRun_CallsConnectorStop(t *testing.T) {
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()
	if err := messaging.EnsureStreams(js); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}

	p := &stoppableProducer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sdk.RunProducer(ctx, "stoppable", p, sdk.WithNATSConn(nc), sdk.WithoutHealthServer())
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunProducer did not shut down")
	}
	if got := atomic.LoadInt64(&p.stopped); got != 1 {
		t.Errorf("connector Stop should be called once on shutdown; got %d", got)
	}
}

func TestMatchers(t *testing.T) {
	e := &envelope.Envelope{ID: "x", TenantID: "t"}
	if !harness.MatchAny()(e) || !harness.MatchID("x")(e) || !harness.MatchTenant("t")(e) {
		t.Error("matchers should match")
	}
	if harness.MatchID("y")(e) || harness.MatchTenant("u")(e) {
		t.Error("matchers should not match wrong values")
	}
}

// Lock in the interface shapes at compile time.
var (
	_ sdk.Filter    = (*testFilter)(nil)
	_ sdk.Converter = (*testConverter)(nil)
)
