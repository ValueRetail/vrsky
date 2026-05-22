package messaging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// Handler is the callback shape data-flow workers implement.
// Returning nil acks the message. Returning a non-nil error NAKs it with
// backoff; after MaxDeliveryAttempts the message moves to the DLQ.
type Handler func(ctx context.Context, msg *nats.Msg) error

// SubscriberOpts configures a per-worker subscription on the main stream.
type SubscriberOpts struct {
	// DurableName is the JetStream consumer name. MUST be stable across
	// restarts of the worker — that is how JS knows which messages have
	// already been acked. Convention: the worker's service name
	// ("data-filter", "http-producer", ...).
	DurableName string

	// FilterSubject narrows what this consumer receives. Default is
	// MainSubjectAll ("vrsky.data.>"), which is what every data-flow
	// worker uses today. Override only if you want a subset.
	FilterSubject string

	// MaxAckPending caps the in-flight count per consumer. Higher = more
	// parallelism per worker; lower = tighter back-pressure.
	MaxAckPending int

	// AckWait is how long JS waits for an Ack before redelivering. Should
	// be a comfortable upper bound on the worker's per-message budget.
	AckWait time.Duration

	// Backoff overrides the default redelivery schedule.
	Backoff []time.Duration

	// Logger receives structured warnings on NAK/redelivery/DLQ events.
	Logger *slog.Logger
}

// Subscriber owns a single durable JetStream consumer on the main stream.
type Subscriber struct {
	pub     *Publisher
	sub     *nats.Subscription
	opts    SubscriberOpts
	handler Handler
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// DefaultBackoff is the per-attempt delay schedule used when SubscriberOpts.
// Backoff is empty. Total time from first delivery to DLQ ≈ 156 seconds.
var DefaultBackoff = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
}

// Subscribe registers a durable consumer on the main data stream and starts
// dispatching messages to handler.
func Subscribe(js nats.JetStreamContext, opts SubscriberOpts, h Handler) (*Subscriber, error) {
	if opts.DurableName == "" {
		return nil, errors.New("messaging: DurableName is required")
	}
	if opts.FilterSubject == "" {
		opts.FilterSubject = MainSubjectAll
	}
	if opts.MaxAckPending <= 0 {
		opts.MaxAckPending = 32
	}
	if opts.AckWait <= 0 {
		opts.AckWait = 30 * time.Second
	}
	if len(opts.Backoff) == 0 {
		opts.Backoff = DefaultBackoff
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if err := EnsureStreams(js); err != nil {
		return nil, err
	}
	pub := NewPublisher(js)

	sub, err := js.PullSubscribe(opts.FilterSubject, opts.DurableName,
		nats.ManualAck(),
		nats.AckWait(opts.AckWait),
		nats.MaxAckPending(opts.MaxAckPending),
		nats.MaxDeliver(MaxDeliveryAttempts),
		nats.BackOff(opts.Backoff),
	)
	if err != nil {
		return nil, fmt.Errorf("PullSubscribe %s: %w", opts.FilterSubject, err)
	}

	s := &Subscriber{
		pub:     pub,
		sub:     sub,
		opts:    opts,
		handler: h,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go s.loop()
	return s, nil
}

// Stop signals the dispatch loop to exit and waits for it to finish. The
// JetStream consumer itself is durable and stays registered server-side.
func (s *Subscriber) Stop() {
	close(s.stopCh)
	<-s.doneCh
	_ = s.sub.Drain()
}

func (s *Subscriber) loop() {
	defer close(s.doneCh)
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		msgs, err := s.sub.Fetch(s.opts.MaxAckPending, nats.MaxWait(2*time.Second))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			s.opts.Logger.Warn("JetStream fetch failed",
				"durable", s.opts.DurableName, "error", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, m := range msgs {
			s.dispatch(m)
		}
	}
}

func (s *Subscriber) dispatch(m *nats.Msg) {
	startNs := time.Now()
	defer func() {
		if r := recover(); r != nil {
			s.opts.Logger.Error("Handler panicked",
				"durable", s.opts.DurableName, "panic", r)
			processingSeconds.WithLabelValues(s.opts.DurableName, "panic").
				Observe(time.Since(startNs).Seconds())
			_ = m.Nak()
		}
	}()

	meta, _ := m.Metadata()
	if meta != nil && meta.NumDelivered > 1 {
		redeliveryCount.WithLabelValues(s.opts.DurableName).Inc()
	}
	ctx := context.Background()
	err := s.handler(ctx, m)
	if err == nil {
		processingSeconds.WithLabelValues(s.opts.DurableName, "ok").
			Observe(time.Since(startNs).Seconds())
		_ = m.Ack()
		return
	}
	processingSeconds.WithLabelValues(s.opts.DurableName, "error").
		Observe(time.Since(startNs).Seconds())

	// Handler failed. If MaxDeliver is exhausted, copy the message to the
	// DLQ stream (with the last error in a header) then ack the original.
	if meta != nil && int(meta.NumDelivered) >= MaxDeliveryAttempts {
		tenantID, connID := parseSubject(m.Subject)
		headers := nats.Header{}
		for k, v := range m.Header {
			headers[k] = v
		}
		headers.Set("X-Vrsky-Last-Error", truncErr(err))
		headers.Set("X-Vrsky-Delivered", fmt.Sprintf("%d", meta.NumDelivered))
		headers.Set("X-Vrsky-Worker", s.opts.DurableName)
		pubErr := s.pub.PublishToDLQ(context.Background(),
			tenantID, connID,
			fmt.Sprintf("%s-%d", meta.Stream, meta.Sequence.Stream),
			m.Data, headers)
		if pubErr != nil {
			s.opts.Logger.Error("Failed to publish to DLQ; will NAK and retry",
				"durable", s.opts.DurableName, "error", pubErr)
			_ = m.Nak()
			return
		}
		dlqCount.WithLabelValues(connID, s.opts.DurableName).Inc()
		s.opts.Logger.Warn("Message moved to DLQ",
			"durable", s.opts.DurableName,
			"delivered", meta.NumDelivered,
			"last_error", err.Error())
		_ = m.Ack()
		return
	}

	s.opts.Logger.Warn("Handler error, will retry",
		"durable", s.opts.DurableName, "error", err)
	_ = m.Nak()
}

// parseSubject pulls the tenant and connection IDs out of a
// "vrsky.data.<tenant>.pipeline.<conn>" subject.
func parseSubject(subj string) (tenantID, connectionID string) {
	parts := splitN(subj, '.', 6)
	if len(parts) < 5 {
		return "", ""
	}
	return parts[2], parts[4]
}

func splitN(s string, sep byte, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(s) && len(out) < n-1; i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func truncErr(err error) string {
	s := err.Error()
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
