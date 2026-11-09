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

	// DeliverPolicy sets where a NEWLY CREATED consumer starts. The zero
	// value is JetStream's default, DeliverAll: everything still in the
	// stream, which is the replay a restarted worker relies on. A consumer
	// created for something that joined late — an agent added to a group
	// while its pipeline runs — wants nats.DeliverNewPolicy instead, or it
	// would receive up to MainRetention of history. It is fixed at creation
	// and ignored for an existing consumer.
	DeliverPolicy nats.DeliverPolicy

	// Logger receives structured warnings on NAK/redelivery/DLQ events.
	Logger *slog.Logger
}

// DeleteConsumer removes a durable for good. Callers use it when the owner is
// gone for good too — a revoked agent's per-member consumer — so nothing keeps
// holding messages for it until the inactive threshold. A missing consumer is
// not an error.
func DeleteConsumer(js nats.JetStreamContext, durable string) error {
	err := js.DeleteConsumer(MainStreamName, durable)
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return nil
	}
	return err
}

// Subscriber owns a single durable JetStream consumer on the main stream.
type Subscriber struct {
	pub     *Publisher
	sub     *nats.Subscription
	opts    SubscriberOpts
	handler Handler
	stopCh  chan struct{}
	doneCh  chan struct{}
	halt    sync.Once
}

// pollWait is how long the server holds each pull request open (its
// "expires"). The loop polls in rounds this short so that Halt and Stop wait
// out at most one of them: an idle Stop used to wait out a two-second fetch
// (#494). Shorter costs more requests: an idle subscriber sends one pull
// request per pollWait, and the server answers each with a 408 when it
// expires. A consumer with a short AckWait polls shorter still, see poll.
const pollWait = 150 * time.Millisecond

// fetchWait is how long the client listens for one fetch to end. The fetch
// normally ends on the server's word, long before this: the batch filled, or
// the 408 that says the pull request expired. fetchWait only covers an answer
// that never comes (a broken connection). It is also the margin by which a
// pull request may reach the server late and still be over before the fetch
// is, which is what lets Stop drain without stranding a message.
const fetchWait = 2 * time.Second

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
	// The consumer is created (or reconciled) explicitly and the subscription
	// BINDS to it, rather than letting PullSubscribe own it. The difference is
	// what happens on Stop: a subscription that created its own durable deletes
	// it when drained, and a recreated consumer starts from DeliverAll — so
	// every graceful restart replayed everything still in the stream, up to
	// MainRetention. Measured: gone 50ms after Stop; 5 of 5 redelivered where
	// 2 were new. A bound subscription leaves the consumer, and its position,
	// where they are.
	if err := ensureConsumer(js, opts); err != nil {
		return nil, fmt.Errorf("ensure consumer %s: %w", opts.DurableName, err)
	}
	pub := NewPublisher(js)

	sub, err := js.PullSubscribe(opts.FilterSubject, "",
		nats.Bind(MainStreamName, opts.DurableName),
		nats.ManualAck(),
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

// consumerConfig is the consumer this subscriber wants, in full.
func consumerConfig(opts SubscriberOpts) *nats.ConsumerConfig {
	return &nats.ConsumerConfig{
		Durable:       opts.DurableName,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       opts.AckWait,
		BackOff:       opts.Backoff,
		MaxAckPending: opts.MaxAckPending,
		MaxDeliver:    MaxDeliveryAttempts,
		FilterSubject: opts.FilterSubject,
		DeliverPolicy: opts.DeliverPolicy,
		// Durables now survive Stop, so nothing deletes one whose owner has
		// gone for good — a tenant-consumer bridge per connection, say, after
		// the connection is deleted. JetStream removes a consumer nobody has
		// bound to for this long. It is deliberately much longer than any
		// restart or outage: a consumer removed this way is recreated at
		// DeliverAll on return, which is the replay this package exists to
		// avoid, so the threshold must only ever fire for the abandoned.
		InactiveThreshold: ConsumerInactiveThreshold,
	}
}

// ConsumerInactiveThreshold is how long an unbound durable lives before
// JetStream reaps it. A week: long past any plausible outage, so a connector
// that comes back keeps its position, while consumers whose owner was deleted
// do not accumulate forever.
const ConsumerInactiveThreshold = 7 * 24 * time.Hour

// ensureConsumer makes the durable match consumerConfig(opts) — created if
// absent, updated in place if it differs. Nothing here ever deletes a
// consumer; deletion is what loses the position.
//
// Two histories meet here. #99: JetStream stores AckWait = BackOff[0], so a
// re-subscribe requesting a different AckWait crash-looped on the mismatch.
// #139: AckWait became tunable, and an existing durable is reconciled to the
// raised value rather than erroring. Both used to be handled around
// PullSubscribe's own config validation; now that the subscription binds
// instead of owning the consumer, there is no client-side validation to trip
// — so EVERY field has to be reconciled here, not just AckWait. The first
// version of this reconciled AckWait alone, which meant a changed
// MaxAckPending or MaxDeliver was silently ignored on every restart, and a
// changed FilterSubject failed the bind outright with the consumer stuck
// until someone deleted it by hand. Deleting on Stop had been masking both.
//
// A durable created by an earlier version of this code (PullSubscribe-owned)
// is just an existing consumer here: it is reconciled and bound to, with its
// position intact.
func ensureConsumer(js nats.JetStreamContext, opts SubscriberOpts) error {
	want := consumerConfig(opts)

	ci, err := js.ConsumerInfo(MainStreamName, opts.DurableName)
	if err != nil {
		if !errors.Is(err, nats.ErrConsumerNotFound) {
			return err
		}
		_, err := js.AddConsumer(MainStreamName, want)
		return err
	}
	have := ci.Config

	// Everything updates in place, keeping the position — the filter
	// included. The first version of this deleted and recreated the consumer
	// on a filter change, reasoning that the old position "refers to messages
	// the new filter may not match". That was wrong: the position is a stream
	// sequence, independent of any filter, and a consumer at sequence N with a
	// new filter simply continues from N delivering only what matches. Since
	// nats-server 2.10 the server updates FilterSubject in place with the
	// position intact. Recreating instead put the consumer at DeliverAll —
	// the same up-to-72h replay #250 exists to prevent — for a routine edit
	// like widening a tenant-consumer bridge from one shared connection to
	// several.
	changed := consumerDrift(have, *want)
	if len(changed) == 0 {
		return nil
	}
	cfg := have
	cfg.FilterSubject = want.FilterSubject
	cfg.AckWait = want.AckWait
	cfg.BackOff = want.BackOff
	cfg.MaxAckPending = want.MaxAckPending
	cfg.MaxDeliver = want.MaxDeliver
	cfg.InactiveThreshold = want.InactiveThreshold
	if _, err := js.UpdateConsumer(MainStreamName, &cfg); err != nil {
		return fmt.Errorf("update consumer: %w", err)
	}
	opts.Logger.Info("reconciled durable consumer config",
		"durable", opts.DurableName, "changed", changed)
	return nil
}

// consumerDrift names the fields on which an existing consumer differs from
// what we want. Named rather than boolean so the log line says what moved.
func consumerDrift(have, want nats.ConsumerConfig) []string {
	var out []string
	if have.FilterSubject != want.FilterSubject {
		out = append(out, "filter_subject")
	}
	if have.AckWait != want.AckWait {
		out = append(out, "ack_wait")
	}
	if !equalDurations(have.BackOff, want.BackOff) {
		out = append(out, "backoff")
	}
	if have.MaxAckPending != want.MaxAckPending {
		out = append(out, "max_ack_pending")
	}
	if have.MaxDeliver != want.MaxDeliver {
		out = append(out, "max_deliver")
	}
	if have.InactiveThreshold != want.InactiveThreshold {
		out = append(out, "inactive_threshold")
	}
	return out
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Stop signals the dispatch loop to exit and waits for it to finish.
//
// The durable consumer survives this. That sentence was in the previous
// version of this comment too, and it was false: the subscription owned the
// consumer, so draining it deleted the durable and the next start replayed
// the stream. The subscription now binds to a consumer it does not own, and
// draining a bound subscription leaves the consumer alone — see Subscribe.
func (s *Subscriber) Stop() {
	s.Halt()
	<-s.doneCh
	_ = s.sub.Drain()
}

// Halt tells the dispatch loop to fetch nothing more, and returns at once. A
// handler already running finishes, and its message is acked or NAK'd as
// usual; Stop still has to be called to wait for that.
//
// Call it before ending whatever a running handler waits on. Otherwise the
// loop can come round between the handler returning and Stop being reached,
// fetch the NAK'd message again, have it NAK'd again by the same ended
// handler, and so on: each round is a delivery attempt, so a stop that is
// descheduled for a couple of milliseconds dead-letters the message (#444).
//
// Halt does not cut a fetch short. The fetch in progress, at most pollWait
// of it, runs to its end, and whatever it brought is handled, acked or NAK'd
// as usual: those messages were delivered to this subscriber, and only it can
// settle them. Then the loop sends no more pull requests. So Stop takes up
// to one poll on an idle subscriber, and when it drains the subscription the
// server holds no pull request of ours that could still deliver into it
// (#494).
//
// So handlers still run after Halt returns: the messages of that last poll
// are dispatched then. Keep whatever the handlers need alive until Stop has
// returned. A caller that ends the handlers' scope right after Halt has them
// fail, and each such message is NAK'd with a delivery attempt spent (on its
// last attempt, dead-lettered). The remote-agent gateway waits for Stop,
// bounded, before it ends its handlers' scope (#576).
func (s *Subscriber) Halt() {
	s.halt.Do(func() { close(s.stopCh) })
}

func (s *Subscriber) halted() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *Subscriber) loop() {
	defer close(s.doneCh)
	for !s.halted() {
		msgs, err := s.fetch()
		// Dispatched even if Halt came during the fetch: the server has
		// counted these as delivered to us.
		for _, m := range msgs {
			s.dispatch(m)
		}
		if err == nil {
			continue
		}
		// A fetch that ended without the server's word (no batch filled, no
		// 408) may leave its pull request open on the server: the 2 s
		// backstop here, a lost connection below. Those are the only cases,
		// and both are logged as such.
		if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			s.opts.Logger.Info("JetStream fetch ended without the server's answer; its pull request may still be open",
				"durable", s.opts.DurableName, "error", err)
			continue
		}
		attrs := []any{"durable", s.opts.DurableName, "error", err}
		if errors.Is(err, nats.ErrFetchDisconnected) {
			attrs = append(attrs, "pull_request_may_be_open", true)
		}
		s.opts.Logger.Warn("JetStream fetch failed", attrs...)
		// The back-off holds no pull request, so a halt may end it.
		t := time.NewTimer(500 * time.Millisecond)
		select {
		case <-s.stopCh:
		case <-t.C:
		}
		t.Stop()
	}
}

// fetch sends one pull request, held by the server for one poll, and returns
// what it brought once the server has ended it: by filling the batch, or by
// the 408 it sends when the request expires. Ending on the server's word, not
// on a client timer, is what makes a halt safe: the request is over when the
// loop sees the halt, so nothing can be delivered into a subscription that no
// one reads any more.
func (s *Subscriber) fetch() ([]*nats.Msg, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchWait)
	defer cancel()
	mb, err := s.sub.FetchBatch(s.opts.MaxAckPending, nats.Context(pollContext{
		Context:  ctx,
		deadline: time.Now().Add(s.poll() * 10 / 9),
	}))
	if err != nil {
		return nil, err
	}
	var msgs []*nats.Msg
	for m := range mb.Messages() {
		msgs = append(msgs, m)
	}
	return msgs, mb.Error()
}

// poll is how long the server holds this subscriber's pull requests:
// pollWait, or a quarter of the shortest ack wait when that is shorter. What
// a fetch brings is dispatched once the fetch is over, so the first message
// of a batch waits up to a whole poll without heartbeats. Held longer than
// its ack wait, it would be redelivered into the same fetch and handled
// twice. With a backoff schedule the server's ack wait for delivery n is
// Backoff[n-1], not AckWait (which is Backoff[0]), so the shortest step
// counts. The default schedule starts at a second and only grows, and
// nothing in production sets a shorter one; some tests do.
func (s *Subscriber) poll() time.Duration {
	ackWait := s.opts.AckWait
	for _, b := range s.opts.Backoff {
		ackWait = min(ackWait, b)
	}
	return max(min(pollWait, ackWait/4), 10*time.Millisecond)
}

// pollContext is the context a fetch runs under. nats.go sets a pull
// request's expires to 90% of the time left until the context's deadline and
// listens until the context is done. Here the two differ on purpose: the
// deadline gives the server one poll, and the context stays open until
// fetchWait. With an honest deadline the client would stop listening only a
// tenth of a poll (15 ms) after the server started its timer, and a request
// that took longer than that to reach the server, a pod held back by its CPU
// limit say, would outlive its fetch: a halt then leaves it open, and a
// message delivered into it is never handled. Listening until the server's
// 408 makes the margin fetchWait instead. FetchBatch (not Fetch) is what ends
// on that 408 when nothing came; Fetch ignores it and waits for the context.
type pollContext struct {
	context.Context
	deadline time.Time
}

func (c pollContext) Deadline() (time.Time, bool) { return c.deadline, true }

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
