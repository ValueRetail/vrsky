package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #672, round two: a retried delete or move must not remove an
// object written again under the same key after the failure, a key handled
// again drops its queued retry, and the loop waits for a retry in flight.

// seqEvents delivers a message of keys each time the test sends one.
type seqEvents struct {
	ch    chan []string
	mu    sync.Mutex
	n     int
	acked []string
}

func (e *seqEvents) Receive(ctx context.Context) ([]eventMessage, error) {
	select {
	case keys := <-e.ch:
		e.mu.Lock()
		e.n++
		h := fmt.Sprintf("h%d", e.n)
		e.mu.Unlock()
		return []eventMessage{{objectKeys: keys, ackHandle: h}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *seqEvents) Ack(_ context.Context, handle string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.acked = append(e.acked, handle)
	return nil
}

func (e *seqEvents) ackCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.acked)
}

// A key whose delete failed is published again, and this time the delete
// works, before the queued retry is due. The retry must not run at all: no
// further Delete, and nothing in the log from a retry.
func TestEventAfterRetry_HandledAgainDropsRetry(t *testing.T) {
	const key = "in/a.csv"
	fs := &fakeStore{objects: map[string][]byte{key: []byte("x")}}
	store := &eaStore{fakeStore: fs, deleteFn: failFirst(1)}
	ev := &seqEvents{ch: make(chan []string)}
	logs := &nfLog{}
	var published atomic.Int32
	c := &cloudConsumer{
		logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		active: make(map[string]*activeConn),
		// The retry is due 300 ms after the failure; the second notification
		// is handled well before that.
		afterRetry: afterRetryTuning{base: 300 * time.Millisecond, max: 300 * time.Millisecond, maxRetries: 3, capacity: 10},
		publish: func(context.Context, *envelope.Envelope) error {
			published.Add(1)
			return nil
		},
		newStore:  func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return store, nil },
		newEvents: func(context.Context, *cloudConfig) (eventSource, error) { return ev, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		c.runEventLoop(ctx, &activeConn{cancel: cancel}, "conn", "tenant", eaCfg("delete"))
	}()
	t.Cleanup(func() { cancel(); srAwait(t, stopped, "event loop to stop") })

	ev.ch <- []string{key}
	harness.Eventually(t, srWait, "first delete to fail and be queued", func() bool {
		return ev.ackCount() == 1 && store.deletes.Load() == 1
	})
	// The same object is uploaded again (its notification arrives); this
	// time the delete works.
	_ = fs.Put(context.Background(), key, []byte("x"), "")
	ev.ch <- []string{key}
	harness.Eventually(t, srWait, "second notification handled", func() bool {
		return ev.ackCount() == 2 && store.deletes.Load() == 2
	})
	// Past the time the retry was due.
	time.Sleep(600 * time.Millisecond)
	cancel()
	srAwait(t, stopped, "event loop to stop")

	if n := published.Load(); n != 2 {
		t.Errorf("published %d times, want 2", n)
	}
	if n := store.deletes.Load(); n != 2 {
		t.Errorf("delete calls = %d, want 2: the queued retry ran after the key was handled again", n)
	}
	out := logs.String()
	for _, p := range []string{"after-action retry", "on retry", "failed again"} {
		if strings.Contains(out, p) {
			t.Errorf("the queued retry ran (%q in the log):\n%s", p, out)
		}
	}
}

// An object written again under the same key after its after-action failed,
// whose notification has not been handled yet, is the replacement: the retry
// must leave it in the bucket, unmoved, and say why.
func TestEventAfterRetry_ReplacedIsLeftAlone(t *testing.T) {
	const key = "in/a.csv"
	for _, action := range []string{"delete", "move"} {
		t.Run(action, func(t *testing.T) {
			fs := &fakeStore{objects: map[string][]byte{key: []byte("x")}}
			store := &eaStore{fakeStore: fs}
			// The first try fails, and the replacement lands right after it,
			// with the same size: only the version tells them apart.
			replace := func(_ context.Context, n int) error {
				if n == 0 {
					_ = fs.Put(context.Background(), key, []byte("y"), "")
					return errors.New("503 slow down")
				}
				return nil
			}
			if action == "delete" {
				store.deleteFn = replace
			} else {
				store.copyFn = replace
			}
			l := eaStart(t, eaCfg(action), store, key, eaTuning)
			harness.Eventually(t, srWait, "the retry to see the replacement", func() bool {
				return l.logHas("level=WARN", "replaced since published", "key="+key)
			})
			// Give a stray retry time to show.
			time.Sleep(100 * time.Millisecond)
			l.stop(t)

			fs.mu.Lock()
			body, ok := fs.objects[key]
			fs.mu.Unlock()
			if !ok || string(body) != "y" {
				t.Fatalf("replacement gone or changed (present %v, body %q): the retry removed an unpublished object", ok, body)
			}
			if store.has("done/a.csv") {
				t.Errorf("replacement copied to done/")
			}
			wantDeletes, wantCopies := int32(1), int32(0)
			if action == "move" {
				wantDeletes, wantCopies = 0, 1
			}
			if store.deletes.Load() != wantDeletes || store.copies.Load() != wantCopies {
				t.Errorf("delete/copy calls = %d/%d, want %d/%d", store.deletes.Load(), store.copies.Load(), wantDeletes, wantCopies)
			}
			if l.published.Load() != 1 {
				t.Errorf("published %d times, want once", l.published.Load())
			}
		})
	}
}

// The loop returns only after a retry in flight has finished, even one whose
// store call ignores the cancellation for a while.
func TestEventAfterRetry_LoopWaitsForRetryInFlight(t *testing.T) {
	const key = "in/a.csv"
	fs := &fakeStore{objects: map[string][]byte{key: []byte("x")}}
	inRetry := make(chan struct{})
	var finished atomic.Bool
	store := &eaStore{fakeStore: fs}
	store.deleteFn = func(_ context.Context, n int) error {
		if n == 0 {
			return errors.New("503")
		}
		if n == 1 {
			close(inRetry)
			time.Sleep(50 * time.Millisecond) // a call that does not watch ctx
			finished.Store(true)
		}
		return nil
	}
	l := eaStart(t, eaCfg("delete"), store, key, eaTuning)
	srAwait(t, inRetry, "a retry in flight")
	l.cancel()
	srAwait(t, l.stopped, "event loop to stop")
	if !finished.Load() {
		t.Fatal("the event loop returned while its retry goroutine was still in a store call")
	}
}
