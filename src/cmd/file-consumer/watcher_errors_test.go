package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Tests for #409: a watch folder that cannot be read shows up in the File
// watcher panel as an "error" event (throttled, with a message that names the
// folder relative to the tenant's root and never the path on the volume), the
// first good read after that says the folder is readable again, and while the
// baseline is deferred a failed checkpoint read keeps it deferred instead of
// treating everything in the folder as done.
//
// Polls are driven by hand on the dirRig's fake clock (dirread_test.go); the
// background watcher's own timer is an hour, so it never polls. Events are
// read from a subscription without blocking right after each poll: emitEvent
// runs inside pollOnce, so whatever a poll emitted is already buffered.

const (
	watchedPath = "orders/in" // configured path, relative to the tenant root
	otherConn   = "conn-someone-else"
)

// errDBRead is what the checkpoint store fails with. None of it may reach the
// panel.
var errDBRead = errors.New(`pq: password authentication failed for user "vrsky_cp_reader" at 10.1.2.3`)

// getToggleStore fails Get while failGet is set and counts Get calls; saves
// go through the savingStore and are counted there.
type getToggleStore struct {
	*savingStore
	failGet atomic.Bool
	gets    atomic.Int32
}

func (s *getToggleStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	s.gets.Add(1)
	if s.failGet.Load() {
		return nil, errDBRead
	}
	return s.savingStore.Get(ctx, tenantID, connectionID, nodeID)
}

// watchRig is a dirRig whose consumer has event subscribers: one for the
// connection, one for an unrelated connection that must stay silent.
type watchRig struct {
	*dirRig
	cp     *getToggleStore
	events chan FileEvent
	other  chan FileEvent
}

func newWatchRig(t *testing.T, path string, realReads bool) *watchRig {
	t.Helper()
	d := newDirRig(t)
	d.path = path
	d.realReads = realReads
	return &watchRig{dirRig: d, cp: &getToggleStore{savingStore: d.store}}
}

// begin starts the connection on a new pod, with every folder read failing
// when folderFailing is set (a deferred start).
func (w *watchRig) begin(folderFailing bool) {
	w.t.Helper()
	p := w.newPod()
	p.c.eventSubs = make(map[string][]chan FileEvent)
	p.c.checkpoints = w.cp
	ch, unsub := p.c.subscribeEvents(w.connID)
	other, unsubOther := p.c.subscribeEvents(otherConn)
	w.t.Cleanup(unsub)
	w.t.Cleanup(unsubOther)
	w.events, w.other = ch, other
	w.reader.setFailing(folderFailing)
	if !w.startOn(p) {
		w.t.Fatalf("start: connection not active\nlogs:\n%s", w.sink.dump())
	}
	if evs := drainEvents(w.events); len(evs) != 0 {
		w.t.Fatalf("start emitted %d events, want 0: %+v", len(evs), evs)
	}
}

// step advances the watcher clock by advance, polls once and returns what the
// poll emitted for the connection.
func (w *watchRig) step(advance time.Duration) []FileEvent {
	w.t.Helper()
	ac := w.active()
	w.clock.advance(advance)
	w.c.pollOnce(context.Background(), ac)
	if evs := drainEvents(w.other); len(evs) != 0 {
		w.t.Fatalf("events for another connection: %+v", evs)
	}
	return drainEvents(w.events)
}

func drainEvents(ch chan FileEvent) []FileEvent {
	var out []FileEvent
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

func ofType(evs []FileEvent, typ string) []FileEvent {
	var out []FileEvent
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func types(evs []FileEvent) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}

// requireNoVolumePath fails if msg gives away where the folder is on the
// volume: the volume root, the tenant's directory or the absolute folder.
func (w *watchRig) requireNoVolumePath(step, msg string) {
	w.t.Helper()
	for _, leak := range []string{w.base, w.dir(), w.tenant} {
		if strings.Contains(msg, leak) {
			w.t.Errorf("%s: message %q contains %q (a path on the volume)", step, msg, leak)
		}
	}
}

// skippedRe finds the number of skipped polls in a message, on either side of
// the word "skipped".
var skippedRe = regexp.MustCompile(`(?i)(\d+)\D{0,20}skipped|skipped\D{0,20}(\d+)`)

func skippedCount(msg string) (int, bool) {
	m := skippedRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	s := m[1]
	if s == "" {
		s = m[2]
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// --- A, C: throttling and the recovery event ---------------------------------

func TestWatcherErrors_ThrottledErrorEvents(t *testing.T) {
	type poll struct {
		advance time.Duration // on the watcher clock, before the poll
		fail    bool          // the folder read fails
		want    []string      // event types the poll emits, in order
	}
	e, i := []string{"error"}, []string{"info"}
	cases := []struct {
		name  string
		polls []poll
	}{
		{
			name: "first failure at once, then at most one per 30s",
			polls: []poll{
				{time.Hour, true, e},
				{10 * time.Second, true, nil},
				{10 * time.Second, true, nil},
				{9 * time.Second, true, nil}, // 29s since the last event
				{time.Second, true, e},       // exactly 30s
				{29 * time.Second, true, nil},
				{time.Second, true, e},
				{5 * time.Minute, true, e},
				{time.Second, true, nil},
			},
		},
		{
			name: "polls 30s or more apart emit every time",
			polls: []poll{
				{time.Hour, true, e},
				{readErrorEventEvery, true, e},
				{readErrorEventEvery, true, e},
				{time.Hour, true, e},
			},
		},
		{
			name: "recovery says so once, and a new run emits at once",
			polls: []poll{
				{time.Hour, true, e},
				{5 * time.Second, true, nil},
				{5 * time.Second, false, i},
				{5 * time.Second, true, e}, // 15s after the last error event
				{5 * time.Second, true, nil},
				{5 * time.Second, true, nil},
				{5 * time.Second, false, i},
				{5 * time.Second, false, nil},
				{time.Hour, false, nil},
				{time.Second, true, e},
				{time.Hour, false, i},
			},
		},
		{
			name: "recovery after a single failed poll",
			polls: []poll{
				{time.Hour, true, e},
				{time.Second, false, i},
				{time.Second, false, nil},
			},
		},
		{
			name: "good reads only",
			polls: []poll{
				{time.Hour, false, nil},
				{5 * time.Second, false, nil},
				{time.Hour, false, nil},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWatchRig(t, watchedPath, false)
			w.write("baseline.json", []byte(`{"baseline":true}`))
			w.begin(false)
			for n, p := range tc.polls {
				w.reader.setFailing(p.fail)
				evs := w.step(p.advance)
				if got := types(evs); !slices.Equal(got, p.want) {
					t.Fatalf("poll %d (+%s, fail=%v): events %v, want %v (%+v)", n+1, p.advance, p.fail, got, p.want, evs)
				}
			}
			w.requireTotal("end", 0)
		})
	}
}

// --- B: what the message says ------------------------------------------------

func TestWatcherErrors_MessageNamesFolderAndReason(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		realReads bool
		// breakIt makes folder reads fail; fixIt makes them work again.
		breakIt, fixIt func(w *watchRig)
		// wantAny: the error message contains at least one of these
		// (case-insensitive).
		wantAny []string
	}{
		{
			name:    "injected read error, nested path",
			path:    watchedPath,
			breakIt: func(w *watchRig) { w.reader.setFailing(true) },
			fixIt:   func(w *watchRig) { w.reader.setFailing(false) },
			wantAny: []string{errInjectedRead.Error()},
		},
		{
			name:    "injected read error, default path",
			path:    restartNodes,
			breakIt: func(w *watchRig) { w.reader.setFailing(true) },
			fixIt:   func(w *watchRig) { w.reader.setFailing(false) },
			wantAny: []string{errInjectedRead.Error()},
		},
		{
			name:      "folder really removed",
			path:      watchedPath,
			realReads: true,
			breakIt: func(w *watchRig) {
				if err := os.RemoveAll(w.dir()); err != nil {
					w.t.Fatal(err)
				}
			},
			fixIt: func(w *watchRig) {
				if err := os.MkdirAll(w.dir(), 0o755); err != nil {
					w.t.Fatal(err)
				}
			},
			wantAny: []string{"missing"},
		},
		{
			name:      "folder not readable (permission denied)",
			path:      watchedPath,
			realReads: true,
			breakIt: func(w *watchRig) {
				if os.Geteuid() == 0 {
					w.t.Skip("root reads a mode 000 folder")
				}
				if err := os.Chmod(w.dir(), 0); err != nil {
					w.t.Fatal(err)
				}
				w.t.Cleanup(func() { _ = os.Chmod(w.dir(), 0o755) })
			},
			fixIt: func(w *watchRig) {
				if err := os.Chmod(w.dir(), 0o755); err != nil {
					w.t.Fatal(err)
				}
			},
			wantAny: []string{"permission denied"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWatchRig(t, tc.path, tc.realReads)
			if err := os.MkdirAll(w.dir(), 0o755); err != nil {
				t.Fatal(err)
			}
			w.begin(false)
			if evs := w.step(time.Hour); len(evs) != 0 {
				t.Fatalf("good poll: events %+v, want none", evs)
			}

			tc.breakIt(w)
			const failures = 5
			offset := 0
			for k := 1; k <= failures; k++ {
				// An hour apart: every failure emits.
				errs := ofType(w.step(time.Hour), "error")
				if len(errs) != 1 {
					t.Fatalf("failed poll %d: %d error events, want 1", k, len(errs))
				}
				msg := errs[0].Message
				step := fmt.Sprintf("failed poll %d", k)
				t.Logf("%s: %s", step, msg)
				if !strings.Contains(msg, `"`+tc.path+`"`) && !strings.Contains(msg, tc.path) {
					t.Errorf("%s: message %q does not name the folder %q", step, msg, tc.path)
				}
				lower := strings.ToLower(msg)
				found := false
				for _, s := range tc.wantAny {
					if strings.Contains(lower, strings.ToLower(s)) {
						found = true
					}
				}
				if !found {
					t.Errorf("%s: message %q says none of %q", step, msg, tc.wantAny)
				}
				w.requireNoVolumePath(step, msg)

				n, ok := skippedCount(msg)
				if k == 1 {
					if ok || strings.Contains(lower, "skipped") {
						t.Errorf("%s: first failure's message %q mentions skipped polls", step, msg)
					}
					continue
				}
				if !ok {
					t.Errorf("%s: message %q does not say how many polls were skipped", step, msg)
					continue
				}
				// The count is the failed polls so far, counted with or
				// without this one, and it grows by one per failed poll.
				if k == 2 {
					offset = k - n
					if offset != 0 && offset != 1 {
						t.Errorf("%s: message %q says %d polls skipped, want %d or %d", step, msg, n, k-1, k)
					}
				} else if k-n != offset {
					t.Errorf("%s: message %q says %d polls skipped, want %d", step, msg, n, k-offset)
				}
			}

			tc.fixIt(w)
			evs := w.step(time.Hour)
			infos := ofType(evs, "info")
			if len(infos) != 1 || len(ofType(evs, "error")) != 0 {
				t.Fatalf("first good read: events %+v, want exactly one info and no error", evs)
			}
			msg := infos[0].Message
			t.Logf("recovery: %s", msg)
			if !strings.Contains(msg, tc.path) {
				t.Errorf("recovery: message %q does not name the folder %q", msg, tc.path)
			}
			if !strings.Contains(strings.ToLower(msg), "again") {
				t.Errorf("recovery: message %q does not say the folder can be read again", msg)
			}
			w.requireNoVolumePath("recovery", msg)
			if evs := w.step(time.Hour); len(evs) != 0 {
				t.Errorf("second good read: events %+v, want none", evs)
			}
		})
	}
}

// --- D: no error events without a failure ------------------------------------

func TestWatcherErrors_NoEventsWithoutFailures(t *testing.T) {
	w := newWatchRig(t, watchedPath, false)
	w.write("baseline.json", []byte(`{"baseline":true}`))
	w.begin(false)

	var all []FileEvent
	a := []byte(`{"a":1}`)
	w.write("a.json", a)
	all = append(all, w.step(time.Hour)...)
	all = append(all, w.step(time.Hour)...)
	w.requireOnce("published", "a.json", a)
	w.remove("a.json")
	all = append(all, w.step(time.Hour)...)
	for k := 0; k < 5; k++ {
		all = append(all, w.step(5*time.Second)...)
	}

	want := []string{"added", "deleted"}
	if got := types(all); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v (%+v)", got, want, all)
	}
	if all[0].Filename != "a.json" || all[1].Filename != "a.json" {
		t.Errorf("events for %q and %q, want a.json both times", all[0].Filename, all[1].Filename)
	}
}

// --- E: a failed checkpoint read keeps a deferred baseline deferred ----------

func TestWatcherErrors_DeferredCheckpointReadFailureStaysDeferred(t *testing.T) {
	p := []byte(`{"p":"published before the stop"}`)
	x := []byte(`{"x":"arrived while stopped"}`)
	during := []byte(`{"during":"arrived during the outage"}`)
	later := []byte(`{"later":"arrived after the baseline"}`)

	cases := []struct {
		name    string
		prepare func(w *watchRig)
		resume  bool
		// folderFails polls fail on the folder, then cpFails polls read the
		// folder but fail on the checkpoint.
		folderFails, cpFails int
		// want is published once each after the baseline; never is never
		// published.
		want  map[string][]byte
		never []string
	}{
		{
			name: "resume, folder then checkpoint unreadable",
			prepare: func(w *watchRig) {
				w.write("p.json", p)
				w.write("x.json", x)
				w.seed("p.json")
			},
			resume:      true,
			folderFails: 2,
			cpFails:     3,
			want:        map[string][]byte{"x.json": x, "during.json": during},
			never:       []string{"p.json"},
		},
		{
			name: "resume, checkpoint unreadable on the first readable poll",
			prepare: func(w *watchRig) {
				w.write("p.json", p)
				w.write("x.json", x)
				w.seed("p.json")
			},
			resume:  true,
			cpFails: 1,
			want:    map[string][]byte{"x.json": x, "during.json": during},
			never:   []string{"p.json"},
		},
		{
			name: "resume, checkpoint unreadable for many polls",
			prepare: func(w *watchRig) {
				w.write("p.json", p)
				w.write("x.json", x)
				w.seed("p.json")
			},
			resume:      true,
			folderFails: 1,
			cpFails:     8,
			want:        map[string][]byte{"x.json": x, "during.json": during},
			never:       []string{"p.json"},
		},
		{
			name:        "first start, checkpoint unreadable",
			prepare:     func(w *watchRig) { w.write("old.json", []byte(`{"old":1}`)) },
			folderFails: 1,
			cpFails:     3,
			never:       []string{"old.json", "during.json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWatchRig(t, watchedPath, false)
			tc.prepare(w)
			saved, saves := w.saved(), w.store.saves.Load()
			if tc.resume && saved == nil {
				t.Fatal("setup: no checkpoint")
			}

			w.begin(true)
			ac := w.active()
			if !ac.baselineDeferred {
				t.Fatal("setup: baseline not deferred after a start that could not read its folder")
			}
			w.requireCheckpoint("deferred start", saved, saves)
			w.write("during.json", during)

			for k := 1; k <= tc.folderFails; k++ {
				step := fmt.Sprintf("folder unreadable, poll %d", k)
				evs := w.step(time.Hour)
				if errs := ofType(evs, "error"); len(errs) != 1 {
					t.Fatalf("%s: events %+v, want one error", step, evs)
				}
				if !ac.baselineDeferred {
					t.Fatalf("%s: baseline no longer deferred", step)
				}
				w.requireTotal(step, 0)
				w.requireCheckpoint(step, saved, saves)
			}

			w.reader.setFailing(false)
			w.cp.failGet.Store(true)
			for k := 1; k <= tc.cpFails; k++ {
				step := fmt.Sprintf("checkpoint unreadable, poll %d", k)
				gets := w.cp.gets.Load()
				evs := w.step(time.Hour)
				if w.cp.gets.Load() == gets {
					t.Fatalf("%s: the checkpoint was not read (not retried)", step)
				}
				if !ac.baselineDeferred {
					t.Fatalf("%s: baseline taken although the checkpoint could not be read", step)
				}
				w.requireTotal(step, 0)
				w.requireCheckpoint(step, saved, saves)
				errs := ofType(evs, "error")
				if len(errs) != 1 {
					t.Fatalf("%s: events %+v, want one error", step, evs)
				}
				if n := len(ofType(evs, "added")); n != 0 {
					t.Errorf("%s: %d added events, want 0", step, n)
				}
				msg := errs[0].Message
				t.Logf("%s: %s", step, msg)
				for _, leak := range []string{errDBRead.Error(), "vrsky_cp_reader", "10.1.2.3", "pq:"} {
					if strings.Contains(msg, leak) {
						t.Errorf("%s: message %q contains database error text %q", step, msg, leak)
					}
				}
				if !strings.Contains(msg, watchedPath) {
					t.Errorf("%s: message %q does not name the folder %q", step, msg, watchedPath)
				}
				w.requireNoVolumePath(step, msg)
			}

			// Both reads work: the baseline is taken from the saved set (or
			// by first-start rules), nothing is published in this poll, and the
			// panel's newest line says the folder is fine again.
			w.cp.failGet.Store(false)
			evs := w.step(time.Hour)
			if ac.baselineDeferred {
				t.Fatal("baseline poll: still deferred with the folder and the checkpoint readable")
			}
			w.requireTotal("baseline poll", 0)
			if n := len(ofType(evs, "error")); n != 0 {
				t.Errorf("baseline poll: %d error events, want 0 (%+v)", n, evs)
			}
			if n := len(ofType(evs, "info")); n != 1 {
				t.Errorf("baseline poll: %d info events, want 1 (%+v)", n, evs)
			}

			w.step(time.Hour)
			w.step(time.Hour)
			for name, content := range tc.want {
				w.requireOnce("after the baseline", name, content)
			}
			w.requireTotal("after the baseline", len(tc.want))

			w.write("later.json", later)
			w.step(time.Hour)
			w.step(time.Hour)
			w.step(time.Hour)
			w.requireOnce("end", "later.json", later)
			for name, content := range tc.want {
				w.requireOnce("end", name, content)
			}
			for _, name := range tc.never {
				w.requireCount("end", name, 0)
			}
			w.requireTotal("end", len(tc.want)+1)

			names := w.savedNames()
			for _, n := range []string{"during.json", "later.json"} {
				if !names[n] {
					t.Errorf("end: %s missing from the saved done set %v", n, slices.Sorted(maps.Keys(names)))
				}
			}
			if tc.resume && !names["p.json"] {
				t.Errorf("end: p.json dropped from the saved done set %v", slices.Sorted(maps.Keys(names)))
			}
		})
	}
}
