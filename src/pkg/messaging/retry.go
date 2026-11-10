package messaging

import (
	"slices"
	"time"

	"github.com/nats-io/nats.go"
)

// minRetryDelay is the shortest wait a handler error can ask for. Anything
// shorter, zero included, is raised to it, so a throttled target is never
// retried in a hot loop.
const minRetryDelay = time.Second

// retryDelayer is a handler error that asks for its message to wait at least
// this long before it comes back (sdk.RateLimited, #721/#794).
type retryDelayer interface{ RetryDelay() time.Duration }

// retryHint returns the largest delay any error in err's tree asks for, and
// whether one asks at all. The whole tree is walked, so with errors.Join the
// largest wins rather than the first found.
func retryHint(err error) (time.Duration, bool) {
	var best time.Duration
	found := false
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if rd, ok := e.(retryDelayer); ok {
			if d := rd.RetryDelay(); !found || d > best {
				best = d
			}
			found = true
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				walk(c)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return best, found
}

// retryWait decides when a failed delivery comes back. delivered is the
// attempt that failed (NumDelivered, from 1). It returns wait, how long after
// the NAK the message should be redelivered, and nak, the delay to send with
// NakWithDelay so that nats-server makes it wait exactly that.
//
// The wait is backoff[delivered-1] (the last step past the end). An error that
// asks for a delay gets the larger of the step and that delay, the delay
// first raised to minRetryDelay and cut to the largest step. A schedule whose
// largest step is under a second (tests only) wins over the floor.
//
// Why nak differs from wait: nats-server (processNak and checkPending, the
// same code in 2.12 and 2.15) stamps a NAK'd message with delay d as if it had
// been delivered at now-AckWait+d, and redelivers it once the step for its
// redelivery count, backoff[delivered-1], has passed since that stamp. So the
// message waits d + backoff[delivered-1] - AckWait, and to wait w we send
// d = w - backoff[delivered-1] + AckWait. For a plain retry that is AckWait:
// the attempt's ack wait starting over. ackWait is Backoff[0] (see
// Subscribe); nak is never below it, so never zero.
func retryWait(backoff []time.Duration, ackWait time.Duration, delivered uint64, err error) (wait, nak time.Duration) {
	i := len(backoff) - 1
	if delivered >= 1 && delivered-1 < uint64(len(backoff)) {
		i = int(delivered - 1)
	}
	step := backoff[i]
	wait = step
	if h, ok := retryHint(err); ok {
		h = min(max(h, minRetryDelay), slices.Max(backoff))
		wait = max(wait, h)
	}
	return wait, wait - step + ackWait
}

// delayedSet is what a subscriber knows about the messages it NAK'd with a
// delay that have not come back yet. Each of them keeps one of the durable's
// MaxAckPending slots on the server while it waits, so the subscriber caps how
// many it holds, in all and per connection (#794). An entry leaves when its
// message is dispatched again, or once it is due (it may have gone to another
// replica). The set is per process: after a restart it is empty while the
// server may still hold delays from before, and during a rolling update the
// old and the new pods each have their own caps, so together they can still
// fill every slot of a shared durable for a while. A durable-wide bound is
// #813. Not safe for concurrent use; only the dispatch loop touches it.
type delayedSet struct {
	bySeq  map[uint64]delayedEntry
	byConn map[string]int
}

type delayedEntry struct {
	conn string
	due  time.Time
}

// add records message seq of connection conn as waiting until due, if that
// stays within maxTotal and maxPerConn. It reports whether it did, and how
// many messages of conn were waiting before.
func (d *delayedSet) add(seq uint64, conn string, due, now time.Time, maxTotal, maxPerConn int) (bool, int) {
	d.prune(now)
	held := d.byConn[conn]
	if e, ok := d.bySeq[seq]; ok {
		// Already counted (a NAK that did not reach the server, retried):
		// it waits from now, so the new due time replaces the old one.
		e.due = due
		d.bySeq[seq] = e
		return true, held
	}
	if len(d.bySeq) >= maxTotal || held >= maxPerConn {
		return false, held
	}
	if d.bySeq == nil {
		d.bySeq = map[uint64]delayedEntry{}
		d.byConn = map[string]int{}
	}
	d.bySeq[seq] = delayedEntry{conn: conn, due: due}
	d.byConn[conn]++
	return true, held
}

// remove forgets seq, if it was waiting.
func (d *delayedSet) remove(seq uint64) {
	e, ok := d.bySeq[seq]
	if !ok {
		return
	}
	delete(d.bySeq, seq)
	if d.byConn[e.conn] <= 1 {
		delete(d.byConn, e.conn)
	} else {
		d.byConn[e.conn]--
	}
}

// prune forgets every entry that is due. The set never holds more than
// MaxDelayed entries, so a scan is cheap.
func (d *delayedSet) prune(now time.Time) {
	for seq, e := range d.bySeq {
		if !now.Before(e.due) {
			d.remove(seq)
		}
	}
}

func (d *delayedSet) len() int { return len(d.bySeq) }

// retry NAKs a delivery that failed and is not dead-lettered, so it comes back
// on the backoff schedule (retryWait). Over the MaxDelayed caps, or without
// metadata to tell the attempt, it NAKs without a delay: the message comes
// back at once, as every failed message did before #794.
func (s *Subscriber) retry(m *nats.Msg, meta *nats.MsgMetadata, err error) {
	if meta == nil {
		s.opts.Logger.Warn("Handler error, retrying at once: no delivery metadata",
			"durable", s.opts.DurableName, "error", err)
		_ = m.Nak()
		return
	}
	tenantID, connID := parseSubject(m.Subject)
	wait, nak := retryWait(s.opts.Backoff, s.opts.AckWait, meta.NumDelivered, err)
	now := time.Now()
	ok, held := s.delayed.add(meta.Sequence.Stream, tenantID+"."+connID, now.Add(wait), now,
		s.opts.MaxDelayed, s.opts.MaxDelayedPerConnection)
	if !ok {
		retryUndelayedCount.WithLabelValues(s.opts.DurableName).Inc()
		s.opts.Logger.Warn("Handler error, retrying at once: too many messages waiting out a retry delay",
			"durable", s.opts.DurableName, "tenant_id", tenantID, "connection_id", connID,
			"attempt", meta.NumDelivered, "waiting", s.delayed.len(), "waiting_for_connection", held,
			"error", err)
		_ = m.Nak()
		return
	}
	s.opts.Logger.Warn("Handler error, will retry",
		"durable", s.opts.DurableName, "tenant_id", tenantID, "connection_id", connID,
		"attempt", meta.NumDelivered, "retry_in", wait, "error", err)
	if nerr := m.NakWithDelay(nak); nerr != nil {
		// The server never heard the NAK, so the message comes back when its
		// ack wait runs out (Backoff[n-1] after the last heartbeat): about the
		// same wait, and it holds its slot meanwhile, so it stays counted.
		s.opts.Logger.Warn("NAK with delay failed; the message comes back when its ack wait runs out",
			"durable", s.opts.DurableName, "error", nerr)
	}
}
