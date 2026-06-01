package harness

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Options configure a harness.
type Options struct {
	Name       string  // connector name / durable (default "test-connector")
	Tenant     string  // tenant for published envelopes (default "test-tenant")
	Connection string  // connection/integration id (default "test-conn")
	DB         *sql.DB // injected into the connector (sqlmock for DB-backed connectors)
}

func (o *Options) withDefaults() {
	if o.Name == "" {
		o.Name = "test-connector"
	}
	if o.Tenant == "" {
		o.Tenant = "test-tenant"
	}
	if o.Connection == "" {
		o.Connection = "test-conn"
	}
}

// ProducerHarness drives a producer connector against an embedded JetStream.
type ProducerHarness struct {
	opts    Options
	nc      *nats.Conn
	pub     *messaging.Publisher
	dlqCh   chan *envelope.Envelope
	cancel  context.CancelFunc
	done    chan error
	cleanup func()
}

// NewProducerHarness boots an embedded JetStream and runs p through the real
// SDK runner. Call Publish to push envelopes at it and assert side effects
// with Eventually; ExpectDLQ to assert a message was dead-lettered. Stop()
// (registered via t.Cleanup) tears everything down.
func NewProducerHarness(t *testing.T, p sdk.Producer, opts Options) *ProducerHarness {
	t.Helper()
	opts.withDefaults()
	nc, js, cleanup := StartEmbeddedJetStream(t)
	if err := messaging.EnsureStreams(js); err != nil {
		cleanup()
		t.Fatalf("harness: ensure streams: %v", err)
	}

	h := &ProducerHarness{
		opts:    opts,
		nc:      nc,
		pub:     messaging.NewPublisher(js),
		dlqCh:   make(chan *envelope.Envelope, 16),
		done:    make(chan error, 1),
		cleanup: cleanup,
	}

	// Capture anything dead-lettered.
	if _, err := nc.Subscribe("vrsky.dlq.>", func(m *nats.Msg) {
		if env, err := envelope.Unmarshal(m.Data); err == nil {
			select {
			case h.dlqCh <- env:
			default:
			}
		}
	}); err != nil {
		cleanup()
		t.Fatalf("harness: dlq subscribe: %v", err)
	}

	runOpts := []sdk.RunOption{sdk.WithNATSConn(nc), sdk.WithoutHealthServer()}
	if opts.DB != nil {
		runOpts = append(runOpts, sdk.WithDB(opts.DB))
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- sdk.RunProducer(ctx, opts.Name, p, runOpts...) }()

	// Give the durable subscription a moment to register before tests publish.
	time.Sleep(150 * time.Millisecond)
	t.Cleanup(h.Stop)
	return h
}

// Publish pushes an envelope to the data stream for the producer to deliver.
// Missing tenant/connection/ID fields are filled from the harness options.
func (h *ProducerHarness) Publish(t *testing.T, env *envelope.Envelope) {
	t.Helper()
	if env.TenantID == "" {
		env.TenantID = h.opts.Tenant
	}
	if env.IntegrationID == "" {
		env.IntegrationID = h.opts.Connection
	}
	if env.ID == "" {
		env.ID = "env-" + time.Now().Format("150405.000000000")
	}
	body, err := envelope.Marshal(env)
	if err != nil {
		t.Fatalf("harness: marshal: %v", err)
	}
	if err := h.pub.Publish(context.Background(), env.TenantID, env.IntegrationID, env.ID, body); err != nil {
		t.Fatalf("harness: publish: %v", err)
	}
}

// ExpectDLQ waits for a dead-lettered envelope or fails after timeout.
func (h *ProducerHarness) ExpectDLQ(t *testing.T, timeout time.Duration) *envelope.Envelope {
	t.Helper()
	select {
	case env := <-h.dlqCh:
		return env
	case <-time.After(timeout):
		t.Fatalf("harness: expected a DLQ message within %s, got none", timeout)
		return nil
	}
}

// Stop cancels the connector and tears down the embedded server.
func (h *ProducerHarness) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
	}
	if h.cleanup != nil {
		h.cleanup()
	}
}

// WorkerHarness drives a filter or converter connector: it publishes input
// envelopes onto the data stream and captures what the connector republishes
// (also on the data stream). Because input and output share the subject, tests
// match on a field the connector sets (e.g. converter metadata) or on a fresh
// re-published ID.
type WorkerHarness struct {
	opts    Options
	nc      *nats.Conn
	pub     *messaging.Publisher
	envCh   chan *envelope.Envelope
	cancel  context.CancelFunc
	done    chan error
	cleanup func()
}

func newWorkerHarness(t *testing.T, opts Options, runner func(context.Context, []sdk.RunOption) error) *WorkerHarness {
	t.Helper()
	opts.withDefaults()
	nc, js, cleanup := StartEmbeddedJetStream(t)
	if err := messaging.EnsureStreams(js); err != nil {
		cleanup()
		t.Fatalf("harness: ensure streams: %v", err)
	}
	h := &WorkerHarness{
		opts:    opts,
		nc:      nc,
		pub:     messaging.NewPublisher(js),
		envCh:   make(chan *envelope.Envelope, 64),
		done:    make(chan error, 1),
		cleanup: cleanup,
	}
	if _, err := nc.Subscribe("vrsky.data.>", func(m *nats.Msg) {
		if env, err := envelope.Unmarshal(m.Data); err == nil {
			select {
			case h.envCh <- env:
			default:
			}
		}
	}); err != nil {
		cleanup()
		t.Fatalf("harness: data subscribe: %v", err)
	}
	runOpts := []sdk.RunOption{sdk.WithNATSConn(nc), sdk.WithoutHealthServer()}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- runner(ctx, runOpts) }()
	time.Sleep(150 * time.Millisecond)
	t.Cleanup(h.Stop)
	return h
}

// NewFilterHarness runs a filter connector.
func NewFilterHarness(t *testing.T, f sdk.Filter, opts Options) *WorkerHarness {
	opts.withDefaults()
	return newWorkerHarness(t, opts, func(ctx context.Context, ro []sdk.RunOption) error {
		return sdk.RunFilter(ctx, opts.Name, f, ro...)
	})
}

// NewConverterHarness runs a converter connector.
func NewConverterHarness(t *testing.T, c sdk.Converter, opts Options) *WorkerHarness {
	opts.withDefaults()
	return newWorkerHarness(t, opts, func(ctx context.Context, ro []sdk.RunOption) error {
		return sdk.RunConverter(ctx, opts.Name, c, ro...)
	})
}

// PublishInput pushes an input envelope onto the data stream for the
// filter/converter to consume.
func (h *WorkerHarness) PublishInput(t *testing.T, env *envelope.Envelope) {
	t.Helper()
	if env.TenantID == "" {
		env.TenantID = h.opts.Tenant
	}
	if env.IntegrationID == "" {
		env.IntegrationID = h.opts.Connection
	}
	if env.ID == "" {
		env.ID = "in-" + time.Now().Format("150405.000000000")
	}
	body, err := envelope.Marshal(env)
	if err != nil {
		t.Fatalf("harness: marshal: %v", err)
	}
	if err := h.pub.Publish(context.Background(), env.TenantID, env.IntegrationID, env.ID, body); err != nil {
		t.Fatalf("harness: publish input: %v", err)
	}
}

// ExpectEnvelope waits for a captured envelope matching m, or fails.
func (h *WorkerHarness) ExpectEnvelope(t *testing.T, m Matcher, timeout time.Duration) *envelope.Envelope {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case env := <-h.envCh:
			if m(env) {
				return env
			}
		case <-deadline:
			t.Fatalf("harness: no matching envelope within %s", timeout)
			return nil
		}
	}
}

// ExpectNone asserts no matching envelope arrives within the window (e.g. a
// filter dropped it). Note: the input itself is on the same subject, so match
// on a connector-set marker, not the input's own fields.
func (h *WorkerHarness) ExpectNone(t *testing.T, m Matcher, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case env := <-h.envCh:
			if m(env) {
				t.Fatalf("harness: expected no matching envelope, got %s", env.ID)
			}
		case <-deadline:
			return
		}
	}
}

// Stop cancels the connector and tears down the embedded server.
func (h *WorkerHarness) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
	}
	if h.cleanup != nil {
		h.cleanup()
	}
}

// ConsumerHarness drives a consumer connector and captures what it publishes.
type ConsumerHarness struct {
	opts    Options
	nc      *nats.Conn
	envCh   chan *envelope.Envelope
	cancel  context.CancelFunc
	done    chan error
	cleanup func()
}

// NewConsumerHarness boots an embedded JetStream, subscribes to the data
// stream to capture published envelopes, and runs c through the SDK runner.
func NewConsumerHarness(t *testing.T, c sdk.Consumer, opts Options) *ConsumerHarness {
	t.Helper()
	opts.withDefaults()
	nc, js, cleanup := StartEmbeddedJetStream(t)
	if err := messaging.EnsureStreams(js); err != nil {
		cleanup()
		t.Fatalf("harness: ensure streams: %v", err)
	}

	h := &ConsumerHarness{
		opts:    opts,
		nc:      nc,
		envCh:   make(chan *envelope.Envelope, 64),
		done:    make(chan error, 1),
		cleanup: cleanup,
	}

	// Capture everything the consumer publishes into the pipeline.
	if _, err := nc.Subscribe("vrsky.data.>", func(m *nats.Msg) {
		if env, err := envelope.Unmarshal(m.Data); err == nil {
			select {
			case h.envCh <- env:
			default:
			}
		}
	}); err != nil {
		cleanup()
		t.Fatalf("harness: data subscribe: %v", err)
	}

	runOpts := []sdk.RunOption{sdk.WithNATSConn(nc), sdk.WithoutHealthServer()}
	if opts.DB != nil {
		runOpts = append(runOpts, sdk.WithDB(opts.DB))
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- sdk.RunConsumer(ctx, opts.Name, c, runOpts...) }()

	t.Cleanup(h.Stop)
	return h
}

// ExpectEnvelope waits for a published envelope matching m, or fails.
func (h *ConsumerHarness) ExpectEnvelope(t *testing.T, m Matcher, timeout time.Duration) *envelope.Envelope {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case env := <-h.envCh:
			if m(env) {
				return env
			}
		case <-deadline:
			t.Fatalf("harness: no matching envelope within %s", timeout)
			return nil
		}
	}
}

// Stop cancels the connector and tears down the embedded server.
func (h *ConsumerHarness) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
	}
	if h.cleanup != nil {
		h.cleanup()
	}
}
