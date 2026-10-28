package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #398: a directory listing of the watch folder that fails, during a
// poll or at start, must change nothing. No name leaves the done set, nothing
// is saved over the checkpoint, and nothing already published goes out again
// once the folder can be read again. Before the fix a failed read looked like
// an empty folder, and the next good read published the whole folder again.
//
// Reads fail through the fileConsumer.readDir seam (or, for a missing folder,
// by really removing it). Polls are driven by hand on the fake clock of the
// restart rig (restart_test.go); the background watcher's own timer is an
// hour, so it never polls during a test.

const (
	dirConn   = "conn-dirread"
	dirTenant = "tenant-dirread"
	dirNode   = "c1" // the file node's id in nodesWithPath
)

// --- failing reader ----------------------------------------------------------

var errInjectedRead = errors.New("injected: input/output error")

// dirReader wraps os.ReadDir. It fails every call while failing is set, and
// the next failNext calls otherwise. onCall, when set, runs on every call
// before the result is returned (outside the lock), so a test can hold a
// read. Safe for use from the watcher goroutine and the start path at once.
type dirReader struct {
	mu       sync.Mutex
	failing  bool
	failNext int
	calls    int
	onCall   func(call int)
}

func (d *dirReader) read(dir string) ([]os.DirEntry, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	fail := d.failing || d.failNext > 0
	if d.failNext > 0 {
		d.failNext--
	}
	hook := d.onCall
	d.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	if fail {
		return nil, &os.PathError{Op: "readdirent", Path: dir, Err: errInjectedRead}
	}
	return os.ReadDir(dir)
}

func (d *dirReader) setFailing(v bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failing = v
}

func (d *dirReader) failNextCalls(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failNext = n
}

func (d *dirReader) setOnCall(fn func(call int)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onCall = fn
}

func (d *dirReader) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// --- checkpoint store that counts saves --------------------------------------

type savingStore struct {
	*lockedStore
	saves atomic.Int32
}

func (s *savingStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.saves.Add(1)
	return s.lockedStore.Save(ctx, cp)
}

// --- log capture -------------------------------------------------------------

// logRecord is one log line with every attribute that reached the handler,
// including those added with logger.With.
type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

type logSink struct {
	mu   sync.Mutex
	recs []logRecord
}

// warnings counts records at Warn or above that carry connection_id=connID.
func (s *logSink) warnings(connID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.recs {
		if r.level >= slog.LevelWarn && r.attrs["connection_id"] == connID {
			n++
		}
	}
	return n
}

func (s *logSink) dump() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b bytes.Buffer
	for _, r := range s.recs {
		b.WriteString(r.level.String() + " " + r.msg)
		for _, k := range slices.Sorted(maps.Keys(r.attrs)) {
			b.WriteString(" " + k + "=" + r.attrs[k])
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sinkHandler records into sink and counts messages in counter (what the
// restart rig waits on). Groups are ignored.
type sinkHandler struct {
	sink    *logSink
	counter *msgCounter
	attrs   []slog.Attr
}

func (h sinkHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h sinkHandler) Handle(ctx context.Context, r slog.Record) error {
	rec := logRecord{level: r.Level, msg: r.Message, attrs: make(map[string]string)}
	for _, a := range h.attrs {
		rec.attrs[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.sink.mu.Lock()
	h.sink.recs = append(h.sink.recs, rec)
	h.sink.mu.Unlock()
	return countingHandler{c: h.counter}.Handle(ctx, r)
}

func (h sinkHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(slices.Clone(h.attrs), attrs...)
	return h
}

func (h sinkHandler) WithGroup(string) slog.Handler { return h }

// --- rig ---------------------------------------------------------------------

// dirRig is a restartRig whose consumers list the folder through reader (or
// through os.ReadDir when realReads is set) and retry a failed start read
// after 1ms instead of 1s.
type dirRig struct {
	*restartRig
	reader    *dirReader
	store     *savingStore
	sink      *logSink
	realReads bool
}

func newDirRig(t *testing.T) *dirRig {
	t.Helper()
	store := &savingStore{lockedStore: newLockedStore()}
	return &dirRig{
		restartRig: newRestartRig(t, t.TempDir(), dirConn, dirTenant, store),
		reader:     &dirReader{},
		store:      store,
		sink:       &logSink{},
	}
}

// dirPod is one consumer process, possibly not (yet) running the connection.
type dirPod struct {
	c    *fileConsumer
	fdb  *fakeConnDB
	logs *msgCounter
}

func (d *dirRig) newPod() *dirPod {
	d.t.Helper()
	fdb := &fakeConnDB{connID: d.connID, tenant: d.tenant, nodes: nodesWithPath(d.path)}
	db := sql.OpenDB(fakeConnector{db: fdb})
	d.t.Cleanup(func() { _ = db.Close() })
	logs := &msgCounter{}
	c := &fileConsumer{
		db:                db,
		logger:            slog.New(sinkHandler{sink: d.sink, counter: logs}),
		publish:           d.probe.publish,
		inlineMax:         1 << 20,
		baseDir:           d.base,
		activeConnections: make(map[string]*ActiveConnection),
		pollInterval:      restartInterval,
		now:               d.clock.now,
		checkpoints:       d.store,
		readDir:           d.reader.read,
		startReadBackoff:  time.Millisecond,
	}
	if d.realReads {
		c.readDir = nil
	}
	d.t.Cleanup(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, ac := range c.activeConnections {
			ac.Cancel()
		}
	})
	return &dirPod{c: c, fdb: fdb, logs: logs}
}

// startOn runs one start for the connection on p. When it made the connection
// active it waits for the watcher goroutine and makes p the rig's current
// lifetime. It reports whether the connection became active.
func (d *dirRig) startOn(p *dirPod) bool {
	d.t.Helper()
	if d.c != nil {
		d.t.Fatal("startOn: a lifetime is still running")
	}
	p.c.startConnection(context.Background(), d.connID, d.tenant)
	if p.c.getActiveConnection(d.connID) == nil {
		return false
	}
	harness.Eventually(d.t, restartWait, "watcher goroutine started", func() bool {
		return p.logs.count(watchStartedMsg) == 1
	})
	d.c, d.logs = p.c, p.logs
	return true
}

// start boots a new pod and requires the start to succeed.
func (d *dirRig) start() *dirPod {
	d.t.Helper()
	p := d.newPod()
	if !d.startOn(p) {
		d.t.Fatalf("start: connection not active\nlogs:\n%s", d.sink.dump())
	}
	return p
}

func (d *dirRig) active() *ActiveConnection {
	d.t.Helper()
	ac := d.c.getActiveConnection(d.connID)
	if ac == nil {
		d.t.Fatal("connection not active")
	}
	return ac
}

// seed writes a checkpoint for the current folder, as a previous lifetime
// would have left it. It is not counted as a save.
func (d *dirRig) seed(names ...string) {
	d.t.Helper()
	state, err := json.Marshal(knownState{WatchDir: d.dir(), Known: names})
	if err != nil {
		d.t.Fatal(err)
	}
	if err := d.store.lockedStore.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: d.tenant, ConnectionID: d.connID, NodeID: dirNode,
		LastProcessedAt: time.Now().UTC(), State: state,
	}); err != nil {
		d.t.Fatal(err)
	}
}

// saved is the checkpoint state as stored right now, nil if there is none.
func (d *dirRig) saved() []byte {
	d.t.Helper()
	cp, err := d.store.Get(context.Background(), d.tenant, d.connID, dirNode)
	if err != nil {
		d.t.Fatal(err)
	}
	if cp == nil {
		return nil
	}
	return cp.State
}

// savedNames decodes the stored done set.
func (d *dirRig) savedNames() map[string]bool {
	d.t.Helper()
	raw := d.saved()
	if raw == nil {
		d.t.Fatal("no checkpoint saved")
	}
	var st knownState
	if err := json.Unmarshal(raw, &st); err != nil {
		d.t.Fatalf("decode checkpoint: %v", err)
	}
	if st.WatchDir != d.dir() {
		d.t.Fatalf("checkpoint for %q, want %q", st.WatchDir, d.dir())
	}
	out := make(map[string]bool, len(st.Known))
	for _, n := range st.Known {
		out[n] = true
	}
	return out
}

// frozen is what a failed read must leave alone.
type frozen struct {
	known   map[string]bool
	pending map[string]fileState
	saved   []byte
	saves   int32
}

func (d *dirRig) freeze() frozen {
	d.t.Helper()
	ac := d.active()
	return frozen{
		known:   maps.Clone(ac.knownFiles),
		pending: maps.Clone(ac.pending),
		saved:   d.saved(),
		saves:   d.store.saves.Load(),
	}
}

func sameFileState(a, b fileState) bool {
	return a.size == b.size && a.modTime.Equal(b.modTime) && a.seenAt.Equal(b.seenAt)
}

// requireUnchanged fails unless the done set, the pending set and the stored
// checkpoint are what they were at f, and nothing was saved since.
func (d *dirRig) requireUnchanged(step string, f frozen) {
	d.t.Helper()
	ac := d.active()
	if !maps.Equal(ac.knownFiles, f.known) {
		d.t.Fatalf("%s: knownFiles = %v, want unchanged %v", step,
			slices.Sorted(maps.Keys(ac.knownFiles)), slices.Sorted(maps.Keys(f.known)))
	}
	if !maps.EqualFunc(ac.pending, f.pending, sameFileState) {
		d.t.Fatalf("%s: pending = %v, want unchanged %v", step, ac.pending, f.pending)
	}
	if got := d.store.saves.Load(); got != f.saves {
		d.t.Fatalf("%s: %d checkpoint saves, want %d (none since the read failed)", step, got, f.saves)
	}
	if got := d.saved(); !bytes.Equal(got, f.saved) {
		d.t.Fatalf("%s: checkpoint is %s, want unchanged %s", step, got, f.saved)
	}
}

// requireFailedStart fails unless a start on p left nothing behind: not
// active, no "running" status, no watcher, checkpoint untouched, and a
// warning or error logged for the connection.
func (d *dirRig) requireFailedStart(step string, p *dirPod, saved []byte, saves int32, warnsBefore int) {
	d.t.Helper()
	if p.c.getActiveConnection(d.connID) != nil {
		d.t.Fatalf("%s: connection active", step)
	}
	if _, running, _ := p.fdb.counts(); running != 0 {
		d.t.Errorf("%s: status running written %d times, want 0", step, running)
	}
	if n := p.logs.count(watchStartedMsg); n != 0 {
		d.t.Errorf("%s: %d watchers started, want 0", step, n)
	}
	if got := d.store.saves.Load(); got != saves {
		d.t.Errorf("%s: %d checkpoint saves, want %d", step, got, saves)
	}
	if got := d.saved(); !bytes.Equal(got, saved) {
		d.t.Errorf("%s: checkpoint is %s, want unchanged %s", step, got, saved)
	}
	if d.sink.warnings(d.connID) <= warnsBefore {
		d.t.Errorf("%s: nothing logged at Warn or above with connection_id=%s\nlogs:\n%s", step, d.connID, d.sink.dump())
	}
}

// --- AC 1/3: a read error between two good polls -----------------------------

func TestDirRead_FailedPollChangesNothing(t *testing.T) {
	a := []byte(`{"a":1}`)
	b := []byte(`{"b":2}`)
	part1 := []byte(`{"rows":[1,2,`)
	part2 := []byte(`3]}`)
	full := append(slices.Clone(part1), part2...)
	late := []byte(`{"late":"written during the outage"}`)

	cases := []struct {
		name        string
		failedPolls int
		// finish appends part2 to the half-written file during the outage.
		finish bool
	}{
		{name: "one failed poll", failedPolls: 1},
		{name: "one failed poll, writer finishes meanwhile", failedPolls: 1, finish: true},
		{name: "three failed polls", failedPolls: 3},
		{name: "three failed polls, writer finishes meanwhile", failedPolls: 3, finish: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDirRig(t)
			d.write("baseline.json", []byte(`{"baseline":true}`))
			d.start()

			d.write("a.json", a)
			d.write("b.json", b)
			d.polls(2)
			d.requireOnce("before the failure", "a.json", a)
			d.requireOnce("before the failure", "b.json", b)
			d.write("half.json", part1)
			d.poll()
			d.requireTotal("half.json sighted", 2)

			f := d.freeze()
			if _, ok := f.pending["half.json"]; !ok {
				t.Fatalf("setup: half.json not pending after one sighting (pending %v)", f.pending)
			}
			if f.saved == nil {
				t.Fatal("setup: no checkpoint saved after publishing")
			}

			d.reader.setFailing(true)
			if tc.finish {
				d.appendTo("half.json", part2)
			}
			d.write("late.json", late)
			for i := 0; i < tc.failedPolls; i++ {
				warns := d.sink.warnings(d.connID)
				d.poll()
				d.requireTotal("failed poll", 2)
				d.requireUnchanged("failed poll", f)
				if i == 0 && d.sink.warnings(d.connID) <= warns {
					t.Errorf("first failed poll: nothing logged at Warn or above with connection_id=%s\nlogs:\n%s",
						d.connID, d.sink.dump())
				}
			}

			d.reader.setFailing(false)
			d.polls(4)
			d.requireOnce("after recovery", "a.json", a)
			d.requireOnce("after recovery", "b.json", b)
			d.requireOnce("after recovery", "late.json", late)
			if tc.finish {
				d.requireOnce("after recovery", "half.json", full)
				d.requireNeverPayload("after recovery", part1)
			} else {
				d.requireOnce("after recovery", "half.json", part1)
			}
			d.requireCount("after recovery", "baseline.json", 0)
			d.requireTotal("after recovery", 4)

			names := d.savedNames()
			for _, n := range []string{"baseline.json", "a.json", "b.json", "half.json", "late.json"} {
				if !names[n] {
					t.Errorf("after recovery: %s missing from the saved done set %v", n, slices.Sorted(maps.Keys(names)))
				}
			}
		})
	}
}

// --- AC 2/3: a read error at start -------------------------------------------

func TestDirRead_FailedStartKeepsSavedSet(t *testing.T) {
	p := []byte(`{"p":"published before the stop"}`)
	x := []byte(`{"x":"arrived while stopped"}`)
	fresh := []byte(`{"fresh":"arrived after the failed start"}`)

	cases := []struct {
		name    string
		prepare func(d *dirRig)
		// want is how often each name is published in all, by the end.
		want map[string][]byte
		// never are names that must never be published.
		never []string
	}{
		{
			name: "saved by a previous lifetime",
			prepare: func(d *dirRig) {
				d.write("baseline.json", []byte(`{"baseline":true}`))
				d.start()
				d.write("p.json", p)
				d.polls(2)
				d.requireOnce("first lifetime", "p.json", p)
				d.shutdown(viaStopCommand)
				d.write("x.json", x)
			},
			want:  map[string][]byte{"p.json": p, "x.json": x, "fresh.json": fresh},
			never: []string{"baseline.json"},
		},
		{
			name: "seeded checkpoint",
			prepare: func(d *dirRig) {
				d.write("p.json", p)
				d.write("x.json", x)
				d.seed("p.json")
			},
			want:  map[string][]byte{"x.json": x, "fresh.json": fresh},
			never: []string{"p.json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDirRig(t)
			tc.prepare(d)
			published := len(d.probe.published())
			saved, saves := d.saved(), d.store.saves.Load()
			if saved == nil {
				t.Fatal("setup: no checkpoint")
			}

			d.reader.setFailing(true)
			pod := d.newPod()
			calls := d.reader.callCount()
			warns := d.sink.warnings(d.connID)
			if d.startOn(pod) {
				t.Fatal("start with every read failing made the connection active")
			}
			d.requireFailedStart("failed start", pod, saved, saves, warns)
			if n := d.reader.callCount() - calls; n < 2 || n > 4 {
				t.Errorf("failed start read the folder %d times, want a few retries (2 to 4)", n)
			}
			d.requireTotal("failed start", published)

			// The folder is readable again; a later start on the same pod (a
			// start command) must not be refused and must resume normally.
			d.write("fresh.json", fresh)
			d.reader.setFailing(false)
			if !d.startOn(pod) {
				t.Fatalf("start after the failed start did not make the connection active\nlogs:\n%s", d.sink.dump())
			}
			if _, running, _ := pod.fdb.counts(); running != 1 {
				t.Errorf("status running written %d times, want 1", running)
			}
			d.polls(4)
			for name, content := range tc.want {
				d.requireOnce("after the good start", name, content)
			}
			for _, name := range tc.never {
				d.requireCount("after the good start", name, 0)
			}
			d.requireTotal("after the good start", len(tc.want))
		})
	}
}

// --- AC 2/3: the start's read fails, then a retry works ----------------------

func TestDirRead_StartRetryRecovers(t *testing.T) {
	for _, failFirst := range []int{1, 3} {
		t.Run(fmt.Sprintf("resume after %d failed reads", failFirst), func(t *testing.T) {
			d := newDirRig(t)
			p := []byte(`{"p":1}`)
			x := []byte(`{"x":"arrived while stopped"}`)
			d.write("baseline.json", []byte(`{"baseline":true}`))
			d.start()
			d.write("p.json", p)
			d.polls(2)
			d.requireOnce("first lifetime", "p.json", p)
			d.shutdown(viaStopCommand)
			d.write("x.json", x)
			saved := d.saved()

			d.reader.failNextCalls(failFirst)
			calls := d.reader.callCount()
			pod := d.start()
			if n := d.reader.callCount() - calls; n < failFirst+1 {
				t.Errorf("start read the folder %d times, want at least %d", n, failFirst+1)
			}
			if _, running, _ := pod.fdb.counts(); running != 1 {
				t.Errorf("status running written %d times, want 1", running)
			}
			if got := d.saved(); !bytes.Equal(got, saved) {
				t.Errorf("checkpoint after the start is %s, want unchanged %s", got, saved)
			}

			d.polls(4)
			d.requireOnce("after the start", "x.json", x)
			d.requireOnce("after the start", "p.json", p)
			d.requireCount("after the start", "baseline.json", 0)
			d.requireTotal("after the start", 2)
		})

		t.Run(fmt.Sprintf("first start after %d failed reads", failFirst), func(t *testing.T) {
			d := newDirRig(t)
			d.write("old-1.json", []byte(`{"old":1}`))
			d.write("old-2.json", []byte(`{"old":2}`))

			d.reader.failNextCalls(failFirst)
			d.start()
			d.polls(4)
			d.requireTotal("first start, existing files only", 0)
			names := d.savedNames()
			if !names["old-1.json"] || !names["old-2.json"] {
				t.Errorf("first start saved %v, want the files already in the folder", slices.Sorted(maps.Keys(names)))
			}

			n := []byte(`{"new":1}`)
			d.write("new.json", n)
			d.polls(2)
			d.requireOnce("after the first start", "new.json", n)
			d.polls(3)
			d.requireTotal("end", 1)
		})
	}
}

// --- AC 3: a read that recovers during polling -------------------------------

func TestDirRead_RecoveredPollResumes(t *testing.T) {
	a := []byte(`{"a":1}`)
	b := []byte(`{"b":1}`)
	c := []byte(`{"c":"written during the outage"}`)
	d := newDirRig(t)
	d.start()
	d.write("a.json", a)
	d.write("b.json", b)
	d.polls(2)
	d.requireTotal("before the outage", 2)

	f := d.freeze()
	d.reader.setFailing(true)
	d.remove("b.json")
	d.write("c.json", c)
	d.polls(3)
	d.requireTotal("during the outage", 2)
	d.requireUnchanged("during the outage", f)

	d.reader.setFailing(false)
	d.poll()
	ac := d.active()
	if ac.knownFiles["b.json"] {
		t.Error("b.json, deleted during the outage, still in the done set after a good read")
	}
	if !ac.knownFiles["a.json"] {
		t.Error("a.json dropped from the done set")
	}
	names := d.savedNames()
	if names["b.json"] || !names["a.json"] {
		t.Errorf("saved done set after a good read = %v, want a.json without b.json", slices.Sorted(maps.Keys(names)))
	}

	d.polls(2)
	d.requireOnce("after recovery", "c.json", c)
	d.requireOnce("after recovery", "a.json", a)
	d.requireOnce("after recovery", "b.json", b)
	d.requireTotal("after recovery", 3)

	// b.json was pruned, so a new b.json is a new file.
	b2 := []byte(`{"b":2,"recreated":true}`)
	d.write("b.json", b2)
	d.polls(2)
	envs := d.probe.forName("b.json")
	if len(envs) != 2 {
		t.Fatalf("re-created b.json: %d envelopes, want 2 (%s)", len(envs), describe(d.probe.published()))
	}
	assertWatchEnvelope(t, envs[1], "b.json", b2)
	d.requireOnce("end", "a.json", a)
	d.requireTotal("end", 4)
}

// --- a stop while the start is retrying --------------------------------------

func TestDirRead_StopDuringStartRetries(t *testing.T) {
	cases := []struct {
		name string
		arm  func(r *dirReader)
	}{
		{"later reads would work", func(r *dirReader) { r.failNextCalls(1) }},
		{"reads keep failing", func(r *dirReader) { r.setFailing(true) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDirRig(t)
			p := []byte(`{"p":1}`)
			d.start()
			d.write("p.json", p)
			d.polls(2)
			d.requireOnce("first lifetime", "p.json", p)
			d.shutdown(viaStopCommand)
			saved, saves := d.saved(), d.store.saves.Load()

			pod := d.newPod()
			base := d.reader.callCount()
			entered, release := make(chan struct{}), make(chan struct{})
			d.reader.setOnCall(func(call int) {
				if call == base+1 {
					close(entered)
					select {
					case <-release:
					case <-time.After(restartWait):
					}
				}
			})
			tc.arm(d.reader)

			startDone := make(chan struct{})
			go func() {
				defer close(startDone)
				pod.c.startConnection(context.Background(), d.connID, d.tenant)
			}()
			waitDone(t, entered, "start reaching its first folder read")
			pod.c.handleStopCommand(d.cmd())
			close(release)
			waitDone(t, startDone, "start after the stop")

			if pod.c.getActiveConnection(d.connID) != nil {
				t.Fatal("connection active after a stop during the start's retries")
			}
			if _, running, _ := pod.fdb.counts(); running != 0 {
				t.Errorf("abandoned start wrote status running %d times, want 0", running)
			}
			if n := pod.logs.count(watchStartedMsg); n != 0 {
				t.Errorf("abandoned start started %d watchers, want 0", n)
			}
			if n := d.reader.callCount() - base; n != 1 {
				t.Errorf("start read the folder %d times, want 1 (no attempt after the stop)", n)
			}
			if got := d.store.saves.Load(); got != saves {
				t.Errorf("%d checkpoint saves, want %d", got, saves)
			}
			if got := d.saved(); !bytes.Equal(got, saved) {
				t.Errorf("checkpoint is %s, want unchanged %s", got, saved)
			}

			// A start after the stop works and resumes normally.
			d.reader.setOnCall(nil)
			d.reader.setFailing(false)
			d.reader.failNextCalls(0)
			q := []byte(`{"q":1}`)
			d.write("q.json", q)
			if !d.startOn(pod) {
				t.Fatalf("start after the stop did not make the connection active\nlogs:\n%s", d.sink.dump())
			}
			d.polls(3)
			d.requireOnce("after the restart", "p.json", p)
			d.requireOnce("after the restart", "q.json", q)
			d.requireTotal("after the restart", 2)
		})
	}
}

// --- a missing folder during a poll ------------------------------------------

func TestDirRead_MissingFolderDuringPoll(t *testing.T) {
	a := []byte(`{"a":1}`)
	b := []byte(`{"b":1}`)
	d := newDirRig(t)
	d.realReads = true // no seam: the folder really goes away
	d.start()
	d.write("a.json", a)
	d.write("b.json", b)
	d.polls(2)
	d.requireTotal("before the folder goes", 2)

	f := d.freeze()
	if err := os.RemoveAll(d.dir()); err != nil {
		t.Fatal(err)
	}
	warns := d.sink.warnings(d.connID)
	d.polls(2)
	d.requireTotal("folder missing", 2)
	d.requireUnchanged("folder missing", f)
	if d.sink.warnings(d.connID) <= warns {
		t.Errorf("missing folder: nothing logged at Warn or above with connection_id=%s\nlogs:\n%s", d.connID, d.sink.dump())
	}

	// The folder comes back with the same files (a remount).
	d.write("a.json", a)
	d.write("b.json", b)
	d.polls(3)
	d.requireOnce("folder back", "a.json", a)
	d.requireOnce("folder back", "b.json", b)
	d.requireTotal("folder back", 2)

	c := []byte(`{"c":1}`)
	d.write("c.json", c)
	d.polls(2)
	d.requireOnce("new file after the folder came back", "c.json", c)
	d.requireTotal("end", 3)
}
