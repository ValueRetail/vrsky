package main

import (
	"crypto/sha256"
	"sync"
	"time"
)

// A message for several HTTP nodes is acked only once no node has failed in
// a way worth retrying. When one node fails (a 5xx, a timeout, a refused
// private address), Deliver returns an error, the message is NAK'd and comes
// back, and before #730 every attempt sent it again to the nodes that had
// already taken it: five requests to a healthy sibling, and a sixth when the
// message was retried from the DLQ.
//
// sentSet remembers the nodes that took a message whose delivery then failed
// on another node, so the redelivery skips them. It is the remote agent's
// design (#587, src/cmd/remote-agent/written.go). It lives in memory: a
// redelivery handled by another process (after a restart, by the new pod of
// a rolling deploy, or by a second replica if the worker is ever scaled out)
// does not see it and sends to every node again. That is the residual
// at-least-once per node, documented in docs/connectors/http.md. #727 is the
// cross-process version for the remote agent; it would cover this producer
// too.
//
// Only a failed delivery with at least one node that took the message adds
// entries, and the delivery that completes the message removes them, so the
// set is normally empty. Entries a redelivery never comes for expire after
// sentTTL, and the set never holds more than sentMax: forgetting an entry
// early costs a second request, never a missed one.

const (
	// sentTTL is how long an entry is kept: long enough for the redeliveries
	// (about 2.5 minutes) and for a retry from the DLQ, which republishes the
	// same envelope while the DLQ keeps it. messaging.DLQRetention; an SDK
	// connector does not import pkg/messaging (make lint-connector).
	sentTTL = 7 * 24 * time.Hour
	// sentMax bounds the set. An entry is a few hundred bytes.
	sentMax = 10000
)

// sentKey is one node's acceptance of one message. The envelope id is the
// message (the SDK gives Deliver the envelope, not the stream sequence; every
// message gets a fresh UUID, and a DLQ retry keeps it), and the node plus
// where it sends is the target, so a node edited to point elsewhere between
// two attempts is a new target and gets the message. The target is a hash
// of method and URL, so the set never holds a URL's user:pass@.
type sentKey struct {
	connID     string
	envelopeID string
	nodeID     string
	target     [sha256.Size]byte // targetHash(method, url)
}

// targetHash identifies where a node sends: its method and URL.
func targetHash(method, url string) [sha256.Size]byte {
	return sha256.Sum256([]byte(method + "\x00" + url))
}

type sentSet struct {
	mu  sync.Mutex
	ttl time.Duration
	max int
	at  map[sentKey]time.Time // when the node took the message
}

func newSentSet(ttl time.Duration, maxEntries int) *sentSet {
	return &sentSet{ttl: ttl, max: maxEntries, at: map[sentKey]time.Time{}}
}

// has reports whether a node took this message within the TTL.
func (s *sentSet) has(k sentKey, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.at[k]
	if ok && now.Sub(at) >= s.ttl {
		delete(s.at, k)
		return false
	}
	return ok
}

// add records nodes that took a message. When the set is full it drops what
// has expired and, if that is not enough, the oldest entries.
func (s *sentSet) add(now time.Time, ks ...sentKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range ks {
		if _, ok := s.at[k]; !ok && len(s.at) >= s.max {
			s.makeRoomLocked(now)
		}
		s.at[k] = now
	}
}

func (s *sentSet) makeRoomLocked(now time.Time) {
	for k, at := range s.at {
		if now.Sub(at) >= s.ttl {
			delete(s.at, k)
		}
	}
	for len(s.at) >= s.max {
		var oldest sentKey
		var oldestAt time.Time
		first := true
		for k, at := range s.at {
			if first || at.Before(oldestAt) {
				oldest, oldestAt, first = k, at, false
			}
		}
		delete(s.at, oldest)
	}
}

// forget removes entries once their message is done.
func (s *sentSet) forget(ks ...sentKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range ks {
		delete(s.at, k)
	}
}

func (s *sentSet) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.at)
}
