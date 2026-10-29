package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Tests for #563: a checkpoint row that is there but can't be decoded counts
// as no checkpoint, so the next save replaces it, while a store error still
// writes nothing (#536, #409, #500). See plans/issue-563-checkpoint-decode.md,
// acceptance criteria 1 to 8 (9 is in checkpoint_decode_pg_test.go).
//
// Postgres keeps the state as jsonb, so a row nobody can decode is valid JSON
// of the wrong shape. Pods are the adoptPod of adopt_stamp_test.go; the ones
// started here log into a levelLog so the tests can count Warn and Error
// records.

// --- logs -------------------------------------------------------------------

type levelRecord struct {
	level slog.Level
	msg   string
}

// levelLog keeps every record a logger handled, at every level.
type levelLog struct {
	mu   sync.Mutex
	recs []levelRecord
}

func (l *levelLog) count(level slog.Level) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.recs {
		if r.level == level {
			n++
		}
	}
	return n
}

func (l *levelLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = nil
}

func (l *levelLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, r := range l.recs {
		fmt.Fprintf(&b, "[%s] %s; ", r.level, r.msg)
	}
	return b.String()
}

type levelHandler struct{ l *levelLog }

func (levelHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h levelHandler) Handle(_ context.Context, r slog.Record) error {
	h.l.mu.Lock()
	defer h.l.mu.Unlock()
	h.l.recs = append(h.l.recs, levelRecord{level: r.Level, msg: r.Message})
	return nil
}
func (h levelHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h levelHandler) WithGroup(string) slog.Handler      { return h }

// --- rows -------------------------------------------------------------------

// seedRaw stores raw as the state of one checkpoint row.
func seedRaw(t *testing.T, store checkpoint.Store, tenant, conn, node, raw string) {
	t.Helper()
	if err := store.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: tenant, ConnectionID: conn, NodeID: node, State: []byte(raw),
	}); err != nil {
		t.Fatalf("seed %s: %v", node, err)
	}
}

// undecodableCase is a saved set nobody can decode. seed writes it for the
// folder dir.
type undecodableCase struct {
	name string
	seed func(t *testing.T, store checkpoint.Store, tenant, conn, dir string)
}

func undecodableCases() []undecodableCase {
	snapshotOnly := func(raw func(dir string) string) func(t *testing.T, store checkpoint.Store, tenant, conn, dir string) {
		return func(t *testing.T, store checkpoint.Store, tenant, conn, dir string) {
			seedRaw(t, store, tenant, conn, knownNode, raw(dir))
		}
	}
	return []undecodableCase{
		{
			name: "snapshot: a number for the folder",
			seed: snapshotOnly(func(string) string { return `{"watch_dir": 5, "known": ["x.csv"]}` }),
		},
		{
			name: "snapshot: an array",
			seed: snapshotOnly(func(string) string { return `["x.csv", "filler-a.csv"]` }),
		},
		{
			name: "snapshot: a string",
			seed: snapshotOnly(func(string) string { return `"x.csv"` }),
		},
		{
			name: "snapshot: this folder, names as a string",
			seed: snapshotOnly(func(dir string) string {
				return fmt.Sprintf(`{"watch_dir": %q, "gen": "g1", "known": "x.csv"}`, dir)
			}),
		},
		{
			name: "snapshot fine, its changes row can't be decoded",
			seed: func(t *testing.T, store checkpoint.Store, tenant, conn, dir string) {
				seedRaw(t, store, tenant, conn, knownNode,
					fmt.Sprintf(`{"watch_dir": %q, "gen": "g1", "known": ["filler-a.csv"]}`, dir))
				seedRaw(t, store, tenant, conn, changesNode,
					fmt.Sprintf(`{"watch_dir": %q, "base": "g1", "files": {"x.csv": "v0"}}`, dir))
			},
		},
	}
}

// requireRowsReplaced checks that both rows decode, are for dir, list every
// name in want as done, and that the changes row is on the snapshot.
func requireRowsReplaced(t *testing.T, store checkpoint.Store, tenant, conn, dir string, want []string) {
	t.Helper()
	snap := decodeSnapshot(t, rawRow(t, store, tenant, conn, knownNode))
	if snap.WatchDir != dir {
		t.Fatalf("snapshot not replaced: watch_dir %q, want %q (%+v)", snap.WatchDir, dir, snap)
	}
	for _, name := range want {
		_, stamped := snap.Files[name]
		if !containsName(snap.Known, name) && !stamped {
			t.Fatalf("%s missing from the new snapshot: %+v", name, snap)
		}
	}
	if snap.Gen == "" {
		return
	}
	ch := decodeChanges(t, rawRow(t, store, tenant, conn, changesNode))
	if ch.Base != snap.Gen || ch.WatchDir != dir {
		t.Fatalf("changes row not on the new snapshot: base %q, snapshot gen %q (%+v)", ch.Base, snap.Gen, ch)
	}
}

// --- pods with a log --------------------------------------------------------

// newLoggedPod is an adoptPod whose consumer logs into a levelLog, before
// any load.
func newLoggedPod(t *testing.T, dir string, store checkpoint.Store, tenant, conn string) (*adoptPod, *levelLog) {
	t.Helper()
	clock := newFakeClock()
	probe := &recordingProbe{}
	logs := &levelLog{}
	c := &fileConsumer{
		logger:       slog.New(levelHandler{l: logs}),
		publish:      probe.publish,
		inlineMax:    1 << 20,
		pollInterval: watchInterval,
		now:          clock.now,
		checkpoints:  store,
	}
	ac := &ActiveConnection{ConnectionID: conn, TenantID: tenant, WatchDir: dir, NodeID: knownNode, Cancel: func() {}}
	return &adoptPod{t: t, c: c, ac: ac, clock: clock, probe: probe}, logs
}

// startLoggedPod starts a pod as a start does: loadKnown, then the start's
// save.
func startLoggedPod(t *testing.T, dir string, store checkpoint.Store, tenant, conn string) (*adoptPod, *levelLog) {
	t.Helper()
	p, logs := newLoggedPod(t, dir, store, tenant, conn)
	if err := p.c.loadKnown(context.Background(), p.ac, false); err != nil {
		t.Fatalf("loadKnown: %v", err)
	}
	p.c.saveKnown(context.Background(), p.ac, p.c.logger)
	return p, logs
}

func (p *adoptPod) flush() { p.c.flushKnown(context.Background(), p.ac, p.c.logger) }

// presentNames is what the folder of these tests holds at the start.
func presentNames(fillers int) []string {
	names := []string{xName}
	for i := 0; i < fillers; i++ {
		names = append(names, fillerName(i))
	}
	return names
}

// --- AC 1, 2 and 7: a start that meets an undecodable row --------------------

// runUndecodableStart: a start reads a saved set nobody can decode. It
// publishes nothing that was in the folder, replaces the rows at once, logs
// no Error and a single Warn, and a later start resumes from what it saved.
func runUndecodableStart(t *testing.T, store checkpoint.Store, tenant, conn string, tc undecodableCase) {
	dir := t.TempDir()
	writeFillers(t, dir, 4)
	put(t, dir, xName, xV0)
	tc.seed(t, store, tenant, conn, dir)

	a, logs := startLoggedPod(t, dir, store, tenant, conn)
	requireRowsReplaced(t, store, tenant, conn, dir, presentNames(4))
	a.polls(4)
	requireNothingPublished(t, "a start on an undecodable row", a)
	a.flush()
	if n := logs.count(slog.LevelError); n != 0 {
		t.Fatalf("%d Error records, want none: %s", n, logs)
	}
	if n := logs.count(slog.LevelWarn); n != 1 {
		t.Fatalf("%d Warn records, want exactly one: %s", n, logs)
	}

	// A file lands while stopped. The next start reads what A saved and
	// publishes that file, and only it.
	writeAt(t, dir, yName, yContent, adoptMTime.Add(3*time.Hour))
	next, nextLogs := startLoggedPod(t, dir, store, tenant, conn)
	next.polls(4)
	if got := len(next.probe.forName(yName)); got != 1 {
		t.Fatalf("y.csv, landed while stopped, published %d times by the next start, want 1 (%s)", got, describe(next.probe.published()))
	}
	if got := len(next.probe.published()); got != 1 {
		t.Fatalf("next start published %d envelopes, want 1 (%s)", got, describe(next.probe.published()))
	}
	if n := nextLogs.count(slog.LevelWarn) + nextLogs.count(slog.LevelError); n != 0 {
		t.Fatalf("the next start logged %d Warn/Error records, want none: the row decodes now (%s)", n, nextLogs)
	}
}

func TestCheckpointDecode_StartReplacesUndecodableRow(t *testing.T) {
	for _, tc := range undecodableCases() {
		t.Run(tc.name, func(t *testing.T) {
			runUndecodableStart(t, newLockedStore(), knownTenant, knownConn, tc)
		})
	}
}

// --- AC 3: a failed read at the start, then an undecodable row ---------------

func TestCheckpointDecode_GuessSettledOverUndecodableRow(t *testing.T) {
	cases := []struct {
		name string
		// publish: a file lands and is published while reads fail.
		publish bool
	}{
		{name: "no change while reads fail"},
		{name: "a publish while reads fail", publish: true},
	}
	for _, uc := range undecodableCases() {
		for _, tc := range cases {
			t.Run(uc.name+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				writeFillers(t, dir, 4)
				put(t, dir, xName, xV0)
				st := newAdoptStore(newLockedStore())
				uc.seed(t, st.Store, knownTenant, knownConn, dir)
				wantSnap, wantChanges := rowsNow(t, st, knownTenant, knownConn)

				failReads(st, true)
				a, logs := startLoggedPod(t, dir, st, knownTenant, knownConn)
				if tc.publish {
					writeAt(t, dir, "n.json", nJSON, adoptMTime.Add(5*time.Hour))
				}
				a.polls(3)
				if snap, changes := rowsNow(t, st, knownTenant, knownConn); string(snap) != string(wantSnap) || string(changes) != string(wantChanges) {
					t.Fatalf("a row was written while reads fail (store error):\nsnapshot %s\nchanges  %s", snap, changes)
				}

				failReads(st, false)
				logs.reset()
				a.polls(3)
				want := presentNames(4)
				if tc.publish {
					want = append(want, "n.json")
				}
				requireRowsReplaced(t, st.Store, knownTenant, knownConn, dir, want)
				if n := logs.count(slog.LevelError); n != 0 {
					t.Fatalf("%d Error records once reads work, want none: %s", n, logs)
				}
				for _, name := range presentNames(4) {
					if got := len(a.probe.forName(name)); got != 0 {
						t.Fatalf("%s, present at the start, published %d times (%s)", name, got, describe(a.probe.published()))
					}
				}
				wantPublished := 0
				if tc.publish {
					wantPublished = 1
				}
				if got := len(a.probe.published()); got != wantPublished {
					t.Fatalf("%d envelopes, want %d (%s)", got, wantPublished, describe(a.probe.published()))
				}

				// A later start publishes nothing again.
				next := startAdoptPod(t, dir, st, knownTenant, knownConn)
				next.polls(4)
				requireNothingPublished(t, "a start after the row was replaced", next)
			})
		}
	}
}

// --- AC 4 and 6: the deferred baseline (#409) --------------------------------

func TestCheckpointDecode_DeferredBaseline(t *testing.T) {
	cases := []struct {
		name string
		// storeError: reads fail; otherwise the row can't be decoded.
		storeError bool
	}{
		{name: "undecodable row: the baseline is taken"},
		{name: "store error: the baseline stays deferred", storeError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			put(t, dir, xName, xV0)
			st := newAdoptStore(newLockedStore())
			seedRaw(t, st.Store, knownTenant, knownConn, knownNode, `{"watch_dir": 5, "known": ["x.csv"]}`)
			wantSnap, wantChanges := rowsNow(t, st, knownTenant, knownConn)
			failReads(st, tc.storeError)

			// The start could not read the folder: the baseline is deferred.
			a, logs := newLoggedPod(t, dir, st, knownTenant, knownConn)
			a.ac.baselineDeferred = true
			a.ac.readFailures = 1
			a.polls(3)
			requireNothingPublished(t, "the polls that take the baseline", a)

			if tc.storeError {
				if !a.ac.baselineDeferred {
					t.Fatal("store error: the baseline was taken, want it deferred (#409)")
				}
				if snap, changes := rowsNow(t, st, knownTenant, knownConn); string(snap) != string(wantSnap) || string(changes) != string(wantChanges) {
					t.Fatalf("a row was written while reads fail:\nsnapshot %s\nchanges  %s", snap, changes)
				}
				return
			}
			if a.ac.baselineDeferred {
				t.Fatalf("undecodable row: the baseline is still deferred after 3 polls (%s)", logs)
			}
			requireRowsReplaced(t, st.Store, knownTenant, knownConn, dir, presentNames(4))
			if n := logs.count(slog.LevelError); n != 0 {
				t.Fatalf("%d Error records, want none: %s", n, logs)
			}

			// Files that land from here on are published once.
			writeAt(t, dir, "n.json", nJSON, adoptMTime.Add(5*time.Hour))
			a.polls(3)
			if got := len(a.probe.forName("n.json")); got != 1 {
				t.Fatalf("n.json published %d times, want 1 (%s)", got, describe(a.probe.published()))
			}
			if got := len(a.probe.published()); got != 1 {
				t.Fatalf("%d envelopes, want 1 (%s)", got, describe(a.probe.published()))
			}
		})
	}
}

// --- AC 5 and 6: keepSavedStamps (#500) --------------------------------------

func TestCheckpointDecode_KeepSavedStamps(t *testing.T) {
	cases := []struct {
		name       string
		storeError bool
	}{
		{name: "undecodable row: the snapshot is written"},
		{name: "store error: nothing is written", storeError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			put(t, dir, xName, xV0)
			st := newAdoptStore(newLockedStore())
			// x.csv is done with no stamp: its stat failed at the start.
			a := startAdoptPod(t, dir, st, knownTenant, knownConn, xName)
			if _, stamped := a.ac.knownStamps[xName]; stamped {
				t.Fatal("premise: x.csv has a stamp")
			}
			// The row turns undecodable while the watcher runs.
			seedRaw(t, st.Store, knownTenant, knownConn, knownNode, `["broken"]`)
			wantSnap, wantChanges := rowsNow(t, st, knownTenant, knownConn)
			failReads(st, tc.storeError)
			a.ac.dirty = true

			err := a.c.saveSnapshot(context.Background(), a.ac, a.c.logger)
			if tc.storeError {
				if err == nil {
					t.Fatal("store error: saveSnapshot returned nil, want the read's error")
				}
				if snap, changes := rowsNow(t, st, knownTenant, knownConn); string(snap) != string(wantSnap) || string(changes) != string(wantChanges) {
					t.Fatalf("a row was written while reads fail:\nsnapshot %s\nchanges  %s", snap, changes)
				}
				return
			}
			if err != nil {
				t.Fatalf("undecodable row: saveSnapshot: %v, want the snapshot written", err)
			}
			requireRowsReplaced(t, st.Store, knownTenant, knownConn, dir, presentNames(4))
		})
	}
}

// --- AC 7: the Warn is logged once, however often the row is met ------------

// garbageStore answers every read of the file node's two rows with a state
// nobody can decode, as a row that keeps being broken would be. Writes go
// through.
type garbageStore struct {
	checkpoint.Store
	mu    sync.Mutex
	reads int
}

func (s *garbageStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	if nodeID != knownNode && nodeID != changesNode {
		return s.Store.Get(ctx, tenantID, connectionID, nodeID)
	}
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return &checkpoint.Checkpoint{TenantID: tenantID, ConnectionID: connectionID, NodeID: nodeID, State: []byte(`[1, 2, 3]`)}, nil
}

func TestCheckpointDecode_WarnedOnce(t *testing.T) {
	dir := t.TempDir()
	writeFillers(t, dir, 4)
	put(t, dir, xName, xV0)
	st := &garbageStore{Store: newLockedStore()}

	a, logs := startLoggedPod(t, dir, st, knownTenant, knownConn)
	a.polls(2)
	writeAt(t, dir, "n.json", nJSON, adoptMTime.Add(5*time.Hour))
	a.polls(3)
	writeAt(t, dir, "m.json", nJSON, adoptMTime.Add(6*time.Hour))
	a.polls(3)
	a.flush()

	if got := len(a.probe.forName("n.json")); got != 1 {
		t.Fatalf("n.json published %d times, want 1 (%s)", got, describe(a.probe.published()))
	}
	if got := len(a.probe.forName("m.json")); got != 1 {
		t.Fatalf("m.json published %d times, want 1 (%s)", got, describe(a.probe.published()))
	}
	if got := len(a.probe.published()); got != 2 {
		t.Fatalf("%d envelopes, want 2 (%s)", got, describe(a.probe.published()))
	}
	st.mu.Lock()
	reads := st.reads
	st.mu.Unlock()
	if reads < 3 {
		t.Fatalf("premise: the broken rows were read %d times, want several", reads)
	}
	if n := logs.count(slog.LevelError); n != 0 {
		t.Fatalf("%d Error records, want none: %s", n, logs)
	}
	if n := logs.count(slog.LevelWarn); n != 1 {
		t.Fatalf("%d Warn records over %d reads of the broken rows, want exactly one: %s", n, reads, logs)
	}
}

// --- a row that breaks while the watcher runs --------------------------------

// The snapshot row turns undecodable while a watcher runs and still owns the
// changes row on it. A poll that reads the saved set (here: for a done name
// with no stamp, #500) meets it. The next save must write a full snapshot,
// not only changes on a snapshot nobody can read: otherwise a crash before
// the stop flush leaves the next start with nothing it can read, and a file
// that lands meanwhile is taken as done and never published.
func TestCheckpointDecode_BrokenMidRunReplacedBeforeACrash(t *testing.T) {
	dir := t.TempDir()
	writeFillers(t, dir, 4)
	put(t, dir, xName, xV0)
	st := newAdoptStore(newLockedStore())
	// x.csv is done with no stamp: its stat failed at the start.
	a := startAdoptPod(t, dir, st, knownTenant, knownConn, xName)
	if a.ac.savedGen == "" {
		t.Fatal("premise: the start wrote no snapshot")
	}
	seedRaw(t, st.Store, knownTenant, knownConn, knownNode, `{"watch_dir": 5}`)

	a.polls(2)
	if _, stamped := a.ac.knownStamps[xName]; !stamped {
		t.Fatal("premise: x.csv still has no stamp after two polls")
	}
	requireRowsReplaced(t, st.Store, knownTenant, knownConn, dir, presentNames(4))
	requireNothingPublished(t, "the polls that met the broken row", a)

	// A crashes (no stop flush); y.csv lands; the next start publishes it.
	writeAt(t, dir, yName, yContent, adoptMTime.Add(3*time.Hour))
	next := startAdoptPod(t, dir, st, knownTenant, knownConn)
	next.polls(4)
	if got := len(next.probe.forName(yName)); got != 1 {
		t.Fatalf("y.csv published %d times by the next start, want 1 (%s)", got, describe(next.probe.published()))
	}
	if got := len(next.probe.published()); got != 1 {
		t.Fatalf("next start published %d envelopes, want 1 (%s)", got, describe(next.probe.published()))
	}
}
