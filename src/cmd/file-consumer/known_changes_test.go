package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Tests for #426: the done set is saved as a snapshot row plus a small
// changes row, so a busy poll writes and reads only what changed. See
// plans/issue-426-known-save.md, acceptance criteria 1 to 7. The storage
// format is taken from the plan: the snapshot row (node id of the file node)
// has watch_dir, gen, known and files; the changes row (node id
// "<node>#changes") has watch_dir, base, files, known and gone.

const changesNode = knownNode + "#changes"

var changesMTime = time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)

// --- a store that counts traffic per row -------------------------------------

// rowTraffic is checkpoint traffic per node id since the last take.
type rowTraffic struct {
	saves, gets   map[string]int
	written, read map[string]int
}

func newRowTraffic() rowTraffic {
	return rowTraffic{
		saves: map[string]int{}, gets: map[string]int{},
		written: map[string]int{}, read: map[string]int{},
	}
}

func (r rowTraffic) bytes(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func (r rowTraffic) String() string {
	return fmt.Sprintf("saves=%v gets=%v written=%v read=%v", r.saves, r.gets, r.written, r.read)
}

// rowCountingStore wraps a lockedStore and counts saves, reads and bytes per
// node id.
type rowCountingStore struct {
	inner *lockedStore
	mu    sync.Mutex
	t     rowTraffic
}

func newRowCountingStore(inner *lockedStore) *rowCountingStore {
	return &rowCountingStore{inner: inner, t: newRowTraffic()}
}

func (s *rowCountingStore) take() rowTraffic {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.t
	s.t = newRowTraffic()
	return out
}

func (s *rowCountingStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.mu.Lock()
	s.t.saves[cp.NodeID]++
	s.t.written[cp.NodeID] += len(cp.State)
	s.mu.Unlock()
	return s.inner.Save(ctx, cp)
}

func (s *rowCountingStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	cp, err := s.inner.Get(ctx, tenantID, connectionID, nodeID)
	s.mu.Lock()
	s.t.gets[nodeID]++
	if cp != nil {
		s.t.read[nodeID] += len(cp.State)
	}
	s.mu.Unlock()
	return cp, err
}

func (s *rowCountingStore) Delete(ctx context.Context, tenantID, connectionID, nodeID string) error {
	return s.inner.Delete(ctx, tenantID, connectionID, nodeID)
}

func (s *rowCountingStore) DeleteForConnection(ctx context.Context, tenantID, connectionID string) error {
	return s.inner.DeleteForConnection(ctx, tenantID, connectionID)
}

// --- stored rows, decoded independently of the implementation ---------------

type wireStamp struct {
	Size      int64 `json:"size"`
	MTime     int64 `json:"mtime"`
	MTimeNsec int64 `json:"mtime_nsec"`
}

type wireSnapshot struct {
	WatchDir string               `json:"watch_dir"`
	Gen      string               `json:"gen"`
	Known    []string             `json:"known"`
	Files    map[string]wireStamp `json:"files"`
}

type wireChanges struct {
	WatchDir string               `json:"watch_dir"`
	Base     string               `json:"base"`
	Files    map[string]wireStamp `json:"files"`
	Known    []string             `json:"known"`
	Gone     []string             `json:"gone"`
}

func (c wireChanges) size() int { return len(c.Files) + len(c.Known) + len(c.Gone) }

// rawRow returns the state of one checkpoint row, nil if there is none.
func rawRow(t *testing.T, store checkpoint.Store, tenant, conn, node string) []byte {
	t.Helper()
	cp, err := store.Get(context.Background(), tenant, conn, node)
	if err != nil {
		t.Fatalf("read checkpoint %s: %v", node, err)
	}
	if cp == nil {
		return nil
	}
	return cp.State
}

func decodeSnapshot(t *testing.T, raw []byte) wireSnapshot {
	t.Helper()
	var st wireSnapshot
	if raw == nil {
		t.Fatal("no snapshot row")
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode snapshot row: %v (%s)", err, raw)
	}
	return st
}

func decodeChanges(t *testing.T, raw []byte) wireChanges {
	t.Helper()
	var ch wireChanges
	if raw == nil {
		t.Fatal("no changes row")
	}
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatalf("decode changes row: %v (%s)", err, raw)
	}
	return ch
}

// --- the previous image (before #426) ----------------------------------------

// prevImageState is the checkpoint struct of the image before #426: no gen,
// and it never reads the changes row.
type prevImageState struct {
	WatchDir string   `json:"watch_dir"`
	Known    []string `json:"known"`
	Files    map[string]struct {
		Size      int64 `json:"size"`
		MTime     int64 `json:"mtime"`
		MTimeNsec int64 `json:"mtime_nsec"`
	} `json:"files,omitempty"`
}

// prevImageLoad mimics the previous image's loadSaved: the snapshot row
// alone, names from known and files, stamps from files.
func prevImageLoad(t *testing.T, raw []byte, dir string) (map[string]bool, map[string]fileStamp) {
	t.Helper()
	if raw == nil {
		t.Fatal("previous image: no checkpoint row")
	}
	var st prevImageState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("previous image: decode: %v", err)
	}
	if st.WatchDir != dir {
		t.Fatalf("previous image: row for %q, want %q", st.WatchDir, dir)
	}
	names := map[string]bool{}
	stamps := map[string]fileStamp{}
	for _, n := range st.Known {
		names[n] = true
	}
	for n, f := range st.Files {
		names[n] = true
		stamps[n] = fileStamp{size: f.Size, mtimeSec: f.MTime, mtimeNsec: f.MTimeNsec}
	}
	return names, stamps
}

// prevImageWouldPublish runs the previous image's start rule on the folder: a
// name it does not know, or one whose saved stamp differs from the file's,
// goes through the poll and is published.
func prevImageWouldPublish(t *testing.T, dir string, names map[string]bool, stamps map[string]fileStamp) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !names[n] {
			out = append(out, n)
			continue
		}
		saved, ok := stamps[n]
		if !ok {
			continue // adopted at start, never published
		}
		if now, ok := statStamp(dir, n); ok && now != saved {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// --- watchers ---------------------------------------------------------------

// newChangesWatcher is newKnownWatcher on any checkpoint store.
func newChangesWatcher(t *testing.T, dir string, store checkpoint.Store, publish sdk.PublishFunc) *knownWatcher {
	t.Helper()
	clock := newFakeClock()
	c := &fileConsumer{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:      publish,
		inlineMax:    1 << 20,
		pollInterval: watchInterval,
		now:          clock.now,
		checkpoints:  store,
	}
	ac := &ActiveConnection{ConnectionID: knownConn, TenantID: knownTenant, WatchDir: dir, NodeID: knownNode, Cancel: func() {}}
	if err := c.loadKnown(context.Background(), ac, false); err != nil {
		t.Fatalf("loadKnown: %v", err)
	}
	c.saveKnown(context.Background(), ac, c.logger)
	return &knownWatcher{c: c, ac: ac, clock: clock}
}

func baseName(i int) string { return fmt.Sprintf("base-%05d.csv", i) }

// writeBaseline fills dir with n files of fixed content and mtime.
func writeBaseline(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		writeAt(t, dir, baseName(i), []byte("id\n"+fmt.Sprint(i)+"\n"), changesMTime)
	}
}

func requireNone(t *testing.T, step string, p *recordingProbe) {
	t.Helper()
	if got := p.published(); len(got) != 0 {
		t.Fatalf("%s: published %d envelopes, want 0 (%s)", step, len(got), describe(got))
	}
}

func requireTimes(t *testing.T, step string, p *recordingProbe, name string, want int) {
	t.Helper()
	if got := len(p.forName(name)); got != want {
		t.Fatalf("%s: %s published %d times, want %d (%s)", step, name, got, want, describe(p.published()))
	}
}

// --- AC 1 and 2: a busy poll writes and reads only the changes row -----------

func TestKnownChanges_BusyPollTouchesOnlyTheChangesRow(t *testing.T) {
	const n = 3000
	dir := t.TempDir()
	writeBaseline(t, dir, n)
	store := newRowCountingStore(newLockedStore())
	probe := &recordingProbe{}
	ctx := context.Background()

	w := newChangesWatcher(t, dir, store, probe.publish) // first start
	store.take()
	snapSize := len(rawRow(t, store.inner, knownTenant, knownConn, knownNode))
	if snapSize < 100*n/2 {
		t.Fatalf("snapshot after the first start is %d bytes for %d names; the test needs a large set", snapSize, n)
	}
	limit := snapSize / 100 // "orders of magnitude": under 1% of the snapshot

	busy := func(step string, got rowTraffic) {
		t.Helper()
		if got.saves[knownNode] != 0 || got.gets[knownNode] != 0 {
			t.Fatalf("%s: touched the snapshot row: %s", step, got)
		}
		if got.saves[changesNode] != 1 {
			t.Fatalf("%s: %d saves of the changes row, want 1: %s", step, got.saves[changesNode], got)
		}
		if w := got.bytes(got.written); w > limit {
			t.Fatalf("%s: wrote %d bytes, want under %d (snapshot is %d): %s", step, w, limit, snapSize, got)
		}
		if r := got.bytes(got.read); r > limit {
			t.Fatalf("%s: read %d bytes, want under %d (snapshot is %d): %s", step, r, limit, snapSize, got)
		}
	}

	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("new-%02d.json", i)
		writeAt(t, dir, name, []byte(`{"n":1}`), changesMTime.Add(time.Duration(i+1)*time.Minute))
		w.poll(ctx) // first sighting: nothing changed, nothing saved or read
		if got := store.take(); got.bytes(got.saves) != 0 || got.bytes(got.gets) != 0 {
			t.Fatalf("sighting poll %d touched the store: %s", i, got)
		}
		w.poll(ctx) // stable: published
		requireTimes(t, "busy poll", probe, name, 1)
		got := store.take()
		busy(fmt.Sprintf("busy poll %d", i), got)
		// AC 2: the re-read before the publish is the changes row only, and
		// so is the check before the save that the set is still this
		// watcher's (found in review).
		if got.gets[changesNode] != 2 {
			t.Fatalf("busy poll %d: %d reads of the changes row, want 2: %s", i, got.gets[changesNode], got)
		}
	}

	// A poll that only sees a deletion is busy too.
	if err := os.Remove(filepath.Join(dir, baseName(7))); err != nil {
		t.Fatal(err)
	}
	w.poll(ctx)
	busy("deletion poll", store.take())

	ch := decodeChanges(t, rawRow(t, store.inner, knownTenant, knownConn, changesNode))
	snap := decodeSnapshot(t, rawRow(t, store.inner, knownTenant, knownConn, knownNode))
	if ch.Base != snap.Gen || snap.Gen == "" {
		t.Fatalf("changes row on base %q, snapshot gen %q", ch.Base, snap.Gen)
	}
	if len(ch.Files) != 5 || len(ch.Gone) != 1 || ch.Gone[0] != baseName(7) {
		t.Fatalf("changes row = %+v, want the 5 new files and %s gone", ch, baseName(7))
	}

	// A new start on the same store loads snapshot plus changes: nothing
	// published again, and it is back on the cheap path after its first save.
	probe2 := &recordingProbe{}
	w2 := newChangesWatcher(t, dir, store, probe2.publish)
	for i := 0; i < 3; i++ {
		w2.poll(ctx)
	}
	requireNone(t, "restart", probe2)
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("after-%02d.json", i)
		writeAt(t, dir, name, []byte(`{"after":1}`), changesMTime.Add(time.Hour+time.Duration(i)*time.Minute))
		w2.poll(ctx)
		store.take()
		w2.poll(ctx)
		requireTimes(t, "after the restart", probe2, name, 1)
		got := store.take()
		if i == 0 {
			continue // the first save after a start is a snapshot
		}
		busy("second busy poll after the restart", got)
		if got.gets[changesNode] != 2 {
			t.Fatalf("second busy poll after the restart: %s", got)
		}
	}
}

// --- AC 7: past the limit a save is a snapshot, and changes start empty ------

func TestKnownChanges_CompactsPastTheLimit(t *testing.T) {
	cases := []struct {
		name    string
		set     int   // files in the folder; replacements keep the set this size
		batches []int // names replaced per busy poll, up to the limit exactly
	}{
		{"large set, limit 512", 2000, []int{256, 256}},
		{"half the set, 1000 names", 1000, []int{250, 250}},
		{"small set, half of it", 20, []int{5, 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeBaseline(t, dir, tc.set)
			store := newRowCountingStore(newLockedStore())
			probe := &recordingProbe{}
			ctx := context.Background()
			w := newChangesWatcher(t, dir, store, probe.publish)
			gen0 := decodeSnapshot(t, rawRow(t, store.inner, knownTenant, knownConn, knownNode)).Gen

			next := 0
			replace := func(k int) {
				t.Helper()
				for j := 0; j < k; j++ {
					writeAt(t, dir, baseName(next), []byte("replaced\n"+fmt.Sprint(next)+"\n"),
						changesMTime.Add(time.Duration(next+1)*time.Second))
					next++
				}
				w.poll(ctx)
				w.poll(ctx)
				if got := len(probe.published()); got != next {
					t.Fatalf("after replacing %d names: %d published, want %d", next, got, next)
				}
			}

			for i, k := range tc.batches {
				store.take()
				replace(k)
				got := store.take()
				if got.saves[knownNode] != 0 || got.saves[changesNode] != 1 {
					t.Fatalf("batch %d (%d changed so far): want only the changes row written: %s", i, next, got)
				}
			}
			ch := decodeChanges(t, rawRow(t, store.inner, knownTenant, knownConn, changesNode))
			if ch.Base != gen0 || ch.size() != next {
				t.Fatalf("at the limit: changes row on %q with %d names, want %q with %d", ch.Base, ch.size(), gen0, next)
			}

			// One more: past the limit.
			store.take()
			replace(1)
			got := store.take()
			if got.saves[knownNode] != 1 || got.saves[changesNode] != 1 {
				t.Fatalf("past the limit: want one snapshot and one changes reset: %s", got)
			}
			snap := decodeSnapshot(t, rawRow(t, store.inner, knownTenant, knownConn, knownNode))
			if snap.Gen == "" || snap.Gen == gen0 {
				t.Fatalf("snapshot past the limit has gen %q (was %q)", snap.Gen, gen0)
			}
			if len(snap.Known) != tc.set || len(snap.Files) != tc.set {
				t.Fatalf("snapshot holds %d names, %d stamps, want %d", len(snap.Known), len(snap.Files), tc.set)
			}
			ch = decodeChanges(t, rawRow(t, store.inner, knownTenant, knownConn, changesNode))
			if ch.Base != snap.Gen || ch.size() != 0 {
				t.Fatalf("changes row after the snapshot = %+v, want empty on %q", ch, snap.Gen)
			}

			// The next busy poll writes changes on the new snapshot again.
			store.take()
			replace(1)
			got = store.take()
			if got.saves[knownNode] != 0 || got.saves[changesNode] != 1 {
				t.Fatalf("after the snapshot: want only the changes row written: %s", got)
			}
			ch = decodeChanges(t, rawRow(t, store.inner, knownTenant, knownConn, changesNode))
			if ch.Base != snap.Gen || ch.size() != 1 {
				t.Fatalf("changes row = %+v, want one name on %q", ch, snap.Gen)
			}

			// And a new start publishes nothing again.
			probe2 := &recordingProbe{}
			w2 := newChangesWatcher(t, dir, store, probe2.publish)
			for i := 0; i < 3; i++ {
				w2.poll(ctx)
			}
			requireNone(t, "restart after the snapshot", probe2)
		})
	}
}

// --- AC 3: a crash keeps what the last save had ------------------------------

func TestKnownChanges_RestartFindsSnapshotPlusChanges(t *testing.T) {
	eachShutdownMode(t, func(t *testing.T, mode shutdownMode) {
		store := newLockedStore()
		r := newRestartRig(t, t.TempDir(), "conn-changes", "tenant-r", store)
		const n = 40
		writeBaseline(t, r.dir(), n)
		r.boot()
		r.polls(2)
		r.requireTotal("first start", 0)

		publish := func(name string, content []byte, mtime time.Time) {
			t.Helper()
			writeAt(t, r.dir(), name, content, mtime)
			r.polls(2)
		}
		n1, n2 := []byte(`{"n":1}`), []byte(`{"n":2}`)
		publish("n1.json", n1, changesMTime.Add(time.Minute))
		publish("n2.json", n2, changesMTime.Add(2*time.Minute))
		v2 := []byte("replaced\n")
		publish(baseName(3), v2, changesMTime.Add(3*time.Minute))
		r.requireTotal("before the stop", 3)

		// base-5 is deleted (a gone name in the changes row) and comes back
		// while stopped, identical to the baseline file: a name that comes
		// back is new again.
		r.remove(baseName(5))
		r.poll()
		// u arrives and is sighted once, not published.
		u := []byte(`{"u":"unpublished at the stop"}`)
		writeAt(t, r.dir(), "u.json", u, changesMTime.Add(4*time.Minute))
		r.poll()
		r.requireTotal("u sighted", 3)

		// The premise: names saved only in the changes row.
		snap := decodeSnapshot(t, rawRow(t, store, r.tenant, r.connID, "c1"))
		ch := decodeChanges(t, rawRow(t, store, r.tenant, r.connID, "c1"+changesSuffix))
		if ch.Base != snap.Gen || ch.Files["n2.json"] == (wireStamp{}) || len(ch.Gone) != 1 {
			t.Fatalf("before the stop: snapshot gen %q, changes %+v; want n2 and one gone name in a changes row on it", snap.Gen, ch)
		}

		r.shutdown(mode)
		writeAt(t, r.dir(), baseName(5), []byte("id\n5\n"), changesMTime)
		r.boot()
		r.polls(3)

		r.requireOnce("after the restart", "u.json", u)
		r.requireOnce("after the restart", "n1.json", n1)
		r.requireOnce("after the restart", "n2.json", n2)
		r.requireOnce("after the restart", baseName(3), v2)
		r.requireOnce("after the restart", baseName(5), []byte("id\n5\n"))
		r.requireTotal("after the restart", 5)

		// Once more, now from what the second lifetime saved.
		r.restart(mode)
		r.polls(3)
		r.requireTotal("after a second restart", 5)
	})
}

// --- AC 4: a stop leaves a snapshot the previous image can use --------------

func TestKnownChanges_StopLeavesAFullSnapshotForThePreviousImage(t *testing.T) {
	for _, mode := range []shutdownMode{viaStopCommand, viaPodShutdown} {
		t.Run(string(mode), func(t *testing.T) {
			store := newLockedStore()
			r := newRestartRig(t, t.TempDir(), "conn-rollback", "tenant-r", store)
			writeBaseline(t, r.dir(), 30)
			r.boot()
			r.polls(2)
			for i := 0; i < 3; i++ {
				writeAt(t, r.dir(), fmt.Sprintf("n%d.json", i), []byte(`{"n":1}`), changesMTime.Add(time.Duration(i+1)*time.Minute))
				r.polls(2)
			}
			writeAt(t, r.dir(), baseName(2), []byte("replaced\n"), changesMTime.Add(time.Hour))
			r.polls(2)
			r.remove(baseName(4))
			r.poll()
			r.requireTotal("before the stop", 4)

			ch := decodeChanges(t, rawRow(t, store, r.tenant, r.connID, "c1"+changesSuffix))
			if ch.size() == 0 || ch.Files["n2.json"] == (wireStamp{}) {
				t.Fatalf("premise: n2.json should be saved only in the changes row before the stop: %+v", ch)
			}
			ac := r.c.getActiveConnection(r.connID)
			live := make(map[string]bool, len(ac.knownFiles))
			liveStamps := make(map[string]fileStamp, len(ac.knownStamps))
			for k := range ac.knownFiles {
				live[k] = true
			}
			for k, v := range ac.knownStamps {
				liveStamps[k] = v
			}

			r.shutdown(mode)

			names, stamps := prevImageLoad(t, rawRow(t, store, r.tenant, r.connID, "c1"), r.dir())
			assertSameSet(t, "previous image's set after the stop", names, live)
			for k, v := range liveStamps {
				if stamps[k] != v {
					t.Fatalf("previous image: stamp of %s = %+v, want %+v", k, stamps[k], v)
				}
			}
			if got := prevImageWouldPublish(t, r.dir(), names, stamps); len(got) != 0 {
				t.Fatalf("previous image would publish again: %v", got)
			}
		})
	}
}

func assertSameSet(t *testing.T, step string, got, want map[string]bool) {
	t.Helper()
	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	if len(missing)+len(extra) > 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		t.Fatalf("%s: missing %v, extra %v", step, missing, extra)
	}
}

// --- AC 5: a changes row on another snapshot is ignored ---------------------

func TestKnownChanges_StaleChangesRowIsIgnored(t *testing.T) {
	cases := []struct {
		name         string
		snapGen      string // "" is a snapshot the previous image wrote
		changesBase  string
		changesValid bool
	}{
		{"previous image's snapshot, changes on an old gen", "", "gen-1", false},
		{"previous image's snapshot, changes with no base", "", "", false},
		{"changes on an older snapshot", "gen-2", "gen-1", false},
		{"control: changes on this snapshot", "gen-1", "gen-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := newLockedStore()
			// keep and dropped are in the snapshot; fresh arrived while
			// stopped. The changes row, if it were applied, says dropped is
			// no longer done and fresh is.
			for _, n := range []string{"keep.json", "dropped.json", "fresh.json"} {
				writeAt(t, dir, n, []byte(`{"name":"`+n+`"}`), changesMTime)
			}
			st := func(n string) wireStamp {
				s, ok := statStamp(dir, n)
				if !ok {
					t.Fatalf("stat %s", n)
				}
				return wireStamp{Size: s.size, MTime: s.mtimeSec, MTimeNsec: s.mtimeNsec}
			}
			snap := map[string]any{
				"watch_dir": dir,
				"known":     []string{"dropped.json", "keep.json"},
				"files":     map[string]wireStamp{"keep.json": st("keep.json"), "dropped.json": st("dropped.json")},
			}
			if tc.snapGen != "" {
				snap["gen"] = tc.snapGen
			}
			changes := map[string]any{
				"watch_dir": dir,
				"base":      tc.changesBase,
				"files":     map[string]wireStamp{"fresh.json": st("fresh.json")},
				"gone":      []string{"dropped.json"},
			}
			seedRow(t, store, knownNode, snap)
			seedRow(t, store, changesNode, changes)

			probe := &recordingProbe{}
			ctx := context.Background()
			w := newChangesWatcher(t, dir, store, probe.publish)
			for i := 0; i < 4; i++ {
				w.poll(ctx)
			}
			requireTimes(t, "after the start", probe, "keep.json", 0)
			if tc.changesValid {
				requireTimes(t, "after the start", probe, "fresh.json", 0)
				requireTimes(t, "after the start", probe, "dropped.json", 1)
			} else {
				requireTimes(t, "after the start", probe, "fresh.json", 1)
				requireTimes(t, "after the start", probe, "dropped.json", 0)
			}

			// What this start saved holds for the next one.
			w2 := newChangesWatcher(t, dir, store, probe.publish)
			for i := 0; i < 4; i++ {
				w2.poll(ctx)
			}
			if got := len(probe.published()); got != 1 {
				t.Fatalf("after a second start: %d published in all, want 1 (%s)", got, describe(probe.published()))
			}
		})
	}
}

func seedRow(t *testing.T, store checkpoint.Store, node string, state any) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: knownTenant, ConnectionID: knownConn, NodeID: node, State: raw,
	}); err != nil {
		t.Fatal(err)
	}
}

// --- AC 6: two watchers with changes rows in play ---------------------------

func TestKnownChanges_TwoWatchersSkipWhatTheOtherPublished(t *testing.T) {
	dir := t.TempDir()
	store := newRowCountingStore(newLockedStore())
	probeA, probeB := &recordingProbe{}, &recordingProbe{}
	ctx := context.Background()
	writeBaseline(t, dir, 4)
	a := newChangesWatcher(t, dir, store, probeA.publish) // first start
	b := newChangesWatcher(t, dir, store, probeB.publish) // loads A's set
	file := func(name string, minute int) {
		writeAt(t, dir, name, []byte(`{"name":"`+name+`"}`), changesMTime.Add(time.Duration(minute)*time.Minute))
	}

	// B publishes b0, so it has a snapshot of its own and the cheap re-read
	// is open to it.
	file("b0.json", 1)
	b.poll(ctx)
	b.poll(ctx)
	requireTimes(t, "B", probeB, "b0.json", 1)

	// A publishes a1, a2, a3 in three busy polls; the last two are saved as
	// changes rows.
	file("a1.json", 2)
	a.poll(ctx)
	file("a2.json", 3)
	a.poll(ctx) // b0 (skipped: B published it) and a1
	file("a3.json", 4)
	store.take()
	a.poll(ctx) // a2
	if got := store.take(); got.saves[knownNode] != 0 || got.saves[changesNode] != 1 {
		t.Fatalf("premise: A's a2 save should be a changes row: %s", got)
	}
	a.poll(ctx) // a3
	if got := store.take(); got.saves[knownNode] != 0 || got.saves[changesNode] != 1 {
		t.Fatalf("premise: A's a3 save should be a changes row: %s", got)
	}
	for _, n := range []string{"a1.json", "a2.json", "a3.json"} {
		requireTimes(t, "A", probeA, n, 1)
	}
	requireTimes(t, "A", probeA, "b0.json", 0)

	// B sees a1..a3 settle after A's changes rows: it must skip them, and to
	// know it has to read the snapshot too, since the changes row is on A's.
	b.poll(ctx)
	store.take()
	b.poll(ctx)
	if got := store.take(); got.gets[knownNode] == 0 {
		t.Fatalf("B's re-read after A saved did not read the snapshot: %s", got)
	}
	if got := probeB.published(); len(got) != 1 {
		t.Fatalf("B published %d envelopes, want only b0 (%s)", len(got), describe(got))
	}

	// A keeps publishing, one file per busy poll, until a save passes the
	// limit and writes a snapshot. B does not poll meanwhile.
	changesSaves, compacted := 0, false
	var names []string
	for i := 0; i < 40 && !compacted; i++ {
		name := fmt.Sprintf("c%02d.json", i)
		names = append(names, name)
		file(name, 10+i)
		a.poll(ctx)
		store.take()
		a.poll(ctx)
		requireTimes(t, "A", probeA, name, 1)
		got := store.take()
		switch {
		case got.saves[knownNode] == 1:
			if i > 0 {
				compacted = true
			}
		case got.saves[changesNode] == 1:
			changesSaves++
		default:
			t.Fatalf("A's busy poll %d saved nothing: %s", i, got)
		}
	}
	if !compacted || changesSaves < 2 {
		t.Fatalf("A never compacted after several changes rows (compacted=%v, changes saves=%d)", compacted, changesSaves)
	}
	// One more after the snapshot, as a change on it.
	names = append(names, "e1.json")
	file("e1.json", 60)
	a.poll(ctx)
	a.poll(ctx)
	requireTimes(t, "A", probeA, "e1.json", 1)

	b.poll(ctx)
	b.poll(ctx)
	if got := probeB.published(); len(got) != 1 {
		t.Fatalf("B published %d envelopes after A's snapshot, want only b0 (%s)", len(got), describe(got))
	}

	// And the other way round: A skips what B publishes now.
	file("f1.json", 70)
	a.poll(ctx) // sighted by A
	b.poll(ctx)
	b.poll(ctx)
	requireTimes(t, "B", probeB, "f1.json", 1)
	a.poll(ctx)
	a.poll(ctx)
	requireTimes(t, "A", probeA, "f1.json", 0)

	// Every file once across both watchers.
	all := append([]string{"b0.json", "a1.json", "a2.json", "a3.json", "f1.json"}, names...)
	for _, n := range all {
		if got := len(probeA.forName(n)) + len(probeB.forName(n)); got != 1 {
			t.Fatalf("%s published %d times by the two watchers, want 1", n, got)
		}
	}
}

// --- a crash between the snapshot and its changes reset ----------------------

// crashingStore passes saves through until it is armed; the first save after
// that lands and then the pod "dies": the store is frozen as it was right
// after that one save, and later saves go nowhere.
type crashingStore struct {
	*lockedStore
	mu     sync.Mutex
	armed  bool
	frozen *lockedStore
}

func (s *crashingStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen != nil {
		return nil
	}
	if err := s.lockedStore.Save(ctx, cp); err != nil {
		return err
	}
	if s.armed {
		s.frozen = s.snapshot()
	}
	return nil
}

// A snapshot is written as two rows, the snapshot first. A crash right after
// the first of the two must leave a set that loads whole: names saved before
// only as changes are still done.
func TestKnownChanges_CrashBetweenSnapshotAndChangesReset(t *testing.T) {
	dir := t.TempDir()
	store := &crashingStore{lockedStore: newLockedStore()}
	probe := &recordingProbe{}
	ctx := context.Background()
	writeBaseline(t, dir, 20)
	w := newChangesWatcher(t, dir, store, probe.publish)
	for i := 0; i < 3; i++ {
		writeAt(t, dir, fmt.Sprintf("n%d.json", i), []byte(`{"n":1}`), changesMTime.Add(time.Duration(i+1)*time.Minute))
		w.poll(ctx)
		w.poll(ctx)
	}
	if got := len(probe.published()); got != 3 {
		t.Fatalf("before the crash: %d published, want 3", got)
	}
	ch := decodeChanges(t, rawRow(t, store, knownTenant, knownConn, changesNode))
	if ch.size() != 3 {
		t.Fatalf("premise: the three files should be saved as changes: %+v", ch)
	}

	// The stop writes a snapshot; the pod dies after its first row.
	store.mu.Lock()
	store.armed = true
	store.mu.Unlock()
	w.c.flushKnown(ctx, w.ac, w.c.logger)
	store.mu.Lock()
	frozen := store.frozen
	store.mu.Unlock()
	if frozen == nil {
		t.Fatal("premise: the stop wrote nothing")
	}

	next := newChangesWatcher(t, dir, frozen, probe.publish)
	for i := 0; i < 4; i++ {
		next.poll(ctx)
	}
	if got := len(probe.published()); got != 3 {
		t.Fatalf("after the crash: %d published in all, want 3 (%s)", got, describe(probe.published()))
	}
}

// A watcher that stops writes a snapshot of its own set (for the previous
// image after a rollback). Written blind, that snapshot dropped a file
// another watcher had published and saved since, and the next start
// published it again. Found by the test author; flushKnown now merges first.
func TestKnownChanges_StopFlushKeepsWhatAnotherWatcherSaved(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	pa, pb, pc := &recordingProbe{}, &recordingProbe{}, &recordingProbe{}
	ctx := context.Background()
	writeBaseline(t, dir, 4)
	a := newChangesWatcher(t, dir, store, pa.publish)
	writeAt(t, dir, "a1.json", []byte(`{"a":1}`), changesMTime.Add(time.Minute))
	a.poll(ctx)
	a.poll(ctx)
	requireTimes(t, "a", pa, "a1.json", 1)
	b := newChangesWatcher(t, dir, store, pb.publish)
	writeAt(t, dir, "x.json", []byte(`{"x":1}`), changesMTime.Add(2*time.Minute))
	b.poll(ctx)
	b.poll(ctx)
	requireTimes(t, "b", pb, "x.json", 1)

	a.c.flushKnown(ctx, a.ac, a.c.logger) // A stops after B saved

	c := newChangesWatcher(t, dir, store, pc.publish)
	c.poll(ctx)
	c.poll(ctx)
	requireNone(t, "next start after A's stop", pc)
}

// Found in review. N has its own snapshot; O, an older watcher of the same
// connection, then writes one (its stop). N's next save has no publish in it,
// so nothing re-read the saved set first. Written as changes on N's own
// snapshot, which every reader now ignores, those saves were lost on a crash,
// and the changes row made N's later re-reads think nobody else had saved.
func TestKnownChanges_ChangesNeverWrittenOnAReplacedSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	pn, po, pc := &recordingProbe{}, &recordingProbe{}, &recordingProbe{}
	ctx := context.Background()
	file := func(name string, minute int) {
		t.Helper()
		writeAt(t, dir, name, []byte(`{"name":"`+name+`"}`), changesMTime.Add(time.Duration(minute)*time.Minute))
	}
	writeBaseline(t, dir, 6)
	o := newChangesWatcher(t, dir, store, po.publish)
	n := newChangesWatcher(t, dir, store, pn.publish)
	file("n0.json", 1)
	n.poll(ctx)
	n.poll(ctx)
	requireTimes(t, "N", pn, "n0.json", 1)

	// O publishes o1 and stops: a snapshot of its own over N's.
	file("o1.json", 2)
	o.poll(ctx)
	o.poll(ctx)
	requireTimes(t, "O", po, "o1.json", 1)
	o.c.flushKnown(ctx, o.ac, o.c.logger)

	// N: a deletion only, then a publish.
	if err := os.Remove(filepath.Join(dir, baseName(0))); err != nil {
		t.Fatal(err)
	}
	n.poll(ctx)
	file("y.json", 3)
	n.poll(ctx)
	n.poll(ctx)
	requireTimes(t, "N", pn, "y.json", 1)
	requireTimes(t, "N", pn, "o1.json", 0)

	// Crash: a new start on the store as it is.
	c := newChangesWatcher(t, dir, store.snapshot(), pc.publish)
	for i := 0; i < 3; i++ {
		c.poll(ctx)
	}
	requireNone(t, "start after the crash", pc)
}

// Found in review. B saves only a deletion right after A replaced the
// snapshot. B must take in what A saved before it writes, or its poll that
// follows publishes a1 again (the race the single row had too).
func TestKnownChanges_SaveMergesAnotherWatchersSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	pa, pb := &recordingProbe{}, &recordingProbe{}
	ctx := context.Background()
	writeBaseline(t, dir, 4)
	a := newChangesWatcher(t, dir, store, pa.publish)
	b := newChangesWatcher(t, dir, store, pb.publish)
	file := func(name string, minute int) {
		t.Helper()
		writeAt(t, dir, name, []byte(`{"name":"`+name+`"}`), changesMTime.Add(time.Duration(minute)*time.Minute))
	}
	file("b0.json", 1)
	b.poll(ctx)
	b.poll(ctx)
	requireTimes(t, "B", pb, "b0.json", 1)
	file("a1.json", 2)
	a.poll(ctx)
	a.poll(ctx)
	requireTimes(t, "A", pa, "a1.json", 1)
	if err := os.Remove(filepath.Join(dir, baseName(0))); err != nil {
		t.Fatal(err)
	}
	b.poll(ctx) // sights a1, saves the deletion
	b.poll(ctx)
	requireTimes(t, "B", pb, "a1.json", 0)
}

// Found in review. A start that read part of the set from a changes row (the
// last watcher crashed) saves a snapshot even when nothing else changed, so a
// clean stop after it leaves the whole set where the previous image reads it.
func TestKnownChanges_CrashThenQuietRunLeavesAFullSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := newLockedStore()
	probe := &recordingProbe{}
	ctx := context.Background()
	writeBaseline(t, dir, 6)
	w := newChangesWatcher(t, dir, store, probe.publish)
	writeAt(t, dir, "n1.json", []byte(`{"n":1}`), changesMTime.Add(time.Minute))
	w.poll(ctx)
	w.poll(ctx)
	requireTimes(t, "first run", probe, "n1.json", 1)
	if ch := decodeChanges(t, rawRow(t, store, knownTenant, knownConn, knownNode+changesSuffix)); ch.size() == 0 {
		t.Fatal("premise: n1.json is not in the changes row")
	}

	crashed := store.snapshot()
	next := newChangesWatcher(t, dir, crashed, probe.publish)
	next.poll(ctx)
	next.poll(ctx)
	next.c.flushKnown(ctx, next.ac, next.c.logger) // a clean stop

	names, stamps := prevImageLoad(t, rawRow(t, crashed, knownTenant, knownConn, knownNode), dir)
	if got := prevImageWouldPublish(t, dir, names, stamps); len(got) != 0 {
		t.Fatalf("previous image after the rollback would publish %v, want nothing", got)
	}
	requireTimes(t, "after the restart", probe, "n1.json", 1)
}
