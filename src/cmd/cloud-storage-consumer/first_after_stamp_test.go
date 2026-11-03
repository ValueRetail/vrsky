package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #707: the first delete or move after a publish must not remove an
// object written again under the same key while the old one was published.
// The store's publish hook stands in for an upload that lands mid-publish.

// fsReplace writes body under key as a new version (the generation goes up,
// as an ETag or GCS generation would), last modified pdT0+sec seconds.
func fsReplace(f *fakeStore, key, body string, sec int) {
	pdPut(f, key, body, sec)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wrote(key)
}

func fsBody(f *fakeStore, key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	return string(b), ok
}

// faStart is eaStart with a hook that runs inside each publish.
func faStart(t *testing.T, cfg *cloudConfig, store objectstore.ObjectStore, key string, onPublish func(n int32)) *eaLoop {
	t.Helper()
	l := &eaLoop{fe: &fakeEvents{keys: []string{key}}, logs: &nfLog{}, stopped: make(chan struct{})}
	l.c = &cloudConsumer{
		logger:     slog.New(slog.NewTextHandler(l.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		active:     make(map[string]*activeConn),
		afterRetry: eaTuning,
		publish: func(context.Context, *envelope.Envelope) error {
			n := l.published.Add(1)
			if onPublish != nil {
				onPublish(n)
			}
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

func TestFirstAfterAction_Event(t *testing.T) {
	const key = "in/a.csv"
	tests := []struct {
		name string
		// during runs inside the publish.
		during func(fs *fakeStore)
		// statFn scripts the store's Stat calls (0 is before the fetch, 1
		// right before the after-action).
		statFn func(ctx context.Context, n int) error
		// done reports the outcome is in.
		done func(l *eaLoop, s *eaStore) bool
		// wantBody is what must be under key at the end; "" means gone.
		wantBody   string
		wantMoved  bool
		wantAction bool // a delete (delete) or a copy (move) was made
		wantLogs   [][]string
		noLogs     [][]string
	}{
		{
			name: "unchanged object is removed as before",
			done: func(l *eaLoop, s *eaStore) bool {
				_, ok := fsBody(s.fakeStore, key)
				return !ok && l.acked() == 1
			},
			wantMoved: true, wantAction: true,
			noLogs: [][]string{{"level=WARN"}, {"level=ERROR"}},
		},
		{
			// Same size and last-modified: only the version tells them apart.
			name:     "replaced during the publish is left alone",
			during:   func(fs *fakeStore) { fsReplace(fs, key, "y", 0) },
			done:     func(l *eaLoop, s *eaStore) bool { return l.logHas("replaced since published") },
			wantBody: "y",
			wantLogs: [][]string{{"level=WARN", "replaced since published; left for its own notification", "key=" + key}},
			noLogs:   [][]string{{"will retry"}, {"on retry"}, {"level=ERROR"}},
		},
		{
			name:     "gone before the after-action is done",
			during:   func(fs *fakeStore) { pdDel(fs, key) },
			done:     func(l *eaLoop, s *eaStore) bool { return l.logHas("already gone") },
			wantLogs: [][]string{{"level=INFO", "already gone; nothing to do", "key=" + key}},
			noLogs:   [][]string{{"will retry"}, {"level=WARN"}, {"level=ERROR"}},
		},
		{
			name: "stat failing before the after-action is retried",
			statFn: func(_ context.Context, n int) error {
				if n == 1 {
					return errors.New("500 internal")
				}
				return nil
			},
			done:      func(l *eaLoop, s *eaStore) bool { return l.logHas("after-action done on retry") },
			wantMoved: true, wantAction: true,
			wantLogs: [][]string{{"level=WARN", "will retry in memory", "500 internal"}},
			noLogs:   [][]string{{"level=ERROR"}},
		},
	}
	for _, tt := range tests {
		for _, action := range []string{"delete", "move"} {
			t.Run(tt.name+"/"+action, func(t *testing.T) {
				// Same last-modified as the replacement below.
				fs := &fakeStore{}
				pdPut(fs, key, "x", 0)
				store := &eaStore{fakeStore: fs, statFn: tt.statFn}
				l := faStart(t, eaCfg(action), store, key, func(n int32) {
					if n == 1 && tt.during != nil {
						tt.during(fs)
					}
				})
				harness.Eventually(t, srWait, "outcome", func() bool { return tt.done(l, store) })
				// Past several retry delays: a stray retry would show.
				time.Sleep(100 * time.Millisecond)
				l.stop(t)

				body, ok := fsBody(fs, key)
				switch {
				case tt.wantBody == "" && ok:
					t.Errorf("%s still in the bucket (%q)", key, body)
				case tt.wantBody != "" && (!ok || body != tt.wantBody):
					t.Errorf("%s: present %v, body %q, want %q: the replacement was removed unpublished", key, ok, body, tt.wantBody)
				}
				moved := store.has("done/a.csv")
				if moved != (tt.wantMoved && action == "move") {
					t.Errorf("done/a.csv present = %v, want %v", moved, tt.wantMoved && action == "move")
				}
				acted := store.deletes.Load() > 0
				if action == "move" {
					acted = store.copies.Load() > 0
				}
				if acted != tt.wantAction {
					t.Errorf("after-action ran = %v (deletes %d, copies %d), want %v",
						acted, store.deletes.Load(), store.copies.Load(), tt.wantAction)
				}
				if n := l.published.Load(); n != 1 {
					t.Errorf("published %d times, want once", n)
				}
				if n := l.acked(); n != 1 {
					t.Errorf("acked %d times, want once", n)
				}
				for _, w := range tt.wantLogs {
					if !l.logHas(w...) {
						t.Errorf("no log line with %q:\n%s", w, l.logs.String())
					}
				}
				for _, w := range tt.noLogs {
					if l.logHas(w...) {
						t.Errorf("unexpected log line with %q:\n%s", w, l.logs.String())
					}
				}
			})
		}
	}
}

// faStatStore is a flakyStore whose Stat calls the test can fail.
type faStatStore struct {
	*flakyStore
	statFn func(n int) error
	stats  atomic.Int32
}

func (s *faStatStore) Stat(ctx context.Context, key string) (objectstore.Object, error) {
	n := int(s.stats.Add(1)) - 1
	if s.statFn != nil {
		if err := s.statFn(n); err != nil {
			return objectstore.Object{}, err
		}
	}
	return s.fakeStore.Stat(ctx, key)
}

// faPoll is an arRig with a text log and a publish hook, polling through
// store.
type faPoll struct {
	*arRig
	store objectstore.ObjectStore
	logs  *nfLog
}

func newFAPoll(t *testing.T, action string, during func(fs *fakeStore)) (*faPoll, *faStatStore) {
	fk := &flakyStore{fakeStore: &fakeStore{}}
	pdPut(fk.fakeStore, "in/a.json", `{"a":1}`, 0)
	st := &faStatStore{flakyStore: fk}
	p := &faPoll{arRig: newARRig(t, action, fk), store: st, logs: &nfLog{}}
	p.c.logger = slog.New(slog.NewTextHandler(p.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var calls atomic.Int32
	p.c.publish = func(ctx context.Context, env *envelope.Envelope) error {
		if err := p.log.publish(ctx, env); err != nil {
			return err
		}
		if calls.Add(1) == 1 && during != nil {
			during(fk.fakeStore)
		}
		return nil
	}
	return p, st
}

func (p *faPoll) tick() {
	p.clock = p.clock.Add(arInterval)
	p.c.pollOnce(context.Background(), p.conn, p.tenant, p.store, &p.cfg, p.done, p.c.logger)
}

func (p *faPoll) logHas(parts ...string) bool {
	for _, line := range strings.Split(p.logs.String(), "\n") {
		ok := true
		for _, s := range parts {
			ok = ok && strings.Contains(line, s)
		}
		if ok {
			return true
		}
	}
	return false
}

// actions is how many deletes (delete) or copies (move) were tried.
func (p *faPoll) actions(action string) int {
	if action == "move" {
		return len(p.fk.attempts("copy"))
	}
	return len(p.fk.attempts("delete"))
}

// A replacement uploaded during the publish is left for the next poll, which
// publishes it and only then removes it.
func TestFirstAfterAction_PollReplaced(t *testing.T) {
	for _, action := range []string{"delete", "move"} {
		t.Run(action, func(t *testing.T) {
			p, _ := newFAPoll(t, action, func(fs *fakeStore) { fsReplace(fs, "in/a.json", `{"a":2,"b":3}`, 5) })

			p.tick()
			requireCounts(t, p.log, "first poll", map[string]int{"in/a.json": 1})
			if body, ok := fsBody(p.fk.fakeStore, "in/a.json"); !ok || body != `{"a":2,"b":3}` {
				t.Fatalf("replacement gone or changed (present %v, body %q): removed unpublished", ok, body)
			}
			if n := p.actions(action); n != 0 {
				t.Fatalf("%s tried %d times on the replacement", action, n)
			}
			if p.fk.has("done/a.json") {
				t.Fatal("replacement copied to done/")
			}
			if !p.logHas("level=WARN", "replaced since published; left for the next poll", "key=in/a.json") {
				t.Errorf("no Warn for the replacement:\n%s", p.logs.String())
			}

			p.tick()
			requireCounts(t, p.log, "next poll", map[string]int{"in/a.json": 2})
			if got := p.log.payloads("in/a.json"); got[1] != `{"a":2,"b":3}` {
				t.Errorf("second publish carried %q, want the replacement", got[1])
			}
			if p.fk.has("in/a.json") {
				t.Error("the replacement, now published, is still in the bucket")
			}
			if n := p.actions(action); n != 1 {
				t.Errorf("%s tried %d times, want once", action, n)
			}
			if action == "move" {
				if body, _ := fsBody(p.fk.fakeStore, "done/a.json"); body != `{"a":2,"b":3}` {
					t.Errorf("done/a.json = %q, want the replacement", body)
				}
			}
			if p.logHas("will retry") || p.logHas("level=ERROR") {
				t.Errorf("unexpected retry or error:\n%s", p.logs.String())
			}
		})
	}
}

// An object gone by the time of the after-action is done: nothing is tried,
// now or at a later poll.
func TestFirstAfterAction_PollGone(t *testing.T) {
	for _, action := range []string{"delete", "move"} {
		t.Run(action, func(t *testing.T) {
			p, _ := newFAPoll(t, action, func(fs *fakeStore) { pdDel(fs, "in/a.json") })
			p.tick()
			if !p.logHas("level=INFO", "already gone; nothing to do", "key=in/a.json") {
				t.Errorf("no Info for the gone object:\n%s", p.logs.String())
			}
			for i := 0; i < 10; i++ {
				p.tick()
			}
			requireCounts(t, p.log, "after the polls", map[string]int{"in/a.json": 1})
			if n := p.actions(action); n != 0 {
				t.Errorf("%s tried %d times on a gone object", action, n)
			}
			if p.logHas("will retry") || p.logHas("level=WARN") || p.logHas("level=ERROR") {
				t.Errorf("unexpected log:\n%s", p.logs.String())
			}
		})
	}
}

// A Stat that fails right before the after-action is a failed after-action:
// it backs off and is retried at a later poll (#640), not published again.
func TestFirstAfterAction_PollStatFails(t *testing.T) {
	for _, action := range []string{"delete", "move"} {
		t.Run(action, func(t *testing.T) {
			p, st := newFAPoll(t, action, nil)
			st.statFn = func(n int) error {
				if n == 1 {
					return errors.New("500 internal")
				}
				return nil
			}
			p.tick()
			if n := p.actions(action); n != 0 {
				t.Fatalf("%s ran %d times after a failed Stat", action, n)
			}
			if !p.logHas("level=WARN", "after-action failed; will retry", "500 internal") {
				t.Fatalf("no retry Warn:\n%s", p.logs.String())
			}
			for i := 0; i < 4; i++ { // two minutes: past the first backoff
				p.tick()
			}
			requireCounts(t, p.log, "after the retry", map[string]int{"in/a.json": 1})
			if p.fk.has("in/a.json") {
				t.Error("in/a.json still in the bucket after the retry")
			}
			if n := p.actions(action); n != 1 {
				t.Errorf("%s ran %d times, want once", action, n)
			}
		})
	}
}
