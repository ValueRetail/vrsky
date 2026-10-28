package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #391: a file that landed just before a stop, restart or deploy and
// was not published yet must be published after the restart, once, while a
// file published before the stop is not published again and a connection's
// very first start still leaves the files already in its folder alone.
//
// Every assertion is about what gets published. A restart is modelled as a new
// fileConsumer (a new pod) sharing only the checkpoint store and the folder
// with the old one. Polls are driven by the test through pollOnce with a fake
// clock; the background watcher's own timer is an hour, so it never polls
// during a test. One test at the end runs the real background watcher instead.

const (
	restartInterval = time.Hour       // the consumers' poll interval; the background timer never fires
	restartWait     = 5 * time.Second // upper bound on any wait, so a regression fails instead of hanging
	restartNodes    = "inbox"         // default watch path, relative to the tenant root
	watchStoppedMsg = "Directory watch stopped"
)

// --- checkpoint store --------------------------------------------------------

// lockedStore is a goroutine-safe checkpoint.Store that copies on the way in
// and out, the way a database does, so a caller that keeps mutating what it
// saved cannot make a save look like it happened. checkpoint.InMemoryStore is
// neither locked nor copying.
type lockedStore struct {
	mu   sync.Mutex
	rows map[[3]string]checkpoint.Checkpoint
}

func newLockedStore() *lockedStore {
	return &lockedStore{rows: make(map[[3]string]checkpoint.Checkpoint)}
}

func copyCheckpoint(cp checkpoint.Checkpoint) checkpoint.Checkpoint {
	cp.State = append(json.RawMessage(nil), cp.State...)
	return cp
}

func (s *lockedStore) Save(_ context.Context, cp *checkpoint.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp.UpdatedAt = time.Now()
	s.rows[[3]string{cp.TenantID, cp.ConnectionID, cp.NodeID}] = copyCheckpoint(*cp)
	return nil
}

func (s *lockedStore) Get(_ context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.rows[[3]string{tenantID, connectionID, nodeID}]
	if !ok {
		return nil, nil
	}
	out := copyCheckpoint(cp)
	return &out, nil
}

func (s *lockedStore) Delete(_ context.Context, tenantID, connectionID, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, [3]string{tenantID, connectionID, nodeID})
	return nil
}

func (s *lockedStore) DeleteForConnection(_ context.Context, tenantID, connectionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.rows {
		if k[0] == tenantID && k[1] == connectionID {
			delete(s.rows, k)
		}
	}
	return nil
}

// snapshot is the store as it is right now, detached from later writes: what
// a new pod finds after the old one died without any chance to clean up.
func (s *lockedStore) snapshot() *lockedStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := newLockedStore()
	for k, v := range s.rows {
		out.rows[k] = copyCheckpoint(v)
	}
	return out
}

// --- clock -------------------------------------------------------------------

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// --- rig ---------------------------------------------------------------------

// shutdownMode is how the running consumer goes away before the restart.
type shutdownMode string

const (
	// The operator stops the connection (NATS stop command); the pod stays.
	viaStopCommand shutdownMode = "stop command"
	// The pod is shut down (deploy, rollout): the SDK runner calls Stop.
	viaPodShutdown shutdownMode = "pod shutdown"
	// The pod dies without any chance to clean up (OOM kill, node loss): the
	// new pod sees the store exactly as it was right after the last poll.
	viaCrash shutdownMode = "crash"
)

var allShutdownModes = []shutdownMode{viaStopCommand, viaPodShutdown, viaCrash}

// restartRig is one file connection across several consumer lifetimes. The
// probe is shared by every lifetime, so counts are totals since the test
// began.
type restartRig struct {
	t      *testing.T
	base   string
	connID string
	tenant string
	path   string // the file node's configured path, relative to the tenant root
	store  checkpoint.Store
	// db, when set, is used for the connection lookup and status writes
	// instead of a fakeConnDB (the Postgres test).
	db    *sql.DB
	clock *fakeClock
	probe *recordingProbe

	c    *fileConsumer // the current lifetime, nil while stopped
	logs *msgCounter
	// realInterval, when set, is the consumers' poll interval instead of
	// restartInterval: the background watcher then polls on its own.
	realInterval time.Duration
}

func newRestartRig(t *testing.T, base, connID, tenant string, store checkpoint.Store) *restartRig {
	t.Helper()
	return &restartRig{
		t: t, base: base, connID: connID, tenant: tenant, path: restartNodes,
		store: store, clock: newFakeClock(), probe: &recordingProbe{},
	}
}

func (r *restartRig) dir() string { return filepath.Join(r.base, r.tenant, r.path) }

func (r *restartRig) file(name string) string { return filepath.Join(r.dir(), name) }

// boot starts a new consumer (a new pod) for the connection and waits until
// its watcher goroutine is running.
func (r *restartRig) boot() {
	r.t.Helper()
	if r.c != nil {
		r.t.Fatal("boot: the previous lifetime is still running")
	}
	db := r.db
	if db == nil {
		fdb := &fakeConnDB{connID: r.connID, tenant: r.tenant, nodes: nodesWithPath(r.path)}
		db = sql.OpenDB(fakeConnector{db: fdb})
		r.t.Cleanup(func() { _ = db.Close() })
	}
	logs := &msgCounter{}
	interval := restartInterval
	if r.realInterval > 0 {
		interval = r.realInterval
	}
	c := &fileConsumer{
		db:                db,
		logger:            slog.New(countingHandler{c: logs}),
		publish:           r.probe.publish,
		inlineMax:         1 << 20,
		baseDir:           r.base,
		activeConnections: make(map[string]*ActiveConnection),
		pollInterval:      interval,
		now:               r.clock.now,
		checkpoints:       r.store,
	}
	r.t.Cleanup(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, ac := range c.activeConnections {
			ac.Cancel()
		}
	})
	c.startConnection(context.Background(), r.connID, r.tenant)
	if c.getActiveConnection(r.connID) == nil {
		r.t.Fatalf("boot: connection %s not active after startConnection", r.connID)
	}
	harness.Eventually(r.t, restartWait, "watcher goroutine started", func() bool {
		return logs.count(watchStartedMsg) == 1
	})
	r.c, r.logs = c, logs
}

func (r *restartRig) cmd() *nats.Msg {
	data, err := json.Marshal(map[string]string{"connection_id": r.connID, "tenant_id": r.tenant})
	if err != nil {
		r.t.Fatalf("marshal command: %v", err)
	}
	return &nats.Msg{Subject: "vrsky.commands." + r.tenant + ".connection.stop", Data: data}
}

// shutdown ends the current lifetime. For the two orderly modes it waits until
// the old watcher goroutine has exited, as it has once the old pod is gone.
func (r *restartRig) shutdown(mode shutdownMode) {
	r.t.Helper()
	c := r.c
	if c == nil {
		r.t.Fatal("shutdown: nothing running")
	}
	switch mode {
	case viaStopCommand:
		c.handleStopCommand(r.cmd())
		if c.getActiveConnection(r.connID) != nil {
			r.t.Fatal("shutdown: connection still active after the stop command")
		}
		r.waitWatcherExit()
	case viaPodShutdown:
		// Stop waits out its own grace period unless ctx ends first; end it
		// once the watcher has exited so the test does not wait 2s each time.
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = c.Stop(ctx) }()
		r.waitWatcherExit()
		cancel()
		waitDone(r.t, done, "fileConsumer.Stop")
	case viaCrash:
		ls, ok := r.store.(*lockedStore)
		if !ok {
			r.t.Fatal("shutdown: a crash can only be modelled on a lockedStore")
		}
		frozen := ls.snapshot()
		if ac := c.getActiveConnection(r.connID); ac != nil {
			ac.Cancel() // only so the goroutine does not outlive the test
		}
		r.store = frozen
	default:
		r.t.Fatalf("unknown shutdown mode %q", mode)
	}
	r.c, r.logs = nil, nil
}

func (r *restartRig) waitWatcherExit() {
	r.t.Helper()
	logs := r.logs
	harness.Eventually(r.t, restartWait, "old watcher goroutine exited", func() bool {
		return logs.count(watchStoppedMsg) == 1
	})
}

// restart is shutdown followed by boot.
func (r *restartRig) restart(mode shutdownMode) {
	r.t.Helper()
	r.shutdown(mode)
	r.boot()
}

// poll means one poll interval has passed on the watcher's clock, then the
// watcher polls once.
func (r *restartRig) poll() {
	r.t.Helper()
	if r.c == nil {
		r.t.Fatal("poll: nothing running")
	}
	ac := r.c.getActiveConnection(r.connID)
	if ac == nil {
		r.t.Fatal("poll: connection not active")
	}
	r.clock.advance(restartInterval)
	r.c.pollOnce(context.Background(), ac)
}

func (r *restartRig) polls(n int) {
	r.t.Helper()
	for i := 0; i < n; i++ {
		r.poll()
	}
}

func (r *restartRig) write(name string, content []byte) {
	r.t.Helper()
	if err := os.MkdirAll(r.dir(), 0o755); err != nil {
		r.t.Fatalf("mkdir %s: %v", r.dir(), err)
	}
	if err := os.WriteFile(r.file(name), content, 0o600); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

func (r *restartRig) appendTo(name string, content []byte) {
	r.t.Helper()
	fh, err := os.OpenFile(r.file(name), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		r.t.Fatalf("open %s for append: %v", name, err)
	}
	if _, err := fh.Write(content); err != nil {
		_ = fh.Close()
		r.t.Fatalf("append %s: %v", name, err)
	}
	if err := fh.Close(); err != nil {
		r.t.Fatalf("close %s: %v", name, err)
	}
}

func (r *restartRig) remove(name string) {
	r.t.Helper()
	if err := os.Remove(r.file(name)); err != nil {
		r.t.Fatalf("remove %s: %v", name, err)
	}
}

func (r *restartRig) requireTotal(step string, want int) {
	r.t.Helper()
	if got := len(r.probe.published()); got != want {
		r.t.Fatalf("%s: %d envelopes published in total, want %d (%s)",
			step, got, want, describe(r.probe.published()))
	}
}

func (r *restartRig) requireCount(step, name string, want int) {
	r.t.Helper()
	if got := len(r.probe.forName(name)); got != want {
		r.t.Fatalf("%s: %q published %d times, want %d (%s)",
			step, name, got, want, describe(r.probe.published()))
	}
}

// requireOnce fails unless name was published exactly once, with exactly
// content, as this rig's tenant and connection.
func (r *restartRig) requireOnce(step, name string, content []byte) {
	r.t.Helper()
	r.requireCount(step, name, 1)
	env := r.probe.forName(name)[0]
	assertWatchEnvelope(r.t, env, name, content)
	if env.TenantID != r.tenant || env.IntegrationID != r.connID {
		r.t.Errorf("%s: %q published as %s/%s, want %s/%s",
			step, name, env.TenantID, env.IntegrationID, r.tenant, r.connID)
	}
}

// requireNeverPayload fails if any envelope carried exactly part, e.g. the
// first half of a file that was still being written.
func (r *restartRig) requireNeverPayload(step string, part []byte) {
	r.t.Helper()
	for _, env := range r.probe.published() {
		if bytes.Equal(env.Payload, part) {
			r.t.Fatalf("%s: published a half-written file %s=%q", step, env.Source, env.Payload)
		}
	}
}

func eachShutdownMode(t *testing.T, fn func(t *testing.T, mode shutdownMode)) {
	t.Helper()
	for _, mode := range allShutdownModes {
		t.Run(string(mode), func(t *testing.T) { fn(t, mode) })
	}
}

// --- AC 1: a file not published before the stop is published after it -------

func TestRestart_UnpublishedFilePublishedOnceAfterRestart(t *testing.T) {
	x := []byte(`{"order":"x","lines":[1,2,3]}`)
	cases := []struct {
		name string
		// before runs in the first lifetime (already booted) and leaves x in
		// the folder unpublished. stopped runs while nothing is running.
		before, stopped func(r *restartRig)
		// wantOthers are files published in the first lifetime, each once.
		wantOthers map[string][]byte
		// neverPublished are baseline files, never to be published.
		neverPublished []string
		preexisting    map[string][]byte
		doubleRestart  bool
	}{
		{
			name:   "arrived after the first start, stop before any poll",
			before: func(r *restartRig) { r.write("x.json", x) },
		},
		{
			name: "arrived and sighted once (not yet stable) before the stop",
			before: func(r *restartRig) {
				r.write("x.json", x)
				r.poll()
				r.requireTotal("first sighting before the stop", 0)
			},
		},
		{
			name:        "next to a published file and a baseline file",
			preexisting: map[string][]byte{"baseline.csv": []byte("a,b\n1,2\n")},
			before: func(r *restartRig) {
				r.write("p.json", []byte(`{"p":1}`))
				r.polls(2)
				r.requireCount("p before the stop", "p.json", 1)
				r.write("x.json", x)
				r.poll()
				r.requireTotal("x sighted before the stop", 1)
			},
			wantOthers:     map[string][]byte{"p.json": []byte(`{"p":1}`)},
			neverPublished: []string{"baseline.csv"},
		},
		{
			name:    "arrived while the connection was stopped",
			stopped: func(r *restartRig) { r.write("x.json", x) },
		},
		{
			name:          "two restarts in a row before any poll",
			before:        func(r *restartRig) { r.write("x.json", x) },
			doubleRestart: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
				r := newRestartRig(t, t.TempDir(), "conn-restart-1", "tenant-r", newLockedStore())
				for name, content := range tc.preexisting {
					r.write(name, content)
				}
				r.boot()
				if tc.before != nil {
					tc.before(r)
				}
				r.shutdown(mode)
				if tc.stopped != nil {
					tc.stopped(r)
				}
				r.boot()
				if tc.doubleRestart {
					r.restart(mode)
				}

				r.poll()
				r.poll()
				r.requireOnce("two polls after the restart", "x.json", x)

				r.polls(4)
				r.requireOnce("after further polls", "x.json", x)
				for name, content := range tc.wantOthers {
					r.requireOnce("published before the stop", name, content)
				}
				for _, name := range tc.neverPublished {
					r.requireCount("baseline file", name, 0)
				}
				r.requireTotal("end", 1+len(tc.wantOthers))

				// And it stays done across one more restart.
				r.restart(mode)
				r.polls(4)
				r.requireTotal("after another restart", 1+len(tc.wantOthers))
			})
		})
	}
}

// --- AC 2: a file published before the stop is not published again ----------

func TestRestart_PublishedFileNotPublishedAgain(t *testing.T) {
	p := []byte(`{"published":"before the stop"}`)
	q := []byte(`{"published":"in the second lifetime"}`)
	cases := []struct {
		name string
		run  func(r *restartRig, mode shutdownMode)
		want map[string][]byte
	}{
		{
			name: "published, then one restart",
			run: func(r *restartRig, mode shutdownMode) {
				r.write("p.json", p)
				r.polls(2)
				r.requireOnce("before the stop", "p.json", p)
				r.restart(mode)
				r.polls(5)
			},
			want: map[string][]byte{"p.json": p},
		},
		{
			name: "published, then two restarts, the middle one without polls",
			run: func(r *restartRig, mode shutdownMode) {
				r.write("p.json", p)
				r.polls(2)
				r.restart(mode)
				r.restart(mode)
				r.polls(5)
			},
			want: map[string][]byte{"p.json": p},
		},
		{
			name: "published in a later lifetime",
			run: func(r *restartRig, mode shutdownMode) {
				r.write("p.json", p)
				r.polls(2)
				r.restart(mode)
				r.write("q.json", q)
				r.polls(2)
				r.requireOnce("q in the second lifetime", "q.json", q)
				r.restart(mode)
				r.polls(5)
			},
			want: map[string][]byte{"p.json": p, "q.json": q},
		},
		{
			name: "published right before the stop, many files",
			run: func(r *restartRig, mode shutdownMode) {
				for i := 0; i < 20; i++ {
					r.write(manyName(i), manyContent(i))
				}
				r.polls(2)
				r.requireTotal("all published before the stop", 20)
				r.restart(mode)
				r.polls(5)
			},
			want: func() map[string][]byte {
				m := make(map[string][]byte)
				for i := 0; i < 20; i++ {
					m[manyName(i)] = manyContent(i)
				}
				return m
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
				r := newRestartRig(t, t.TempDir(), "conn-restart-2", "tenant-r", newLockedStore())
				r.boot()
				tc.run(r, mode)
				for name, content := range tc.want {
					r.requireOnce("after the restart", name, content)
				}
				r.requireTotal("after the restart", len(tc.want))

				// The restarted watcher is alive: a new file is published once.
				fresh := []byte(`{"after":"restart"}`)
				r.write("fresh.json", fresh)
				r.polls(3)
				r.requireOnce("new file after the restart", "fresh.json", fresh)
				r.requireTotal("end", len(tc.want)+1)
			})
		})
	}
}

func manyName(i int) string    { return "batch-" + string(rune('a'+i)) + ".json" }
func manyContent(i int) []byte { return []byte(`{"n":"` + string(rune('a'+i)) + `"}`) }

// --- AC 3: first start leaves the folder's existing files alone --------------

func TestRestart_FirstStartSkipsExistingFiles(t *testing.T) {
	existing := map[string][]byte{
		"old.json":   []byte(`{"old":true}`),
		"old.csv":    []byte("a,b\n"),
		"legacy.xml": []byte("<x/>"),
	}
	eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
		r := newRestartRig(t, t.TempDir(), "conn-first", "tenant-r", newLockedStore())
		for name, content := range existing {
			r.write(name, content)
		}
		r.boot()
		r.polls(5)
		r.requireTotal("first start, existing files only", 0)

		fresh := []byte(`{"new":true}`)
		r.write("new.json", fresh)
		r.polls(2)
		r.requireOnce("file written after the first start", "new.json", fresh)
		r.polls(3)
		r.requireTotal("after further polls", 1)

		// The existing files stay unpublished across a restart too.
		r.restart(mode)
		r.polls(5)
		for name := range existing {
			r.requireCount("existing file after a restart", name, 0)
		}
		r.requireOnce("after a restart", "new.json", fresh)
		r.requireTotal("after a restart", 1)
	})
}

// A first start that is followed by a restart before anything was published:
// the existing files are still the baseline, not files that "arrived before
// the stop".
func TestRestart_FirstStartThenRestartBeforeAnyPublish(t *testing.T) {
	eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
		r := newRestartRig(t, t.TempDir(), "conn-first-2", "tenant-r", newLockedStore())
		r.write("old-1.json", []byte(`{"old":1}`))
		r.write("old-2.json", []byte(`{"old":2}`))
		r.boot()
		r.restart(mode)
		r.polls(5)
		r.requireTotal("restart right after the first start", 0)
	})
}

// --- AC 4: a file still settling at the stop ---------------------------------

func TestRestart_SettlingFilePublishedOnceComplete(t *testing.T) {
	part1 := []byte(`{"rows":[1,2,3,`)
	part2 := []byte(`4,5,6]}`)
	full := append(append([]byte(nil), part1...), part2...)

	type step struct {
		desc      string
		append    bool // append part2 before this step's poll
		wantTotal int
	}
	cases := []struct {
		name            string
		sightedBefore   bool // the first lifetime polled once after part1
		appendWhileDown bool
		after           []step // polls after the restart
	}{
		{
			name:            "completed while stopped",
			sightedBefore:   true,
			appendWhileDown: true,
			after: []step{
				{desc: "first poll after the restart", wantTotal: 0},
				{desc: "second poll, unchanged", wantTotal: 1},
			},
		},
		{
			name:            "completed while stopped, never sighted before",
			appendWhileDown: true,
			after: []step{
				{desc: "first poll after the restart", wantTotal: 0},
				{desc: "second poll, unchanged", wantTotal: 1},
			},
		},
		{
			name:          "completed after the restart, before its first poll",
			sightedBefore: true,
			after: []step{
				{desc: "first poll sees the full file", append: true, wantTotal: 0},
				{desc: "second poll, unchanged", wantTotal: 1},
			},
		},
		{
			name:          "completed after the restart's first poll saw part 1",
			sightedBefore: true,
			after: []step{
				{desc: "first poll sees part 1", wantTotal: 0},
				{desc: "second poll sees the size change", append: true, wantTotal: 0},
				{desc: "third poll, unchanged", wantTotal: 1},
			},
		},
		{
			name: "completed after the restart, never sighted before",
			after: []step{
				{desc: "first poll sees part 1", wantTotal: 0},
				{desc: "second poll sees the size change", append: true, wantTotal: 0},
				{desc: "third poll, unchanged", wantTotal: 1},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
				r := newRestartRig(t, t.TempDir(), "conn-settle", "tenant-r", newLockedStore())
				r.boot()
				r.write("big.json", part1)
				if tc.sightedBefore {
					r.poll()
					r.requireTotal("part 1 sighted before the stop", 0)
				}
				r.shutdown(mode)
				if tc.appendWhileDown {
					r.appendTo("big.json", part2)
				}
				r.boot()
				for _, s := range tc.after {
					if s.append {
						r.appendTo("big.json", part2)
					}
					r.poll()
					r.requireTotal(s.desc, s.wantTotal)
				}
				r.requireOnce("end", "big.json", full)
				r.requireNeverPayload("end", part1)

				r.polls(3)
				r.requireTotal("after further polls", 1)
				r.restart(mode)
				r.polls(3)
				r.requireTotal("after another restart", 1)
			})
		})
	}
}

// --- also: edited path, deletions while stopped, isolation -------------------

// The saved state belongs to the old folder. After the watch path is edited,
// the new folder gets a first start: what is already there is not published,
// even names the old folder never had.
func TestRestart_EditedPathIsFirstStartForNewFolder(t *testing.T) {
	eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
		r := newRestartRig(t, t.TempDir(), "conn-edit", "tenant-r", newLockedStore())
		a := []byte(`{"a":1}`)
		r.boot()
		r.write("a.json", a)
		r.polls(2)
		r.requireOnce("a in the old folder", "a.json", a)
		r.shutdown(mode)

		r.path = "inbox-new"
		r.write("a.json", []byte(`{"a":"new folder"}`))
		r.write("b.json", []byte(`{"b":"new folder"}`))
		r.boot()
		r.polls(5)
		r.requireCount("existing a.json in the new folder", "a.json", 1)
		r.requireCount("existing b.json in the new folder", "b.json", 0)
		r.requireTotal("new folder, first start", 1)

		c := []byte(`{"c":1}`)
		r.write("c.json", c)
		r.polls(2)
		r.requireOnce("file written into the new folder", "c.json", c)

		r.restart(mode)
		r.polls(5)
		r.requireTotal("restart in the new folder", 2)
	})
}

// A published file deleted while the connection is stopped: nothing is
// published at the restart, the watcher still works, and a file re-created
// later under that name is new again (as without a restart).
func TestRestart_FileDeletedWhileStopped(t *testing.T) {
	eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
		r := newRestartRig(t, t.TempDir(), "conn-del", "tenant-r", newLockedStore())
		p := []byte(`{"p":1}`)
		keep := []byte(`{"keep":1}`)
		r.boot()
		r.write("p.json", p)
		r.write("keep.json", keep)
		r.polls(2)
		r.requireTotal("before the stop", 2)
		r.shutdown(mode)

		r.remove("p.json")
		r.boot()
		r.polls(5)
		r.requireTotal("restart after the delete", 2)

		q := []byte(`{"q":1}`)
		r.write("q.json", q)
		r.polls(2)
		r.requireOnce("new file after the restart", "q.json", q)

		p2 := []byte(`{"p":2,"recreated":true}`)
		r.write("p.json", p2)
		r.polls(2)
		envs := r.probe.forName("p.json")
		if len(envs) != 2 {
			t.Fatalf("re-created p.json: %d envelopes, want 2 (%s)", len(envs), describe(r.probe.published()))
		}
		assertWatchEnvelope(t, envs[1], "p.json", p2)
		r.requireOnce("untouched file", "keep.json", keep)
		r.requireTotal("end", 4)
	})
}

// Saved state is per tenant and connection. Two connections share one store
// and the same node id ("c1"); one connection's done set must neither make the
// other skip a file nor make it publish one.
func TestRestart_SavedStateIsolatedPerConnection(t *testing.T) {
	cases := []struct {
		name           string
		connA, tenantA string
		connB, tenantB string
	}{
		{"other tenant, same connection id", "conn-iso", "tenant-a", "conn-iso", "tenant-b"},
		{"other tenant, other connection", "conn-iso-a", "tenant-a", "conn-iso-b", "tenant-b"},
		{"same tenant, other connection", "conn-iso-a", "tenant-a", "conn-iso-b", "tenant-a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
				base := t.TempDir()
				store := newLockedStore()
				a := newRestartRig(t, base, tc.connA, tc.tenantA, store)
				b := newRestartRig(t, base, tc.connB, tc.tenantB, store)
				a.path, b.path = "inbox-a", "inbox-b"

				x := []byte(`{"x":"a"}`)
				u := []byte(`{"u":"a, unpublished at the stop"}`)
				a.boot()
				a.write("x.json", x)
				a.polls(2)
				a.requireOnce("A publishes x", "x.json", x)
				a.write("u.json", u)
				a.shutdown(mode)

				// B starts for the first time with x, u and y already in its
				// folder. Loading A's state would publish y (and u).
				b.store = a.store
				b.write("x.json", []byte(`{"x":"b"}`))
				b.write("u.json", []byte(`{"u":"b"}`))
				b.write("y.json", []byte(`{"y":"b"}`))
				b.boot()
				b.polls(5)
				b.requireTotal("B's first start", 0)
				z := []byte(`{"z":"b"}`)
				b.write("z.json", z)
				b.polls(2)
				b.requireOnce("B publishes z", "z.json", z)
				b.shutdown(mode)

				// A restarts after B saved its state. Loading B's state (where u
				// is baseline) would skip u for good.
				a.store = b.store
				a.boot()
				a.polls(4)
				a.requireOnce("A after the restart", "u.json", u)
				a.requireOnce("A after the restart", "x.json", x)
				a.requireTotal("A after the restart", 2)

				// And B restarted again still has its own state.
				b.store = a.store
				b.boot()
				b.polls(4)
				b.requireOnce("B after its restart", "z.json", z)
				b.requireTotal("B after its restart", 1)
			})
		})
	}
}

// --- real background watcher -------------------------------------------------

// The same restart, with polls done by the real background watcher instead of
// by the test: whatever the watcher does around pollOnce (saving the done set
// included) is exercised. The fake clock stays frozen unless the test moves
// it, so no file can become stable behind the test's back.
func TestRestart_BackgroundWatcherAcrossRestart(t *testing.T) {
	for _, mode := range []shutdownMode{viaStopCommand, viaPodShutdown} {
		t.Run(string(mode), func(t *testing.T) {
			r := newRestartRig(t, t.TempDir(), "conn-bg", "tenant-r", newLockedStore())
			r.realInterval = 5 * time.Millisecond
			r.write("baseline.json", []byte(`{"baseline":true}`))
			r.boot()

			// settle advances the fake clock while the real watcher polls,
			// until name has been published.
			settle := func(step, name string) {
				t.Helper()
				harness.Eventually(t, restartWait, step+": "+name+" published", func() bool {
					r.clock.advance(r.realInterval)
					return len(r.probe.forName(name)) > 0
				})
			}

			p := []byte(`{"p":1}`)
			r.write("p.json", p)
			settle("first lifetime", "p.json")

			// x arrives; the clock is frozen, so it cannot become stable.
			x := []byte(`{"x":"arrived before the stop"}`)
			r.write("x.json", x)
			r.shutdown(mode)
			r.requireCount("before the restart", "x.json", 0)

			r.boot()
			s := []byte(`{"sentinel":1}`)
			r.write("s.json", s)
			settle("second lifetime", "s.json")
			// x was in the folder from the restart on, s only after it, so x
			// is published no later than s.
			s2 := []byte(`{"sentinel":2}`)
			r.write("s2.json", s2)
			settle("second lifetime", "s2.json")

			r.requireOnce("after the restart", "x.json", x)
			r.requireOnce("after the restart", "p.json", p)
			r.requireOnce("after the restart", "s.json", s)
			r.requireOnce("after the restart", "s2.json", s2)
			r.requireCount("baseline", "baseline.json", 0)
			r.requireTotal("after the restart", 4)
		})
	}
}
