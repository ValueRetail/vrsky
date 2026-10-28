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
// A start that cannot read its folder still starts, with the baseline
// deferred to the first poll whose read works.
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

// dirReader wraps os.ReadDir and fails every call while failing is set. Safe
// for use from the watcher goroutine and the start path at once.
type dirReader struct {
	mu      sync.Mutex
	failing bool
}

func (d *dirReader) read(dir string) ([]os.DirEntry, error) {
	d.mu.Lock()
	fail := d.failing
	d.mu.Unlock()
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

// dirRig is a restartRig whose consumers list the folder through reader, or
// through os.ReadDir when realReads is set.
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
// would have left it: since #420 with the stamp of each name that is in the
// folder. It is not counted as a save.
func (d *dirRig) seed(names ...string) {
	d.t.Helper()
	files := make(map[string]savedStamp)
	for _, name := range names {
		if st, ok := statStamp(d.dir(), name); ok {
			files[name] = savedStamp{Size: st.size, MTime: st.mtimeSec, MTimeNsec: st.mtimeNsec}
		}
	}
	state, err := json.Marshal(knownState{WatchDir: d.dir(), Known: names, Files: files})
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

// saved is the checkpoint state as stored right now: the snapshot row, then
// the changes row (#426) after a newline. nil if there is neither.
func (d *dirRig) saved() []byte {
	d.t.Helper()
	snap, changes := d.savedRow(dirNode), d.savedRow(dirNode+changesSuffix)
	if snap == nil && changes == nil {
		return nil
	}
	return append(append(append([]byte(nil), snap...), '\n'), changes...)
}

// savedRow is the state of one checkpoint row of the connection, nil if there
// is none.
func (d *dirRig) savedRow(nodeID string) []byte {
	d.t.Helper()
	cp, err := d.store.Get(context.Background(), d.tenant, d.connID, nodeID)
	if err != nil {
		d.t.Fatal(err)
	}
	if cp == nil {
		return nil
	}
	return cp.State
}

// savedNames decodes the stored done set: the snapshot, with the changes row
// applied when it is on that snapshot (#426).
func (d *dirRig) savedNames() map[string]bool {
	d.t.Helper()
	raw := d.savedRow(dirNode)
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
	var ch changesState
	if rawCh := d.savedRow(dirNode + changesSuffix); rawCh != nil && st.Gen != "" {
		if err := json.Unmarshal(rawCh, &ch); err != nil {
			d.t.Fatalf("decode changes row: %v", err)
		}
	}
	if ch.Base != st.Gen || ch.WatchDir != d.dir() {
		return out
	}
	for n := range ch.Files {
		out[n] = true
	}
	for _, n := range ch.Known {
		out[n] = true
	}
	for _, n := range ch.Gone {
		delete(out, n)
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

// --- a folder unreadable at start: the baseline is deferred ------------------

// startDeferred starts a new pod while every folder read fails. The start
// must go ahead: active, "running" written once, a watcher running, and one
// warning with the connection id. It returns the pod and the number of
// warnings for the connection right after the start.
func (d *dirRig) startDeferred() (*dirPod, int) {
	d.t.Helper()
	d.reader.setFailing(true)
	warns := d.sink.warnings(d.connID)
	pod := d.start()
	if _, running, _ := pod.fdb.counts(); running != 1 {
		d.t.Errorf("deferred start: status running written %d times, want 1", running)
	}
	after := d.sink.warnings(d.connID)
	if after != warns+1 {
		d.t.Errorf("deferred start: %d records at Warn or above with connection_id=%s, want 1\nlogs:\n%s",
			after-warns, d.connID, d.sink.dump())
	}
	return pod, after
}

// requireCheckpoint fails unless the stored checkpoint is saved and there
// were saves saves in all.
func (d *dirRig) requireCheckpoint(step string, saved []byte, saves int32) {
	d.t.Helper()
	if got := d.store.saves.Load(); got != saves {
		d.t.Fatalf("%s: %d checkpoint saves, want %d", step, got, saves)
	}
	if got := d.saved(); !bytes.Equal(got, saved) {
		d.t.Fatalf("%s: checkpoint is %s, want unchanged %s", step, got, saved)
	}
}

func TestDirRead_DeferredBaseline(t *testing.T) {
	p := []byte(`{"p":"published before the stop"}`)
	x := []byte(`{"x":"arrived while stopped"}`)
	during := []byte(`{"during":"arrived while the folder could not be read"}`)
	later := []byte(`{"later":"arrived after the baseline"}`)

	cases := []struct {
		name    string
		prepare func(d *dirRig)
		// resume: a checkpoint for the folder exists before the start.
		resume bool
		// want are the files the first good read finds new, each published
		// once; never are names that must never be published again.
		want  map[string][]byte
		never []string
		// alreadyOnce are names published once before the deferred start.
		alreadyOnce map[string][]byte
	}{
		{
			name: "resume, saved by a previous lifetime",
			prepare: func(d *dirRig) {
				d.write("baseline.json", []byte(`{"baseline":true}`))
				d.start()
				d.write("p.json", p)
				d.polls(2)
				d.requireOnce("first lifetime", "p.json", p)
				d.shutdown(viaStopCommand)
				d.write("x.json", x)
			},
			resume:      true,
			want:        map[string][]byte{"x.json": x, "during.json": during},
			never:       []string{"baseline.json"},
			alreadyOnce: map[string][]byte{"p.json": p},
		},
		{
			name: "resume, seeded checkpoint",
			prepare: func(d *dirRig) {
				d.write("p.json", p)
				d.write("x.json", x)
				d.seed("p.json")
			},
			resume: true,
			want:   map[string][]byte{"x.json": x, "during.json": during},
			never:  []string{"p.json"},
		},
		{
			name: "first start",
			prepare: func(d *dirRig) {
				d.write("old-1.json", []byte(`{"old":1}`))
				d.write("old-2.json", []byte(`{"old":2}`))
			},
			never: []string{"old-1.json", "old-2.json", "during.json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDirRig(t)
			tc.prepare(d)
			published := len(d.probe.published())
			saved, saves := d.saved(), d.store.saves.Load()
			if tc.resume && saved == nil {
				t.Fatal("setup: no checkpoint")
			}

			_, warns := d.startDeferred()
			d.requireCheckpoint("deferred start", saved, saves)

			// Deferred: many polls with the folder unreadable publish and save
			// nothing, and only the start's warning is at Warn.
			d.write("during.json", during)
			d.polls(8)
			d.requireTotal("while deferred", published)
			d.requireCheckpoint("while deferred", saved, saves)
			if got := d.sink.warnings(d.connID); got != warns {
				t.Errorf("while deferred: %d more records at Warn or above, want 0 (repeats are Debug)\nlogs:\n%s",
					got-warns, d.sink.dump())
			}

			// The first good read takes the baseline and publishes nothing.
			d.reader.setFailing(false)
			d.poll()
			d.requireTotal("baseline poll", published)
			if tc.resume {
				// Nothing saved was pruned, so the saved set stays as it was.
				d.requireCheckpoint("baseline poll", saved, saves)
			} else {
				names := d.savedNames()
				for _, n := range []string{"old-1.json", "old-2.json", "during.json"} {
					if !names[n] {
						t.Errorf("baseline poll: %s missing from the saved first-start set %v", n, slices.Sorted(maps.Keys(names)))
					}
				}
			}

			// From the next poll on it runs normally: new files are pending on
			// one poll and published on the next.
			d.poll()
			d.requireTotal("first normal poll", published)
			d.poll()
			for name, content := range tc.want {
				d.requireOnce("second normal poll", name, content)
			}
			d.requireTotal("second normal poll", published+len(tc.want))

			d.write("later.json", later)
			d.polls(2)
			d.requireOnce("after the baseline", "later.json", later)
			d.polls(3)
			for name, content := range tc.want {
				d.requireOnce("end", name, content)
			}
			for name, content := range tc.alreadyOnce {
				d.requireOnce("end", name, content)
			}
			for _, name := range tc.never {
				d.requireCount("end", name, 0)
			}
			d.requireTotal("end", published+len(tc.want)+1)
		})
	}
}

// The saved set is never written while the baseline is deferred, whatever
// happens in the folder meanwhile, including a saved name being deleted and
// files arriving; and a pod shutdown in that state saves nothing either.
func TestDirRead_DeferredStartNeverOverwritesSavedSet(t *testing.T) {
	d := newDirRig(t)
	p := []byte(`{"p":1}`)
	q := []byte(`{"q":1}`)
	d.start()
	d.write("p.json", p)
	d.write("q.json", q)
	d.polls(2)
	d.requireTotal("first lifetime", 2)
	d.shutdown(viaStopCommand)
	saved, saves := d.saved(), d.store.saves.Load()
	if saved == nil {
		t.Fatal("setup: no checkpoint")
	}

	d.startDeferred()
	d.requireCheckpoint("deferred start", saved, saves)
	for i, change := range []func(){
		func() { d.write("new-1.json", []byte(`{"n":1}`)) },
		func() { d.remove("q.json") },
		func() { d.write("new-2.json", []byte(`{"n":2}`)) },
		func() {},
	} {
		change()
		d.poll()
		d.requireCheckpoint(fmt.Sprintf("failed poll %d", i+1), saved, saves)
	}
	d.requireTotal("while deferred", 2)
	d.shutdown(viaPodShutdown)
	d.requireCheckpoint("after a pod shutdown while deferred", saved, saves)
	d.requireTotal("after a pod shutdown while deferred", 2)
}

// A stop while the baseline is deferred is an ordinary stop: the connection
// is no longer active, its watcher exits, and nothing is published or saved.
// A later start with the folder readable resumes from the saved set.
func TestDirRead_StopWhileDeferred(t *testing.T) {
	for _, mode := range []shutdownMode{viaStopCommand, viaPodShutdown} {
		t.Run(string(mode), func(t *testing.T) {
			d := newDirRig(t)
			p := []byte(`{"p":1}`)
			d.start()
			d.write("p.json", p)
			d.polls(2)
			d.requireOnce("first lifetime", "p.json", p)
			d.shutdown(viaStopCommand)
			saved, saves := d.saved(), d.store.saves.Load()

			pod, _ := d.startDeferred()
			q := []byte(`{"q":"arrived while deferred"}`)
			d.write("q.json", q)
			d.polls(3)
			d.shutdown(mode) // waits for the watcher goroutine to exit
			if pod.c.getActiveConnection(d.connID) != nil {
				t.Fatal("connection still active after the stop")
			}
			d.requireTotal("after the stop", 1)
			d.requireCheckpoint("after the stop", saved, saves)

			d.reader.setFailing(false)
			d.start()
			d.polls(3)
			d.requireOnce("after the restart", "p.json", p)
			d.requireOnce("after the restart", "q.json", q)
			d.requireTotal("after the restart", 2)
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
	// A remount brings the same files back with their mtimes. Since #420 a
	// file with another mtime is a new file under that name, so the copies
	// written below get the originals' mtimes.
	mtimes := make(map[string]time.Time)
	for _, name := range []string{"a.json", "b.json"} {
		info, err := os.Stat(d.dir() + string(os.PathSeparator) + name)
		if err != nil {
			t.Fatal(err)
		}
		mtimes[name] = info.ModTime()
	}
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
	for name, mt := range mtimes {
		if err := os.Chtimes(d.dir()+string(os.PathSeparator)+name, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
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
