package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/doneset"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #672: in event mode a delete or move that failed after a
// successful publish is retried from a bounded in-memory queue, with a
// backoff, without publishing again.

// statSame is a Stat for queue tests: every object is there, with the zero
// stamp the tests queue it with.
func statSame(_ context.Context, key string) (objectstore.Object, error) {
	return objectstore.Object{Key: key}, nil
}

// eaTuning makes the backoff a few milliseconds so the real clock is enough.
var eaTuning = afterRetryTuning{base: 5 * time.Millisecond, max: 20 * time.Millisecond, maxRetries: 3, capacity: 10}

// eaStore is a fakeStore whose delete, copy and stat results the test
// scripts: the function gets the 0-based call number and returns the error, or
// nil to let the fake do it. Like S3, deleting a missing key is not an error;
// Stat of one is ErrNotFound, as on every backend.
type eaStore struct {
	*fakeStore
	deleteFn func(ctx context.Context, n int) error
	copyFn   func(ctx context.Context, n int) error
	statFn   func(ctx context.Context, n int) error

	deletes atomic.Int32
	copies  atomic.Int32
	stats   atomic.Int32
}

func (s *eaStore) Stat(ctx context.Context, key string) (objectstore.Object, error) {
	n := int(s.stats.Add(1)) - 1
	if s.statFn != nil {
		if err := s.statFn(ctx, n); err != nil {
			return objectstore.Object{}, err
		}
	}
	return s.fakeStore.Stat(ctx, key)
}

func (s *eaStore) Delete(ctx context.Context, key string) error {
	n := int(s.deletes.Add(1)) - 1
	if s.deleteFn != nil {
		if err := s.deleteFn(ctx, n); err != nil {
			return err
		}
	}
	return s.fakeStore.Delete(ctx, key)
}

func (s *eaStore) Copy(ctx context.Context, src, dst string) error {
	n := int(s.copies.Add(1)) - 1
	if s.copyFn != nil {
		if err := s.copyFn(ctx, n); err != nil {
			return err
		}
	}
	return s.fakeStore.Copy(ctx, src, dst)
}

func (s *eaStore) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

// failFirst fails the first n calls.
func failFirst(n int) func(context.Context, int) error {
	return func(_ context.Context, i int) error {
		if i < n {
			return errors.New("503 slow down")
		}
		return nil
	}
}

func failAlways(context.Context, int) error { return errors.New("403 denied") }

// remove takes key out of the bucket behind the consumer's back.
func (s *eaStore) remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
}

type eaLoop struct {
	c         *cloudConsumer
	fe        *fakeEvents
	logs      *nfLog
	published atomic.Int32
	stopped   chan struct{}
	cancel    context.CancelFunc
}

// eaStart runs the event loop on one message naming key, over store.
func eaStart(t *testing.T, cfg *cloudConfig, store objectstore.ObjectStore, key string, tuning afterRetryTuning) *eaLoop {
	t.Helper()
	l := &eaLoop{fe: &fakeEvents{keys: []string{key}}, logs: &nfLog{}, stopped: make(chan struct{})}
	l.c = &cloudConsumer{
		logger:     slog.New(slog.NewTextHandler(l.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		active:     make(map[string]*activeConn),
		afterRetry: tuning,
		publish: func(context.Context, *envelope.Envelope) error {
			l.published.Add(1)
			return nil
		},
		newStore:  func(context.Context, *objectstore.Config) (objectstore.ObjectStore, error) { return store, nil },
		newEvents: func(context.Context, *cloudConfig) (eventSource, error) { return l.fe, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	go func() {
		defer close(l.stopped)
		l.c.runEventLoop(ctx, &activeConn{cancel: cancel}, "conn", "tenant", cfg)
	}()
	t.Cleanup(func() { cancel(); srAwait(t, l.stopped, "event loop to stop") })
	return l
}

// stop ends the loop and waits for it, and so for the retry goroutine.
func (l *eaLoop) stop(t *testing.T) {
	t.Helper()
	l.cancel()
	srAwait(t, l.stopped, "event loop and retry queue to stop")
}

func (l *eaLoop) acked() int {
	l.fe.mu.Lock()
	defer l.fe.mu.Unlock()
	return len(l.fe.acked)
}

func (l *eaLoop) logHas(parts ...string) bool {
	for _, line := range strings.Split(l.logs.String(), "\n") {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			return true
		}
	}
	return false
}

func eaCfg(action string) *cloudConfig {
	return &cloudConfig{
		Config:        objectstore.Config{Provider: objectstore.ProviderS3, Bucket: "bkt", Prefix: "in/"},
		Mode:          "event",
		EventQueueURL: "http://sqs.local/q",
		AfterAction:   action,
		MovePrefix:    "done",
	}
}

func TestEventAfterRetry_Loop(t *testing.T) {
	const key = "in/a.csv"
	tests := []struct {
		name   string
		action string
		// key and prefix default to in/a.csv and in/.
		key, prefix string
		// refused: the store is not to be touched at all.
		refused bool
		store   func(fs *fakeStore) *eaStore
		// done: what to wait for before checking.
		done func(l *eaLoop, s *eaStore) bool
		// wantCalls: delete calls (move: copy calls) once it is over.
		wantDeletes, wantCopies int32
		wantGone                bool // key no longer in the bucket
		wantDest                bool // key present under done/
		wantLogs                [][]string
		noLogs                  [][]string
	}{
		{
			name:        "delete fails twice, then works",
			action:      "delete",
			store:       func(fs *fakeStore) *eaStore { return &eaStore{fakeStore: fs, deleteFn: failFirst(2)} },
			done:        func(l *eaLoop, s *eaStore) bool { return l.logHas("after-action done on retry") },
			wantDeletes: 3, wantGone: true,
			wantLogs: [][]string{{"level=WARN", "will retry in memory", "key=" + key}},
			noLogs:   [][]string{{"level=ERROR"}},
		},
		{
			name:       "move: copy fails once, then works",
			action:     "move",
			store:      func(fs *fakeStore) *eaStore { return &eaStore{fakeStore: fs, copyFn: failFirst(1)} },
			done:       func(l *eaLoop, s *eaStore) bool { return l.logHas("after-action done on retry") },
			wantCopies: 2, wantDeletes: 1, wantGone: true, wantDest: true,
			noLogs: [][]string{{"level=ERROR"}},
		},
		{
			name:   "always fails: stops at the cap and says so",
			action: "delete",
			store:  func(fs *fakeStore) *eaStore { return &eaStore{fakeStore: fs, deleteFn: failAlways} },
			done:   func(l *eaLoop, s *eaStore) bool { return l.logHas("level=ERROR", "giving up") },
			// the first try plus maxRetries retries
			wantDeletes: 1 + 3,
			wantLogs:    [][]string{{"level=ERROR", "giving up", "bucket=bkt", "key=" + key}},
		},
		{
			// The delete fails, then someone else removes the object: the
			// retry's Stat finds it gone, and no second delete is sent.
			name:   "gone before the retry counts as done",
			action: "delete",
			store: func(fs *fakeStore) *eaStore {
				s := &eaStore{fakeStore: fs}
				s.deleteFn = func(_ context.Context, n int) error {
					if n == 0 {
						s.remove(key)
						return errors.New("503")
					}
					return nil
				}
				return s
			},
			done:        func(l *eaLoop, s *eaStore) bool { return l.logHas("retry: object already gone") },
			wantDeletes: 1, wantGone: true,
			noLogs: [][]string{{"level=ERROR"}},
		},
		{
			// Azure's copy reads the source first, so a source that is gone
			// fails the move with ErrNotFound: done, nothing queued.
			name:   "move: source gone at the first try is done, nothing queued",
			action: "move",
			store: func(fs *fakeStore) *eaStore {
				s := &eaStore{fakeStore: fs}
				s.copyFn = func(context.Context, int) error {
					s.remove(key)
					return fmt.Errorf("azure get %q: %w", key, objectstore.ErrNotFound)
				}
				return s
			},
			done:       func(l *eaLoop, s *eaStore) bool { return l.logHas("object already gone; nothing to do") },
			wantCopies: 1, wantGone: true,
			noLogs: [][]string{{"level=ERROR"}, {"will retry"}},
		},
		{
			// A retry whose Stat fails is a failed try like any other.
			name:   "stat fails on a retry, then works",
			action: "delete",
			store: func(fs *fakeStore) *eaStore {
				// Stat 0 is the event loop's, before the fetch; Stat 1 the
				// check before the first delete (#707); Stat 2 the retry's.
				return &eaStore{fakeStore: fs, deleteFn: failFirst(1), statFn: func(_ context.Context, n int) error {
					if n == 2 {
						return errors.New("500 internal")
					}
					return nil
				}}
			},
			done:        func(l *eaLoop, s *eaStore) bool { return l.logHas("after-action done on retry") },
			wantDeletes: 2, wantGone: true,
			wantLogs: [][]string{{"level=WARN", "failed again", "500 internal"}},
			noLogs:   [][]string{{"level=ERROR"}},
		},
		{
			name:     "move onto its own key is refused and not retried",
			action:   "move",
			key:      "done/a.csv",
			prefix:   "none",
			refused:  true,
			store:    func(fs *fakeStore) *eaStore { return &eaStore{fakeStore: fs} },
			done:     func(l *eaLoop, s *eaStore) bool { return l.logHas("already in move_prefix") },
			wantLogs: [][]string{{"level=WARN", "already in move_prefix"}},
			noLogs:   [][]string{{"will retry"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := key
			cfg := eaCfg(tt.action)
			if tt.key != "" {
				k = tt.key
			}
			if tt.prefix == "none" {
				cfg.Prefix = ""
			}
			fs := &fakeStore{objects: map[string][]byte{k: []byte("x")}}
			store := tt.store(fs)
			l := eaStart(t, cfg, store, k, eaTuning)

			harness.Eventually(t, srWait, "outcome", func() bool { return tt.done(l, store) })
			// Nothing happens after the outcome: give a stray retry time to show.
			time.Sleep(100 * time.Millisecond)
			l.stop(t)

			if n := l.published.Load(); n != 1 {
				t.Errorf("published %d times, want once", n)
			}
			if l.acked() != 1 {
				t.Errorf("acked %d times, want once (ack must not wait for the after-action)", l.acked())
			}
			if !tt.refused {
				if got := store.deletes.Load(); got != tt.wantDeletes {
					t.Errorf("delete calls = %d, want %d", got, tt.wantDeletes)
				}
				if got := store.copies.Load(); got != tt.wantCopies {
					t.Errorf("copy calls = %d, want %d", got, tt.wantCopies)
				}
				if tt.wantGone == store.has(k) {
					t.Errorf("key present = %v, want %v", store.has(k), !tt.wantGone)
				}
				if tt.wantDest != store.has("done/a.csv") {
					t.Errorf("done/a.csv present = %v, want %v", store.has("done/a.csv"), tt.wantDest)
				}
			} else if store.deletes.Load() != 0 || store.copies.Load() != 0 {
				t.Errorf("refused move touched the store: %d deletes, %d copies", store.deletes.Load(), store.copies.Load())
			}
			for _, parts := range tt.wantLogs {
				if !l.logHas(parts...) {
					t.Errorf("no log line with %v in:\n%s", parts, l.logs.String())
				}
			}
			for _, parts := range tt.noLogs {
				if l.logHas(parts...) {
					t.Errorf("unexpected log line with %v in:\n%s", parts, l.logs.String())
				}
			}
		})
	}
}

// after_action none has nothing to retry: no queue, no store calls.
func TestEventAfterRetry_NoneHasNoQueue(t *testing.T) {
	fs := &fakeStore{objects: map[string][]byte{"in/a.csv": []byte("x")}}
	store := &eaStore{fakeStore: fs, deleteFn: failAlways}
	l := eaStart(t, eaCfg("none"), store, "in/a.csv", eaTuning)
	harness.Eventually(t, srWait, "acked", func() bool { return l.acked() == 1 })
	l.stop(t)
	if store.deletes.Load() != 0 || store.copies.Load() != 0 {
		t.Fatalf("store touched with after_action none")
	}
}

// A full queue drops the new key with an Error that names bucket and key; a
// key that is already queued does not take another place.
func TestEventAfterRetry_QueueFull(t *testing.T) {
	logs := &nfLog{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	// An hour's backoff: nothing is retried while the test fills the queue.
	q := newAfterRetryQueue(logger, "delete", "bkt", afterRetryTuning{base: time.Hour, capacity: 2}, doneset.MatchTime, statSame,
		func(context.Context, string, string) error { return nil })

	boom := errors.New("boom")
	q.failed("in/1", doneset.Stamp{}, boom)
	q.failed("in/2", doneset.Stamp{}, boom)
	q.failed("in/2", doneset.Stamp{}, boom) // already queued: still two
	if q.len() != 2 {
		t.Fatalf("len = %d, want 2", q.len())
	}
	q.failed("in/3", doneset.Stamp{}, boom)
	if q.len() != 2 {
		t.Fatalf("len = %d after the third key, want 2", q.len())
	}
	out := logs.String()
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "level=ERROR") && strings.Contains(line, "queue is full") &&
			strings.Contains(line, "bucket=bkt") && strings.Contains(line, "key=in/3") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no Error line for the dropped key in:\n%s", out)
	}
	if _, _, ok := q.next(); !ok {
		t.Fatal("queue empty")
	}
	if _, queued := q.items["in/3"]; queued {
		t.Fatal("in/3 was queued")
	}
	// A key forgotten makes room.
	q.forget("in/1")
	q.failed("in/3", doneset.Stamp{}, boom)
	if _, queued := q.items["in/3"]; !queued {
		t.Fatal("in/3 not queued after room was made")
	}
}

// A key published again and handled well is dropped from the queue, so the
// older failure does not touch the new object.
func TestEventAfterRetry_ForgetOnSuccess(t *testing.T) {
	var calls atomic.Int32
	q := newAfterRetryQueue(slog.New(slog.NewTextHandler(io.Discard, nil)), "delete", "bkt",
		afterRetryTuning{base: 30 * time.Millisecond}, doneset.MatchTime, statSame,
		func(context.Context, string, string) error { calls.Add(1); return nil })
	q.failed("in/a", doneset.Stamp{}, errors.New("boom"))
	q.forget("in/a")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); q.run(ctx) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	srAwait(t, done, "queue to stop")
	if calls.Load() != 0 {
		t.Fatalf("retried a forgotten key %d times", calls.Load())
	}
}

// Stop while the queue sits in its backoff: run returns, and keys left behind
// are reported.
func TestEventAfterRetry_StopDuringBackoff(t *testing.T) {
	logs := &nfLog{}
	q := newAfterRetryQueue(slog.New(slog.NewTextHandler(logs, nil)), "delete", "bkt",
		afterRetryTuning{base: time.Hour}, doneset.MatchTime, statSame,
		func(context.Context, string, string) error { t.Error("retried before the backoff ran out"); return nil })
	q.failed("in/a", doneset.Stamp{}, errors.New("boom"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); q.run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	srAwait(t, done, "queue to stop")
	if !strings.Contains(logs.String(), "pending=1") {
		t.Errorf("no note of the pending key:\n%s", logs.String())
	}
}

// Stop while a retry is in flight: the store call is cancelled by the
// connection's context, the loop returns and the retry goroutine is gone with
// it (runEventLoop waits for it, so returning is the proof).
func TestEventAfterRetry_StopWhileRetrying(t *testing.T) {
	inRetry := make(chan struct{})
	var once sync.Once
	fs := &fakeStore{objects: map[string][]byte{"in/a.csv": []byte("x")}}
	store := &eaStore{fakeStore: fs}
	store.deleteFn = func(ctx context.Context, n int) error {
		if n == 0 {
			return errors.New("503")
		}
		once.Do(func() { close(inRetry) })
		<-ctx.Done() // a hung request, released only by the connection stopping
		return ctx.Err()
	}
	l := eaStart(t, eaCfg("delete"), store, "in/a.csv", eaTuning)
	srAwait(t, inRetry, "a retry in flight")
	l.cancel()
	srAwait(t, l.stopped, "event loop and retry goroutine to stop")
	if got := store.deletes.Load(); got != 2 {
		t.Errorf("delete calls = %d, want 2 (first try and the cancelled retry)", got)
	}
	if l.logHas("level=WARN", "failed again") || l.logHas("level=ERROR") {
		t.Errorf("a cancelled retry was counted as a failure:\n%s", l.logs.String())
	}
}

func TestEventAfterRetry_Backoff(t *testing.T) {
	q := newAfterRetryQueue(slog.New(slog.NewTextHandler(io.Discard, nil)), "delete", "b", afterRetryTuning{}, doneset.MatchTime, statSame,
		func(context.Context, string, string) error { return nil })
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute,
		32 * time.Minute, time.Hour, time.Hour}
	for i, w := range want {
		if got := q.wait(i + 1); got != w {
			t.Errorf("wait(%d) = %v, want %v", i+1, got, w)
		}
	}
	if q.t.maxRetries != eventAfterMaxRetries || q.t.capacity != eventAfterQueueMax {
		t.Errorf("defaults = %d retries, %d capacity", q.t.maxRetries, q.t.capacity)
	}
}

// A key queued anew while its retry ran keeps the new item: the old try's
// result, a success or a last failure, does not remove it or count against
// it, and it keeps its new stamp.
func TestEventAfterRetry_RequeuedDuringTry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result error
	}{
		{"old try works", nil},
		{"old try fails for the last time", errors.New("503 old")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newer := doneset.Stamp{Version: "2"}
			var q *afterRetryQueue
			// maxRetries 1: a failure of the old try is its last.
			q = newAfterRetryQueue(slog.New(slog.NewTextHandler(io.Discard, nil)), "delete", "bkt",
				afterRetryTuning{base: time.Hour, maxRetries: 1}, doneset.MatchTime, statSame,
				func(context.Context, string, string) error {
					// The key is published again and its new after-action fails.
					q.failed("in/a", newer, errors.New("503 new"))
					return tc.result
				})
			q.failed("in/a", doneset.Stamp{}, errors.New("boom"))
			q.attempt(context.Background(), "in/a")
			q.mu.Lock()
			defer q.mu.Unlock()
			it, ok := q.items["in/a"]
			if !ok {
				t.Fatal("the new item was dropped by the old try's result")
			}
			if it.retries != 0 || it.stamp != newer {
				t.Fatalf("item = %+v, want the new one untouched (retries 0, stamp %+v)", *it, newer)
			}
		})
	}
}
