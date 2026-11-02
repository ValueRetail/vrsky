package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #609: in event mode a key that is no longer in the bucket counts
// as handled. The case that matters is a redelivered multi-key message. One key
// failed, so the message was not acked; the other keys were published and
// deleted or moved by after_action. On redelivery their get says "not found".
// Before the fix that kept the message from ever acking.
//
// The event source here is a real JetStream consumer on the embedded server
// (explicit acks, a short AckWait), so redelivery is the queue's own and not a
// fake that replays on demand.

const (
	nfStream  = "NFEVENTS"
	nfSubject = "nfevents.msg"
	nfAckWait = 250 * time.Millisecond
)

// nfEvents is an eventSource over a JetStream pull consumer. Receive hands out
// a message again once its AckWait has passed without an ack, which is how SQS,
// Azure Storage Queues and Pub/Sub behave.
type nfEvents struct {
	sub *nats.Subscription

	mu      sync.Mutex
	msgs    map[string]*nats.Msg
	maxSeen uint64 // highest delivery count of any message
}

func newNFEvents(t *testing.T) (*nfEvents, nats.JetStreamContext) {
	t.Helper()
	_, js, stop := harness.StartEmbeddedJetStream(t)
	t.Cleanup(stop)
	if _, err := js.AddStream(&nats.StreamConfig{Name: nfStream, Subjects: []string{"nfevents.>"}}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
	sub, err := js.PullSubscribe(nfSubject, "nf-consumer",
		nats.AckExplicit(), nats.AckWait(nfAckWait), nats.MaxDeliver(-1))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}
	return &nfEvents{sub: sub, msgs: map[string]*nats.Msg{}}, js
}

func (e *nfEvents) Receive(ctx context.Context) ([]eventMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	got, err := e.sub.Fetch(10, nats.MaxWait(100*time.Millisecond))
	if err != nil {
		if errors.Is(err, nats.ErrTimeout) {
			return nil, nil
		}
		return nil, err
	}
	var out []eventMessage
	for _, m := range got {
		md, err := m.Metadata()
		if err != nil {
			return nil, err
		}
		handle := strconv.FormatUint(md.Sequence.Stream, 10) + "/" + strconv.FormatUint(md.NumDelivered, 10)
		e.mu.Lock()
		e.msgs[handle] = m
		if md.NumDelivered > e.maxSeen {
			e.maxSeen = md.NumDelivered
		}
		e.mu.Unlock()
		var body struct {
			Keys []string `json:"keys"`
		}
		if err := json.Unmarshal(m.Data, &body); err != nil {
			return nil, err
		}
		out = append(out, eventMessage{objectKeys: body.Keys, ackHandle: handle})
	}
	return out, nil
}

func (e *nfEvents) Ack(_ context.Context, handle string) error {
	e.mu.Lock()
	m := e.msgs[handle]
	e.mu.Unlock()
	if m == nil {
		return fmt.Errorf("unknown handle %q", handle)
	}
	return m.AckSync()
}

func (e *nfEvents) deliveries() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maxSeen
}

// acked reports whether the stream's one message has been acknowledged.
func (e *nfEvents) acked(t *testing.T) bool {
	t.Helper()
	ci, err := e.sub.ConsumerInfo()
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	return ci.AckFloor.Stream >= 1 && ci.NumAckPending == 0 && ci.NumPending == 0
}

// nfStore is a fakeStore that, like the real backends, says "not found" for a
// key that is not there. A key in broken fails with some other error.
type nfStore struct {
	*fakeStore
	broken map[string]bool
}

func (s *nfStore) lookup(key string) error {
	if s.broken[key] {
		return errors.New("403 access denied")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects[key]; !ok {
		return fmt.Errorf("fake get %q: %w", key, objectstore.ErrNotFound)
	}
	return nil
}

func (s *nfStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	if err := s.lookup(key); err != nil {
		return nil, "", err
	}
	return s.fakeStore.Get(ctx, key)
}

func (s *nfStore) GetStream(ctx context.Context, key string) (io.ReadCloser, string, error) {
	if err := s.lookup(key); err != nil {
		return nil, "", err
	}
	return s.fakeStore.GetStream(ctx, key)
}

// nfLog is a concurrency-safe log sink.
type nfLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *nfLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *nfLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestEventMode_KeyAlreadyGone(t *testing.T) {
	tests := []struct {
		name string
		// inBucket: keys present when the message is first delivered.
		inBucket []string
		// gone: keys already removed before the first delivery (another
		// consumer's work, a lifecycle rule, an earlier delivery).
		gone []string
		keys []string
		// failPublish: keys whose publish fails while failures > 0 (-1 = always).
		failPublish map[string]int
		// broken: keys whose get fails with an error that is not "not found".
		broken      map[string]bool
		afterAction string
		movePrefix  string

		wantAcked bool
		// wantPublished: how many times each key is published, in total.
		wantPublished map[string]int
		wantGoneLog   []string // keys that must be logged as no longer in the bucket
		wantMinDeliv  uint64
	}{
		{
			name:          "sibling failed once, delete: redelivery acks",
			inBucket:      []string{"in/a.csv", "in/b.csv", "in/c.csv"},
			keys:          []string{"in/a.csv", "in/b.csv", "in/c.csv"},
			failPublish:   map[string]int{"in/b.csv": 1},
			afterAction:   "delete",
			wantAcked:     true,
			wantPublished: map[string]int{"in/a.csv": 1, "in/b.csv": 1, "in/c.csv": 1},
			wantGoneLog:   []string{"in/a.csv", "in/c.csv"},
			wantMinDeliv:  2,
		},
		{
			name:          "sibling failed once, move: redelivery acks",
			inBucket:      []string{"in/a.csv", "in/b.csv", "in/c.csv"},
			keys:          []string{"in/a.csv", "in/b.csv", "in/c.csv"},
			failPublish:   map[string]int{"in/b.csv": 1},
			afterAction:   "move",
			movePrefix:    "done",
			wantAcked:     true,
			wantPublished: map[string]int{"in/a.csv": 1, "in/b.csv": 1, "in/c.csv": 1},
			wantGoneLog:   []string{"in/a.csv", "in/c.csv"},
			wantMinDeliv:  2,
		},
		{
			name:          "every key already gone: acks, publishes nothing",
			gone:          []string{"in/a.csv", "in/b.csv"},
			keys:          []string{"in/a.csv", "in/b.csv"},
			afterAction:   "delete",
			wantAcked:     true,
			wantPublished: map[string]int{},
			wantGoneLog:   []string{"in/a.csv", "in/b.csv"},
			wantMinDeliv:  1,
		},
		{
			name:          "one key gone, the other ingested: acks on the first delivery",
			inBucket:      []string{"in/b.csv"},
			gone:          []string{"in/a.csv"},
			keys:          []string{"in/a.csv", "in/b.csv"},
			afterAction:   "none",
			wantAcked:     true,
			wantPublished: map[string]int{"in/b.csv": 1},
			wantGoneLog:   []string{"in/a.csv"},
			wantMinDeliv:  1,
		},
		{
			name:          "a key that keeps failing is redelivered; gone keys are not published again",
			inBucket:      []string{"in/a.csv", "in/b.csv", "in/c.csv"},
			keys:          []string{"in/a.csv", "in/b.csv", "in/c.csv"},
			failPublish:   map[string]int{"in/b.csv": -1},
			afterAction:   "delete",
			wantAcked:     false,
			wantPublished: map[string]int{"in/a.csv": 1, "in/c.csv": 1},
			wantMinDeliv:  3,
		},
		{
			name:          "gone key beside a key whose get fails otherwise: not acked",
			inBucket:      []string{"in/b.csv"},
			gone:          []string{"in/a.csv"},
			broken:        map[string]bool{"in/b.csv": true},
			keys:          []string{"in/a.csv", "in/b.csv"},
			afterAction:   "none",
			wantAcked:     false,
			wantPublished: map[string]int{},
			wantMinDeliv:  3,
		},
		{
			name:          "a get error that is not not-found never counts as gone",
			inBucket:      []string{"in/a.csv"},
			broken:        map[string]bool{"in/a.csv": true},
			keys:          []string{"in/a.csv"},
			afterAction:   "none",
			wantAcked:     false,
			wantPublished: map[string]int{},
			wantMinDeliv:  3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, js := newNFEvents(t)

			fs := &fakeStore{objects: map[string][]byte{}}
			for _, k := range tt.inBucket {
				fs.objects[k] = []byte(k) // the payload is the key
			}
			store := &nfStore{fakeStore: fs, broken: tt.broken}

			var mu sync.Mutex
			published := map[string]int{}
			failsLeft := map[string]int{}
			for k, n := range tt.failPublish {
				failsLeft[k] = n
			}
			logs := &nfLog{}
			c := &cloudConsumer{
				logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				active: make(map[string]*activeConn),
				publish: func(_ context.Context, e *envelope.Envelope) error {
					mu.Lock()
					defer mu.Unlock()
					key := string(e.Payload)
					if n := failsLeft[key]; n != 0 {
						if n > 0 {
							failsLeft[key] = n - 1
						}
						return io.ErrClosedPipe
					}
					published[key]++
					return nil
				},
				newStore:  func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return store, nil },
				newEvents: func(context.Context, *cloudConfig) (eventSource, error) { return events, nil },
			}
			cfg := &cloudConfig{
				Config:        objectstore.Config{Provider: objectstore.ProviderS3, Bucket: "b", Prefix: "in/"},
				Mode:          "event",
				EventQueueURL: "http://sqs.local/q",
				AfterAction:   tt.afterAction,
				MovePrefix:    tt.movePrefix,
			}

			body, _ := json.Marshal(map[string]any{"keys": tt.keys})
			if _, err := js.Publish(nfSubject, body); err != nil {
				t.Fatalf("publish event: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				c.runEventLoop(ctx, &activeConn{cancel: cancel}, "conn", "tenant", cfg)
			}()
			defer func() {
				cancel()
				srAwait(t, stopped, "event loop to stop")
			}()

			if tt.wantAcked {
				harness.Eventually(t, srWait, "message acked", func() bool { return events.acked(t) })
			}
			// Redelivery is the queue's own: wait until it has happened as
			// often as the case needs, so "not acked" means "kept coming back".
			harness.Eventually(t, srWait, fmt.Sprintf("%d deliveries", tt.wantMinDeliv), func() bool {
				return events.deliveries() >= tt.wantMinDeliv
			})
			if !tt.wantAcked && events.acked(t) {
				t.Fatal("message was acked, want it left for redelivery")
			}
			if tt.wantAcked && !events.acked(t) {
				t.Fatal("message no longer acked")
			}

			mu.Lock()
			if !reflect.DeepEqual(published, tt.wantPublished) {
				t.Errorf("published %v, want %v", published, tt.wantPublished)
			}
			mu.Unlock()

			out := logs.String()
			for _, k := range tt.wantGoneLog {
				found := false
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(line, "level=INFO") && strings.Contains(line, "treated as already handled") &&
						strings.HasSuffix(line, " key="+k) {
						found = true
					}
				}
				if !found {
					t.Errorf("no Info line for gone key %s in:\n%s", k, out)
				}
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "no longer in the bucket") && !strings.Contains(line, "level=INFO") {
					t.Errorf("gone-key line is not Info: %s", line)
				}
			}
			// A gone key gets no after-action: nothing to move or delete.
			fs.mu.Lock()
			defer fs.mu.Unlock()
			for _, k := range tt.gone {
				for _, d := range fs.deleted {
					if d == k {
						t.Errorf("gone key %s was deleted", k)
					}
				}
				if _, ok := fs.copied[k]; ok {
					t.Errorf("gone key %s was copied", k)
				}
			}
		})
	}
}

// A message that names only keys that are gone costs one delivery, even with
// the streaming read path on.
func TestEventMode_KeyAlreadyGone_StreamPath(t *testing.T) {
	events, js := newNFEvents(t)
	fs := &fakeStore{objects: map[string][]byte{}}
	var calls atomic.Int32
	c := &cloudConsumer{
		logger:        slog.New(slog.NewTextHandler(&nfLog{}, nil)),
		active:        make(map[string]*activeConn),
		publish:       func(context.Context, *envelope.Envelope) error { calls.Add(1); return nil },
		publishStream: func(context.Context, *envelope.Envelope, io.Reader) error { calls.Add(1); return nil },
		inlineMax:     8,
		newStore: func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) {
			return &nfStore{fakeStore: fs}, nil
		},
		newEvents: func(context.Context, *cloudConfig) (eventSource, error) { return events, nil },
	}
	cfg := &cloudConfig{
		Config:        objectstore.Config{Provider: objectstore.ProviderS3, Bucket: "b"},
		Mode:          "event",
		EventQueueURL: "http://sqs.local/q",
	}
	body, _ := json.Marshal(map[string]any{"keys": []string{"x.csv"}})
	if _, err := js.Publish(nfSubject, body); err != nil {
		t.Fatalf("publish event: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		c.runEventLoop(ctx, &activeConn{cancel: cancel}, "conn", "tenant", cfg)
	}()
	harness.Eventually(t, srWait, "message acked", func() bool { return events.acked(t) })
	cancel()
	srAwait(t, stopped, "event loop to stop")
	if n := calls.Load(); n != 0 {
		t.Errorf("published %d times, want 0", n)
	}
	if d := events.deliveries(); d != 1 {
		t.Errorf("deliveries = %d, want 1", d)
	}
}
