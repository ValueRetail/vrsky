package main

import (
	"sync"
	"time"
)

// A message for several target databases is acked only once every target
// has its rows. When one target fails, Deliver returns an error, the message
// is NAK'd and comes back, and before #705 a redelivery would have written it
// again to the targets that had already committed it.
//
// writtenSet remembers the targets that committed a message whose delivery
// then failed, so the redelivery skips them. It is the remote agent's design
// (#587, src/cmd/remote-agent/written.go). It lives in memory: a redelivery
// handled by another process (after a restart, or by the new pod of a
// rolling deploy) does not see it and writes to every target again. That is
// the residual at-least-once per target, documented in
// docs/connectors/database.md. Keeping it across processes would need shared
// storage that the customer's table can't give us (no key of ours in it) and
// that a JetStream key-value bucket would, which is stream configuration.
//
// Only a failed delivery with at least one committed target adds entries,
// and the delivery that completes the message removes them, so the set is
// normally empty. Entries a redelivery never comes for expire after
// writtenTTL, and the set never holds more than writtenMax: forgetting an
// entry early costs a second write, never a missed one.

const (
	// writtenTTL is how long an entry is kept: long enough for the
	// redeliveries (about 2.5 minutes) and for a retry from the DLQ, which
	// republishes the same envelope. messaging.DLQRetention; an SDK
	// connector does not import pkg/messaging (make lint-connector).
	writtenTTL = 7 * 24 * time.Hour
	// writtenMax bounds the set. An entry is a few hundred bytes.
	writtenMax = 10000
)

// writtenKey is one target's commit of one message. The envelope id is the
// message (the SDK gives Deliver the envelope, not the stream sequence; every
// message gets a fresh UUID), and the node plus where it points is the
// target, so a node edited to point elsewhere is a new target.
type writtenKey struct {
	connID     string
	envelopeID string
	nodeID     string
	host       string
	port       int
	database   string
	table      string
}

type writtenSet struct {
	mu  sync.Mutex
	ttl time.Duration
	max int
	at  map[writtenKey]time.Time // when the target committed
}

func newWrittenSet(ttl time.Duration, maxEntries int) *writtenSet {
	return &writtenSet{ttl: ttl, max: maxEntries, at: map[writtenKey]time.Time{}}
}

// has reports whether a target committed this message within the TTL.
func (w *writtenSet) has(k writtenKey, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.at[k]
	if ok && now.Sub(at) >= w.ttl {
		delete(w.at, k)
		return false
	}
	return ok
}

// add records commits. When the set is full it drops what has expired and,
// if that is not enough, the oldest entries.
func (w *writtenSet) add(now time.Time, ks ...writtenKey) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range ks {
		if _, ok := w.at[k]; !ok && len(w.at) >= w.max {
			w.makeRoomLocked(now)
		}
		w.at[k] = now
	}
}

func (w *writtenSet) makeRoomLocked(now time.Time) {
	for k, at := range w.at {
		if now.Sub(at) >= w.ttl {
			delete(w.at, k)
		}
	}
	for len(w.at) >= w.max {
		var oldest writtenKey
		var oldestAt time.Time
		first := true
		for k, at := range w.at {
			if first || at.Before(oldestAt) {
				oldest, oldestAt, first = k, at, false
			}
		}
		delete(w.at, oldest)
	}
}

// forget removes entries once their message is done.
func (w *writtenSet) forget(ks ...writtenKey) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range ks {
		delete(w.at, k)
	}
}

func (w *writtenSet) size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.at)
}
