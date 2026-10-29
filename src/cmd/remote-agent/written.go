package main

import (
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/messaging"
)

// A message for several targets (agents, or one agent's folders) is acked
// only once every target has written it. If the session ends first, or one
// target's write fails, the message is NAK'd and comes back, and before #587
// the redelivery went to every target again: a target that had written the
// file was offered it a second time. The agent writes it under the same name
// and replaces it, which is harmless while the file is still there and a
// second copy for whatever reads the folder once it has taken the first away.
//
// writtenSet remembers the targets that wrote a message which was then NAK'd,
// so its redelivery skips them. It is in memory: a redelivery handled by
// another process (after a restart, or by the new pod of a rolling deploy)
// does not see it and writes to every target, as before. That is the residual
// at-least-once per target, and it is documented in
// docs/connectors/remote-agent.md. Keeping it across processes would need a
// JetStream key-value bucket, which is stream configuration.
//
// Only a NAK'd message with at least one written target adds entries, and the
// redelivery that completes the message removes them, so the set is normally
// empty. Entries a redelivery never comes for (the message went to the
// dead-letter queue, the pipeline was changed or deleted) expire after
// writtenTTL, and the set never holds more than writtenMax: forgetting an
// entry early costs a second write, never a missed one.

const (
	// writtenTTL is how long an entry is kept: as long as a message can stay
	// in the stream waiting for its next delivery.
	writtenTTL = messaging.MainRetention
	// writtenMax bounds the set. An entry is a few hundred bytes; each one
	// is a target that wrote a message whose other target has not.
	writtenMax = 10000
)

// writtenKey is one target's write of one message: the message as JetStream
// numbers it in its stream (plus the envelope ID, so a sequence reused by a
// recreated stream is a different message), and the file as this target
// wrote it.
type writtenKey struct {
	connID     string
	stream     string
	seq        uint64
	envelopeID string
	nodeID     string
	agentID    string
	directory  string
	filename   string
}

type writtenSet struct {
	mu  sync.Mutex
	ttl time.Duration
	max int
	at  map[writtenKey]time.Time // when the write was acked
}

func newWrittenSet(ttl time.Duration, maxEntries int) *writtenSet {
	return &writtenSet{ttl: ttl, max: maxEntries, at: map[writtenKey]time.Time{}}
}

// has reports whether a target wrote this message within the TTL.
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

// add records writes. When the set is full it drops what has expired and,
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
