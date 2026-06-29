package messaging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

	// AckWait is how long JS waits for an Ack before redelivering. It is the
	// crash-recovery window, NOT the per-message time budget: while a handler
	// runs, the dispatch loop sends periodic InProgress heartbeats that reset
	// this timer (#139), so a handler may legitimately run far longer than
	// AckWait without triggering a duplicate delivery. Leave it small for fast
	// recovery when a worker actually dies. Raising it is allowed (see the
	// Backoff[0] alignment note in Subscribe) and reconciles an existing
	// durable in place.
	AckWait time.Duration

	// Backoff overrides the default redelivery schedule. Backoff[0] doubles as
	// the consumer's effective AckWait in JetStream (see Subscribe).
	Backoff []time.Duration

	// HeartbeatInterval overrides how often an in-flight message is marked
	// InProgress to reset its AckWait timer (#139). Default: AckWait/2, floored
	// at 250ms. Must be < AckWait or redelivery can fire between heartbeats.
	HeartbeatInterval time.Duration

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
	if len(opts.Backoff) == 0 {
		opts.Backoff = DefaultBackoff
	}
	// JetStream derives a consumer's effective AckWait from Backoff[0] when a
	// backoff schedule is supplied. If we request a *different* AckWait, the
	// consumer is created storing Backoff[0] — but every later re-subscribe
	// then fails with "configuration requests ack wait to be X, but consumer's
	// value is Y", crash-looping the worker on restart (#99). So AckWait and
	// Backoff[0] must always agree.
	//
	// #139 keeps them aligned while making AckWait *tunable*: a caller that
	// explicitly raises AckWait above the first backoff step gets that value
	// promoted to Backoff[0] (and reconcileAckWait below updates any existing
	// durable in place, so the raise doesn't reintroduce #99). When AckWait is
	// left unset, the default schedule's first step wins — identical to the
	// previous behavior, so existing durables re-bind with no reconcile.
	opts.Backoff = append([]time.Duration(nil), opts.Backoff...) // copy; never mutate DefaultBackoff
	if opts.AckWait > opts.Backoff[0] {
		opts.Backoff[0] = opts.AckWait
		// Keep the schedule non-decreasing so later (shorter) steps don't
		// redeliver sooner than the ack window.
		for i := 1; i < len(opts.Backoff); i++ {
			if opts.Backoff[i] < opts.Backoff[0] {
				opts.Backoff[i] = opts.Backoff[0]
			}
		}
	}
	opts.AckWait = opts.Backoff[0]
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if err := EnsureStreams(js); err != nil {
		return nil, err
	}
	// If a durable already exists with a different ack-wait (e.g. AckWait was
	// just raised, or a pre-#139 consumer is stored at 1s), update it in place
	// so the PullSubscribe below binds cleanly instead of erroring on the
	// mismatch (#99). Best-effort: a missing consumer is created fresh by
	// PullSubscribe; a transient error here surfaces on the bind attempt.
	if err := reconcileAckWait(js, opts); err != nil {
		opts.Logger.Warn("could not reconcile consumer ack wait before bind",
			"durable", opts.DurableName, "error", err)
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

// reconcileAckWait updates an already-existing durable's AckWait/BackOff to the
// requested values so a re-subscribe binds cleanly. It is a no-op when the
// consumer doesn't exist yet (PullSubscribe will create it) or already matches.
func reconcileAckWait(js nats.JetStreamContext, opts SubscriberOpts) error {
	ci, err := js.ConsumerInfo(MainStreamName, opts.DurableName)
	if err != nil {
		// Not found / transient: nothing to reconcile. PullSubscribe creates
		// the consumer, or surfaces a real error on bind.
		return nil //nolint:nilerr // intentional: absence is not an error here
	}
	if ci.Config.AckWait == opts.AckWait {
		return nil
	}
	cfg := ci.Config
	cfg.AckWait = opts.AckWait
	cfg.BackOff = opts.Backoff
	if _, err := js.UpdateConsumer(MainStreamName, &cfg); err != nil {
		return err
	}
	opts.Logger.Info("reconciled durable ack wait",
		"durable", opts.DurableName,
		"old_ack_wait", ci.Config.AckWait, "new_ack_wait", opts.AckWait)
	return nil
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

// heartbeatInterval returns how often to mark an in-flight message InProgress.
// Default is AckWait/2 (floored at 250ms) so the redelivery timer is reset with
// margin even if a tick is slightly late; an explicit HeartbeatInterval wins.
func (s *Subscriber) heartbeatInterval() time.Duration {
	if s.opts.HeartbeatInterval > 0 {
		return s.opts.HeartbeatInterval
	}
	hb := s.opts.AckWait / 2
	if hb < 250*time.Millisecond {
		hb = 250 * time.Millisecond
	}
	return hb
}

// startHeartbeat launches a goroutine that calls m.InProgress() on a ticker,
// resetting the message's AckWait timer until the returned stop func is called.
// The stop func is idempotent (safe to call from both the normal path and a
// deferred panic-safety net).
func (s *Subscriber) startHeartbeat(m *nats.Msg) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(s.heartbeatInterval())
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				// Best-effort: if the message is already terminal (acked/nakked)
				// or the connection is gone, stop heartbeating.
				if err := m.InProgress(); err != nil {
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
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
	// Keep this message in-flight for as long as the handler runs: periodic
	// InProgress() calls reset the server-side AckWait timer so JetStream never
	// redelivers a message we're still processing (#139). This decouples the
	// handler's wall-clock budget from AckWait, so a slow producer target /
	// large payload / bulk API can take far longer than AckWait without causing
	// a duplicate downstream delivery.
	stopHB := s.startHeartbeat(m)
	defer stopHB() // panic-safety net; idempotent, no-op after the explicit stop
	ctx := context.Background()
	err := s.handler(ctx, m)
	stopHB() // stop before Ack/Nak in the normal path
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
