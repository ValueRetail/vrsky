package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

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

	// /readyz runs the runner's default upstream checks; NATS is connected so it
	// must be 200 with the nats check reported ok.
	rr, err := http.Get("http://127.0.0.1:18195/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer rr.Body.Close()
	var ready struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&ready); err != nil {
		t.Fatalf("decode /readyz response: %v", err)
	}
	if rr.StatusCode != http.StatusOK {
		t.Errorf("/readyz = %d, want 200", rr.StatusCode)
	}
	if ready.Checks["nats"] != "ok" {
		t.Errorf("/readyz checks[nats] = %q, want ok", ready.Checks["nats"])
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

// heldProducer blocks Deliver until the test releases it, then reports what
// its context said at that point and returns ctx.Err(), the way a connector
// that honours its context aborts an in-flight send.
type heldProducer struct {
	sdk.BaseProducer
	calls   int64
	started chan struct{} // cap 1: the first Deliver has begun
	release chan struct{} // closed by the test
	ctxErr  chan error    // cap 1: ctx.Err() seen by the first Deliver
}

func (p *heldProducer) Configure(ctx context.Context, res *sdk.Resources) error { return nil }
func (p *heldProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	atomic.AddInt64(&p.calls, 1)
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.release
	err := ctx.Err()
	select {
	case p.ctxErr <- err:
	default:
	}
	return err
}

// TestProducer_ShutdownDoesNotCancelInFlightDelivery guards #459. run()
// cancels runCtx and only then calls the subscriber's Stop. That order is
// harmless only because the context the SDK hands Deliver does not derive
// from runCtx. If it ever did, a shutdown would cancel the in-flight Deliver,
// the message would be NAK'd, and (as #444 showed for another caller) the
// dispatch loop could re-fetch it until it was dead-lettered. The test pins
// that a delivery in flight at shutdown keeps a live context, runs once and
// is acked, with nothing left on the durable and nothing in the DLQ.
func TestProducer_ShutdownDoesNotCancelInFlightDelivery(t *testing.T) {
	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()
	if err := messaging.EnsureStreams(js); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}

	const name = "held-producer"
	p := &heldProducer{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		ctxErr:  make(chan error, 1),
	}
	released := false
	defer func() {
		if !released {
			close(p.release)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- sdk.RunProducer(ctx, name, p, sdk.WithNATSConn(nc), sdk.WithoutHealthServer())
	}()

	env := envelope.New()
	env.ID = "held-1"
	env.TenantID = "tenant-held"
	env.IntegrationID = "conn-held"
	env.Payload = []byte("held")
	body, err := envelope.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := messaging.NewPublisher(js).Publish(context.Background(), env.TenantID, env.IntegrationID, env.ID, body); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Deliver never started")
	}

	// Shut the runner down while Deliver is in flight. Cancelling ctx cancels
	// runCtx synchronously. The short pause only lets run() reach Stop (Halt)
	// before Deliver returns, so the loop doesn't sit in a 2s Fetch; the
	// assertions hold either way.
	cancel()
	time.Sleep(50 * time.Millisecond)
	released = true
	close(p.release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunProducer did not return after ctx cancel")
	}

	select {
	case err := <-p.ctxErr:
		if err != nil {
			t.Errorf("Deliver's context was cancelled by the shutdown: %v", err)
		}
	default:
		t.Fatal("Deliver did not record its context state")
	}
	if got := atomic.LoadInt64(&p.calls); got != 1 {
		t.Errorf("Deliver ran %d times, want 1", got)
	}

	// The ack is sent asynchronously; give the server a moment to apply it.
	var info *nats.ConsumerInfo
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, err = js.ConsumerInfo(messaging.MainStreamName, name)
		if err != nil {
			t.Fatalf("consumer info: %v", err)
		}
		if info.NumPending == 0 && info.NumAckPending == 0 && info.NumRedelivered == 0 ||
			time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if info.NumPending != 0 || info.NumAckPending != 0 || info.NumRedelivered != 0 {
		t.Errorf("durable %q after shutdown: pending=%d ack_pending=%d redelivered=%d, want all 0",
			name, info.NumPending, info.NumAckPending, info.NumRedelivered)
	}
	if info.Delivered.Consumer != 1 {
		t.Errorf("durable %q delivered %d times, want 1", name, info.Delivered.Consumer)
	}

	dlq, err := js.StreamInfo(messaging.DLQStreamName)
	if err != nil {
		t.Fatalf("DLQ info: %v", err)
	}
	if dlq.State.Msgs != 0 {
		t.Errorf("shutdown dead-lettered the in-flight message: %d in the DLQ", dlq.State.Msgs)
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
