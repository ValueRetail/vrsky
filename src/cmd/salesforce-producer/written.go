package main

import (
	"sync"
	"time"
)

// A message is acked only once every record of it is in every target. When
// one record fails, Deliver returns an error, the message is NAK'd and comes
// back, and before #735 the redelivery posted every record again, so an
// insert created duplicates of the records that had gone in.
//
// writtenSet remembers the records that went in during a delivery that then
// failed, so the redelivery skips them. It is the design of db-producer
// (#705) and the remote agent (#587), per record instead of per target. It
// lives in memory: a redelivery handled by another process (after a restart,
// or by the new pod of a rolling deploy) does not see it and sends every
// record again, and so does a record whose answer from Salesforce was lost.
// That residual is documented in docs/connectors/salesforce.md.
//
// Only a failed delivery adds entries, and the delivery that completes the
// message removes them, so the set is normally empty. Entries a redelivery
// never comes for expire after writtenTTL, and the set never holds more than
// writtenMax: forgetting an entry early costs a second write, never a missed
// one.

const (
	// writtenTTL is how long an entry is kept: long enough for the
	// redeliveries (about 2.5 minutes) and for a retry from the DLQ, which
	// republishes the same envelope. messaging.DLQRetention; an SDK
	// connector does not import pkg/messaging (make lint-connector).
	writtenTTL = 7 * 24 * time.Hour
	// writtenMax bounds the set. An entry is a few hundred bytes.
	writtenMax = 10000
)

// bulkRecord is the record index of a Bulk API job: the job takes every
// record of the message at once, so it gets one entry.
const bulkRecord = -1

// writtenKey is one record of one message in one target. The envelope id is
// the message (the SDK gives Deliver the envelope, not the stream sequence;
// every message gets a fresh UUID), the record is its index in the payload,
// and the node plus where it writes is the target, so a node edited to write
// elsewhere is a new target.
type writtenKey struct {
	connID      string
	envelopeID  string
	nodeID      string
	instanceURL string
	object      string
	operation   string
	externalID  string
	record      int
}

type writtenSet struct {
	mu  sync.Mutex
	ttl time.Duration
	max int
	at  map[writtenKey]time.Time // when the record went in
}

func newWrittenSet(ttl time.Duration, maxEntries int) *writtenSet {
	return &writtenSet{ttl: ttl, max: maxEntries, at: map[writtenKey]time.Time{}}
}

// has reports whether the record went in within the TTL.
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
