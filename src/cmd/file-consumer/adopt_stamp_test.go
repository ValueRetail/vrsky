package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Tests for #500: a done name with no stamp in a watcher's memory must not
// take the file's stamp as it is now when another watcher saved the name with
// a different one. That watcher published the saved version; the file has
// been replaced since, and the replacement is published once. See
// plans/issue-500-adopt-stamp.md, acceptance criteria 1 to 6 (7 is in
// adopt_stamp_pg_test.go).
//
// A watcher gets a done name with no stamp when its stat of the name fails as
// the set is loaded. The tests produce that with the readDir seam: the file
// is moved out of the folder for the load, and the seam lists it anyway, so
// the listing has the name and the stat finds nothing (the file vanished
// between the two, as on a share that blips). The file is moved back right
// after, with its content and mtime as they were. This behaves the same as
// root and on every OS, which chmod-ing the folder does not.
//
// Each pod is driven by hand on its own fake clock, as in known_test.go.

var adoptMTime = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// --- a name the stat can't find ---------------------------------------------

// phantomEntry is a directory entry for a file that is not there.
type phantomEntry string

func (p phantomEntry) Name() string             { return string(p) }
func (phantomEntry) IsDir() bool                { return false }
func (phantomEntry) Type() fs.FileMode          { return 0 }
func (phantomEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

// hideFromStat moves names out of dir. The returned readDir lists dir with
// those names in it anyway; restore moves them back.
func hideFromStat(t *testing.T, dir string, names ...string) (readDir func(string) ([]os.DirEntry, error), restore func()) {
	t.Helper()
	aside := t.TempDir()
	for _, n := range names {
		if err := os.Rename(filepath.Join(dir, n), filepath.Join(aside, n)); err != nil {
			t.Fatal(err)
		}
	}
	readDir = func(d string) ([]os.DirEntry, error) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			entries = append(entries, phantomEntry(n))
		}
		return entries, nil
	}
	restore = func() {
		for _, n := range names {
			if err := os.Rename(filepath.Join(aside, n), filepath.Join(dir, n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return readDir, restore
}

// --- a store that counts and can fail reads ---------------------------------

// adoptStore wraps any checkpoint store (the in-memory one or Postgres). It
// counts saves and reads per node id since the last take, and fails reads of
// the node ids in failing.
type adoptStore struct {
	checkpoint.Store
	mu      sync.Mutex
	saves   map[string]int
	gets    map[string]int
	failing map[string]bool
}

func newAdoptStore(inner checkpoint.Store) *adoptStore {
	return &adoptStore{Store: inner, saves: map[string]int{}, gets: map[string]int{}, failing: map[string]bool{}}
}

var errAdoptRead = errors.New("injected: checkpoint read failed")

func (s *adoptStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	s.mu.Lock()
	s.gets[nodeID]++
	fail := s.failing[nodeID]
	s.mu.Unlock()
	if fail {
		return nil, errAdoptRead
	}
	return s.Store.Get(ctx, tenantID, connectionID, nodeID)
}

func (s *adoptStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.mu.Lock()
	s.saves[cp.NodeID]++
	s.mu.Unlock()
	return s.Store.Save(ctx, cp)
}

// take returns the saves and reads per node id since the last take.
func (s *adoptStore) take() (saves, gets map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	saves, gets = s.saves, s.gets
	s.saves, s.gets = map[string]int{}, map[string]int{}
	return saves, gets
}

func (s *adoptStore) setFailing(nodeID string, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing[nodeID] = v
}

// --- pods -------------------------------------------------------------------

// adoptPod is one consumer process with its own watcher for the shared
// connection, folder and checkpoint store.
type adoptPod struct {
	t     *testing.T
	c     *fileConsumer
	ac    *ActiveConnection
	clock *fakeClock
	probe *recordingProbe
}

// startAdoptPod loads the done set as a start does (loadKnown, then the
// start's save) with the stat of each name in statFails failing, and checks
// that those names came out done with no stamp. store may be nil: no
// checkpoint is kept.
func startAdoptPod(t *testing.T, dir string, store checkpoint.Store, tenant, conn string, statFails ...string) *adoptPod {
	t.Helper()
	clock := newFakeClock()
	probe := &recordingProbe{}
	c := &fileConsumer{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:      probe.publish,
		inlineMax:    1 << 20,
		pollInterval: watchInterval,
		now:          clock.now,
		checkpoints:  store,
	}
	ac := &ActiveConnection{ConnectionID: conn, TenantID: tenant, WatchDir: dir, NodeID: knownNode, Cancel: func() {}}
	var restore func()
	if len(statFails) > 0 {
		c.readDir, restore = hideFromStat(t, dir, statFails...)
	}
	if err := c.loadKnown(context.Background(), ac, false); err != nil {
		t.Fatalf("loadKnown: %v", err)
	}
	c.saveKnown(context.Background(), ac, c.logger)
	if restore != nil {
		restore()
		c.readDir = nil
	}
	for _, n := range statFails {
		if _, stamped := ac.knownStamps[n]; !ac.knownFiles[n] || stamped {
			t.Fatalf("premise: %s should be done with no stamp after the load (done=%v, stamped=%v)", n, ac.knownFiles[n], stamped)
		}
	}
	return &adoptPod{t: t, c: c, ac: ac, clock: clock, probe: probe}
}

func (p *adoptPod) poll() {
	p.clock.advance(watchInterval)
	p.c.pollOnce(context.Background(), p.ac)
}

func (p *adoptPod) polls(n int) {
	for i := 0; i < n; i++ {
		p.poll()
	}
}

// --- files and what was published -------------------------------------------

// version is one content of x.csv, with its mtime. Sizes differ too, so no
// two versions share a stamp by accident.
type version struct {
	content []byte
	mtime   time.Time
}

var (
	xV0 = version{[]byte("id\nv0\n"), adoptMTime}
	xV1 = version{[]byte("id\nv1 published by B\n"), adoptMTime.Add(time.Hour)}
	xV2 = version{[]byte("id\nv2, the replacement nobody published\n"), adoptMTime.Add(2 * time.Hour)}
)

const xName = "x.csv"

func put(t *testing.T, dir, name string, v version) {
	t.Helper()
	writeAt(t, dir, name, v.content, v.mtime)
}

func stampNow(t *testing.T, dir, name string) fileStamp {
	t.Helper()
	st, ok := statStamp(dir, name)
	if !ok {
		t.Fatalf("stat %s failed", name)
	}
	return st
}

// fillerName is a file that sits in the folder untouched.
func fillerName(i int) string { return "filler-" + string(rune('a'+i)) + ".csv" }

func writeFillers(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		writeAt(t, dir, fillerName(i), []byte("id\n"+fillerName(i)+"\n"), adoptMTime.Add(-time.Hour))
	}
}

// published counts the envelopes for name with content, across pods.
func published(name string, content []byte, pods ...*adoptPod) int {
	n := 0
	for _, p := range pods {
		for _, env := range p.probe.forName(name) {
			if bytes.Equal(env.Payload, content) {
				n++
			}
		}
	}
	return n
}

func publishedAll(pods ...*adoptPod) string {
	var b strings.Builder
	for i, p := range pods {
		b.WriteString("pod ")
		b.WriteByte(byte('A' + i))
		b.WriteString(" ")
		b.WriteString(describe(p.probe.published()))
		b.WriteString("; ")
	}
	return b.String()
}

func requireNothingPublished(t *testing.T, step string, pods ...*adoptPod) {
	t.Helper()
	for _, p := range pods {
		if got := p.probe.published(); len(got) != 0 {
			t.Fatalf("%s: published %d envelopes, want 0 (%s)", step, len(got), publishedAll(pods...))
		}
	}
}

// --- AC 1 and 2: two pods, one with no stamp for x.csv ----------------------

// twoPodCase is one run of the scenario in the plan: A loads with its stat of
// x.csv failing; B publishes x.csv at stamp S and saves S; x.csv is replaced;
// the pods poll in order.
type twoPodCase struct {
	name string
	// fillers is how many untouched files sit in the folder. None: B's
	// save of v1 is a snapshot (one change is more than half of one name).
	// Four: it is a change on B's own snapshot, in the changes row (#426).
	fillers int
	// wantRow is where B's save of v1 must land: knownNode or changesNode.
	wantRow string
	// replacement is what x.csv is replaced with after B published v1.
	replacement version
	// order is which pod polls, in turn, after the replacement.
	order string
}

func twoPodCases() []twoPodCase {
	var cases []twoPodCase
	for _, row := range []struct {
		label   string
		fillers int
		node    string
	}{
		{"B saved in the snapshot", 0, knownNode},
		{"B saved in the changes row", 4, changesNode},
	} {
		for _, order := range []string{"AABB", "ABAB", "BABA", "BBAA"} {
			cases = append(cases, twoPodCase{
				name:    row.label + ", polls " + order,
				fillers: row.fillers, wantRow: row.node, replacement: xV2, order: order,
			})
		}
	}
	// The snapshot still has v0's stamp under B's changes row. A replacement
	// with that stamp (the original put back with its mtime kept, as cp -p
	// does) is new after v1 too, and only the changes row says so.
	for _, order := range []string{"AABB", "ABAB"} {
		cases = append(cases, twoPodCase{
			name:    "B saved in the changes row, v0 put back, polls " + order,
			fillers: 4, wantRow: changesNode, replacement: xV0, order: order,
		})
	}
	return cases
}

// runTwoPods runs tc on store for the connection conn of tenant.
func runTwoPods(t *testing.T, store checkpoint.Store, tenant, conn string, tc twoPodCase) {
	t.Helper()
	dir := t.TempDir()
	st := newAdoptStore(store)
	writeFillers(t, dir, tc.fillers)
	put(t, dir, xName, xV0)

	// A: first start for the folder, its stat of x.csv fails. Its snapshot
	// has x.csv as a name with no stamp.
	a := startAdoptPod(t, dir, st, tenant, conn, xName)
	// B starts after A, with the stat working: it takes v0's stamp for the
	// name and saves its own snapshot.
	b := startAdoptPod(t, dir, st, tenant, conn)
	s0 := stampNow(t, dir, xName)
	if got := b.ac.knownStamps[xName]; got != s0 {
		t.Fatalf("premise: B's stamp for x.csv = %+v, want v0's %+v", got, s0)
	}

	// B publishes v1 and saves its stamp S.
	put(t, dir, xName, xV1)
	s1 := stampNow(t, dir, xName)
	b.poll()
	st.take()
	b.poll()
	saves, _ := st.take()
	if got := published(xName, xV1.content, b); got != 1 {
		t.Fatalf("premise: B published v1 %d times, want 1 (%s)", got, publishedAll(a, b))
	}
	snap := decodeSnapshot(t, rawRow(t, st, tenant, conn, knownNode))
	ch := decodeChanges(t, rawRow(t, st, tenant, conn, changesNode))
	v1Saved := wireStamp{Size: s1.size, MTime: s1.mtimeSec, MTimeNsec: s1.mtimeNsec}
	switch tc.wantRow {
	case knownNode:
		if saves[knownNode] != 1 || snap.Files[xName] != v1Saved {
			t.Fatalf("premise: B's save of v1 should be a snapshot with its stamp: saves %v, snapshot %+v", saves, snap)
		}
	case changesNode:
		if saves[knownNode] != 0 || saves[changesNode] != 1 || ch.Base != snap.Gen || ch.Files[xName] != v1Saved {
			t.Fatalf("premise: B's save of v1 should be in the changes row only: saves %v, snapshot %+v, changes %+v", saves, snap, ch)
		}
		v0Saved := wireStamp{Size: s0.size, MTime: s0.mtimeSec, MTimeNsec: s0.mtimeNsec}
		if snap.Files[xName] != v0Saved {
			t.Fatalf("premise: the snapshot under B's changes row should hold v0's stamp: %+v", snap)
		}
	}
	requireNothingPublished(t, "before the replacement", a)

	// x.csv is replaced, then the pods poll.
	put(t, dir, xName, tc.replacement)
	if stampNow(t, dir, xName) == s1 {
		t.Fatal("premise: the replacement has v1's stamp")
	}
	pods := map[byte]*adoptPod{'A': a, 'B': b}
	for i := 0; i < len(tc.order); i++ {
		pods[tc.order[i]].poll()
	}
	for i := 0; i < 3; i++ {
		a.poll()
		b.poll()
	}

	if got := published(xName, tc.replacement.content, a, b); got != 1 {
		t.Fatalf("the replacement was published %d times by the two pods, want 1 (%s)", got, publishedAll(a, b))
	}
	if got := published(xName, xV1.content, a, b); got != 1 {
		t.Fatalf("v1 was published %d times in all, want 1 (by B, before the replacement) (%s)", got, publishedAll(a, b))
	}
	if got := len(a.probe.forName(xName)) + len(b.probe.forName(xName)); got != 2 {
		t.Fatalf("x.csv was published %d times in all, want 2 (v1, then the replacement) (%s)", got, publishedAll(a, b))
	}
	if got := len(a.probe.published()) + len(b.probe.published()); got != 2 {
		t.Fatalf("%d envelopes in all, want 2: the fillers are never published (%s)", got, publishedAll(a, b))
	}
}

func TestAdoptStamp_TwoPodsPublishTheReplacementOnce(t *testing.T) {
	for _, tc := range twoPodCases() {
		t.Run(tc.name, func(t *testing.T) {
			runTwoPods(t, newLockedStore(), knownTenant, knownConn, tc)
		})
	}
}

// --- AC 3: nothing changes when the saved row agrees or has no stamp --------

func TestAdoptStamp_StamplessNameAdoptedWithoutPublishing(t *testing.T) {
	// older is a stamp x.csv never had.
	older := savedStamp{Size: 1, MTime: adoptMTime.Add(-48 * time.Hour).Unix()}
	cases := []struct {
		name string
		// noStore runs the pod with no checkpoint store at all.
		noStore bool
		// saved replaces what is stored after A's start; nil keeps A's own
		// snapshot (x.csv as a name with no stamp).
		saved func(t *testing.T, st checkpoint.Store, dir string, now fileStamp)
	}{
		{
			name: "A's own snapshot, name with no stamp",
		},
		{
			name: "saved by another pod with the same stamp",
			saved: func(t *testing.T, st checkpoint.Store, dir string, _ fileStamp) {
				startAdoptPod(t, dir, st, knownTenant, knownConn) // takes the stamp, saves it
			},
		},
		{
			name: "saved by a pod from before #420 (names only)",
			saved: func(t *testing.T, st checkpoint.Store, dir string, _ fileStamp) {
				clearRows(t, st)
				seedRow(t, st, knownNode, map[string]any{"watch_dir": dir, "known": []string{xName, fillerName(0), fillerName(1)}})
			},
		},
		{
			name: "no checkpoint",
			saved: func(t *testing.T, st checkpoint.Store, _ string, _ fileStamp) {
				clearRows(t, st)
			},
		},
		{
			name:    "no checkpoint store",
			noStore: true,
		},
		{
			name: "same stamp in the changes row, an older one in the snapshot",
			saved: func(t *testing.T, st checkpoint.Store, dir string, now fileStamp) {
				clearRows(t, st)
				seedRow(t, st, knownNode, knownState{WatchDir: dir, Gen: "g-seed", Known: []string{xName},
					Files: map[string]savedStamp{xName: older}})
				seedRow(t, st, changesNode, changesState{WatchDir: dir, Base: "g-seed",
					Files: map[string]savedStamp{xName: toSaved(now)}})
			},
		},
		{
			name: "no stamp in the changes row over a stamped snapshot",
			saved: func(t *testing.T, st checkpoint.Store, dir string, _ fileStamp) {
				clearRows(t, st)
				seedRow(t, st, knownNode, knownState{WatchDir: dir, Gen: "g-seed", Known: []string{xName},
					Files: map[string]savedStamp{xName: older}})
				seedRow(t, st, changesNode, changesState{WatchDir: dir, Base: "g-seed", Known: []string{xName}})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 2)
			put(t, dir, xName, xV1)
			var st checkpoint.Store
			if !tc.noStore {
				st = newLockedStore()
			}
			a := startAdoptPod(t, dir, st, knownTenant, knownConn, xName)
			now := stampNow(t, dir, xName)
			if tc.saved != nil {
				tc.saved(t, st, dir, now)
			}

			a.poll()
			if got, ok := a.ac.knownStamps[xName]; !ok || got != now {
				t.Fatalf("after the first poll: stamp of x.csv = %+v (stamped %v), want the file's %+v", got, ok, now)
			}
			a.polls(4)
			requireNothingPublished(t, "after five polls", a)

			// The adopted stamp is the file's: a real replacement now is
			// published once.
			put(t, dir, xName, xV2)
			a.polls(4)
			if got := published(xName, xV2.content, a); got != 1 || len(a.probe.published()) != 1 {
				t.Fatalf("a replacement after the adoption: published %d times, want once and nothing else (%s)",
					got, describe(a.probe.published()))
			}
		})
	}
}

// clearRows deletes the connection's checkpoint rows.
func clearRows(t *testing.T, st checkpoint.Store) {
	t.Helper()
	if err := st.DeleteForConnection(context.Background(), knownTenant, knownConn); err != nil {
		t.Fatal(err)
	}
}

// --- AC 4: a failed read publishes and adopts nothing that poll -------------

func TestAdoptStamp_FailedReadWaitsForTheNextPoll(t *testing.T) {
	cases := []struct {
		name string
		// failRow is the row whose read fails.
		failRow string
		// replaced: x.csv was replaced after B published it, so A must
		// publish it once reads work. Otherwise A adopts and publishes
		// nothing.
		replaced bool
	}{
		{name: "snapshot read fails, file replaced", failRow: knownNode, replaced: true},
		{name: "changes row read fails, file replaced", failRow: changesNode, replaced: true},
		{name: "snapshot read fails, file as B published it", failRow: knownNode},
		{name: "changes row read fails, file as B published it", failRow: changesNode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			put(t, dir, xName, xV0)
			st := newAdoptStore(newLockedStore())
			a := startAdoptPod(t, dir, st, knownTenant, knownConn, xName)
			b := startAdoptPod(t, dir, st, knownTenant, knownConn)
			put(t, dir, xName, xV1)
			b.polls(2)
			if got := published(xName, xV1.content, b); got != 1 {
				t.Fatalf("premise: B published v1 %d times, want 1", got)
			}
			want := xV1
			if tc.replaced {
				put(t, dir, xName, xV2)
				want = xV2
			}

			st.setFailing(tc.failRow, true)
			for i := 0; i < 3; i++ {
				a.poll()
				if _, stamped := a.ac.knownStamps[xName]; stamped || !a.ac.knownFiles[xName] {
					t.Fatalf("failed read, poll %d: x.csv should stay done with no stamp (done=%v, stamped=%v)",
						i+1, a.ac.knownFiles[xName], stamped)
				}
				if _, pending := a.ac.pending[xName]; pending {
					t.Fatalf("failed read, poll %d: x.csv is pending; nothing should happen to it until a read works", i+1)
				}
			}
			requireNothingPublished(t, "while reads fail", a)

			st.setFailing(tc.failRow, false)
			a.polls(3)
			if got := a.ac.knownStamps[xName]; got != stampNow(t, dir, xName) {
				t.Fatalf("after reads work: stamp of x.csv = %+v, want the file's", got)
			}
			b.polls(3)
			a.polls(2)
			if tc.replaced {
				if got := published(xName, xV2.content, a, b); got != 1 {
					t.Fatalf("the replacement was published %d times, want 1 (%s)", got, publishedAll(a, b))
				}
				if got := published(xName, xV2.content, a); got != 1 {
					t.Fatalf("A published the replacement %d times, want 1: B never polled before A did (%s)", got, publishedAll(a, b))
				}
			}
			if got := len(a.probe.published()) + len(b.probe.published()); got != 1+boolInt(tc.replaced) {
				t.Fatalf("%d envelopes in all, want %d (%s)", got, 1+boolInt(tc.replaced), publishedAll(a, b))
			}
			if got := published(xName, want.content, a, b); got != 1 {
				t.Fatalf("the current x.csv was published %d times, want 1 (%s)", got, publishedAll(a, b))
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- AC 5: the snapshot is read once per poll, and only when needed ---------

func TestAdoptStamp_SavedSetReadOncePerPoll(t *testing.T) {
	cases := []struct {
		name      string
		stampless []string
		wantReads int
	}{
		{name: "no stampless names", wantReads: 0},
		{name: "one stampless name", stampless: []string{"s0.csv"}, wantReads: 1},
		{name: "three stampless names", stampless: []string{"s0.csv", "s1.csv", "s2.csv"}, wantReads: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 10)
			for i, n := range tc.stampless {
				writeAt(t, dir, n, []byte("id\n"+n+"\n"), adoptMTime.Add(time.Duration(i)*time.Minute))
			}
			st := newAdoptStore(newLockedStore())
			a := startAdoptPod(t, dir, st, knownTenant, knownConn, tc.stampless...)
			// A new file arrives too, so the poll has work besides the
			// stampless names.
			writeAt(t, dir, "new.json", []byte(`{"new":1}`), adoptMTime.Add(time.Hour))
			st.take()

			a.poll()
			_, gets := st.take()
			if gets[knownNode] != tc.wantReads {
				t.Fatalf("first poll read the snapshot %d times, want %d (reads %v)", gets[knownNode], tc.wantReads, gets)
			}
			for _, n := range tc.stampless {
				if _, stamped := a.ac.knownStamps[n]; !stamped {
					t.Fatalf("first poll: %s still has no stamp", n)
				}
			}

			// Nothing has no stamp any more: the next polls read no
			// snapshot, the one that publishes new.json included (A owns
			// the saved set, so its re-read is the changes row).
			for i := 0; i < 2; i++ {
				a.poll()
				if _, gets := st.take(); gets[knownNode] != 0 {
					t.Fatalf("poll %d after the adoption read the snapshot %d times, want 0 (reads %v)", i+2, gets[knownNode], gets)
				}
			}
			if got := a.probe.published(); len(got) != 1 || len(a.probe.forName("new.json")) != 1 {
				t.Fatalf("published %s, want only new.json", describe(got))
			}
		})
	}
}

// --- AC 6: a snapshot keeps the saved stamp of a name with none -------------

// pollsHidden polls p n times with names listed in dir but not stat-able.
func (p *adoptPod) pollsHidden(n int, dir string, names ...string) {
	p.t.Helper()
	readDir, restore := hideFromStat(p.t, dir, names...)
	p.c.readDir = readDir
	p.polls(n)
	p.c.readDir = nil
	restore()
}

var nJSON = []byte(`{"n":1}`)

// B publishes v1, saves its stamp and stops; x.csv is replaced; A, with no
// stamp for x.csv, publishes another file while its reads of the saved set
// fail. Saving that publish as a snapshot written blind dropped B's stamp,
// and once reads worked A took the replacement as done. No snapshot may be
// written while the read fails; the one there stays as it was.
func TestAdoptStamp_FailedReadWritesNoSnapshot(t *testing.T) {
	for _, row := range []string{knownNode, changesNode} {
		t.Run("read of "+row+" fails", func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			put(t, dir, xName, xV0)
			st := newAdoptStore(newLockedStore())
			a := startAdoptPod(t, dir, st, knownTenant, knownConn, xName)
			b := startAdoptPod(t, dir, st, knownTenant, knownConn)
			put(t, dir, xName, xV1)
			b.polls(2)
			if got := published(xName, xV1.content, b); got != 1 {
				t.Fatalf("premise: B published v1 %d times, want 1", got)
			}
			b.c.flushKnown(context.Background(), b.ac, b.c.logger) // B stops
			put(t, dir, xName, xV2)
			writeAt(t, dir, "n.json", nJSON, adoptMTime.Add(5*time.Hour))

			before := rawRow(t, st.Store, knownTenant, knownConn, knownNode)
			st.take()
			st.setFailing(row, true)
			a.polls(3)
			saves, _ := st.take()
			if got := len(a.probe.forName("n.json")); got != 1 {
				t.Fatalf("premise: A published n.json %d times while reads failed, want 1 (%s)", got, describe(a.probe.published()))
			}
			if saves[knownNode] != 0 || saves[changesNode] != 0 {
				t.Fatalf("checkpoint rows written while the read failed: %v, want none", saves)
			}
			if got := rawRow(t, st.Store, knownTenant, knownConn, knownNode); !bytes.Equal(got, before) {
				t.Fatalf("snapshot row changed while the read failed:\n got %s\nwant %s", got, before)
			}

			st.setFailing(row, false)
			a.polls(4)
			if got := published(xName, xV2.content, a, b); got != 1 {
				t.Fatalf("the replacement was published %d times, want 1 (%s)", got, publishedAll(a, b))
			}
			if got := len(a.probe.forName("n.json")); got != 1 {
				t.Fatalf("n.json published %d times in all, want 1 (%s)", got, publishedAll(a, b))
			}
			if got := len(a.probe.published()) + len(b.probe.published()); got != 3 {
				t.Fatalf("%d envelopes in all, want 3: v1 by B, n.json and the replacement by A (%s)", got, publishedAll(a, b))
			}
			names := decodeSnapshot(t, rawRow(t, st.Store, knownTenant, knownConn, knownNode))
			if !containsName(names.Known, "n.json") {
				t.Fatalf("n.json missing from the snapshot once reads work: %+v", names)
			}
		})
	}
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// A snapshot A writes while it still can't stat x.csv must keep the stamp
// another watcher saved for it. Without it the snapshot drops that stamp,
// and the first poll that can stat x.csv takes a replacement as done. A
// name saved with no stamp stays without one.
func TestAdoptStamp_SnapshotKeepsTheSavedStamp(t *testing.T) {
	cases := []struct {
		name string
		// setup leaves A with x.csv done and no stamp, the saved set as
		// described, and returns A. B (nil when there is none) has stopped.
		setup func(t *testing.T, dir string, st *adoptStore) (a, b *adoptPod)
		// savedStamp: the saved set has B's stamp for x.csv (v1's) and
		// A's snapshot must keep it. Otherwise it has none and the
		// snapshot has x.csv as a name with no stamp.
		savedStamp bool
	}{
		{
			name: "A's first save, after a start whose checkpoint read failed",
			setup: func(t *testing.T, dir string, st *adoptStore) (*adoptPod, *adoptPod) {
				b := startAdoptPod(t, dir, st, knownTenant, knownConn)
				put(t, dir, xName, xV1)
				b.polls(2)
				b.c.flushKnown(context.Background(), b.ac, b.c.logger)
				// The start's read fails: every name in the folder is done
				// for this run, stamped by a stat, and nothing is saved.
				st.setFailing(knownNode, true)
				a := startAdoptPod(t, dir, st, knownTenant, knownConn, xName)
				st.setFailing(knownNode, false)
				if a.ac.savedGen != "" {
					t.Fatal("premise: A should have no snapshot of its own after the start")
				}
				return a, b
			},
			savedStamp: true,
		},
		{
			name: "a later snapshot, after another watcher saved",
			setup: func(t *testing.T, dir string, st *adoptStore) (*adoptPod, *adoptPod) {
				a := startAdoptPod(t, dir, st, knownTenant, knownConn, xName)
				b := startAdoptPod(t, dir, st, knownTenant, knownConn)
				put(t, dir, xName, xV1)
				b.polls(2)
				b.c.flushKnown(context.Background(), b.ac, b.c.logger)
				return a, b
			},
			savedStamp: true,
		},
		{
			name: "saved with no stamp (a pod from before #420)",
			setup: func(t *testing.T, dir string, st *adoptStore) (*adoptPod, *adoptPod) {
				a := startAdoptPod(t, dir, st, knownTenant, knownConn, xName)
				clearRows(t, st)
				known := []string{xName}
				for i := 0; i < 4; i++ {
					known = append(known, fillerName(i))
				}
				seedRow(t, st, knownNode, map[string]any{"watch_dir": dir, "known": known})
				return a, nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFillers(t, dir, 4)
			put(t, dir, xName, xV0)
			st := newAdoptStore(newLockedStore())
			a, b := tc.setup(t, dir, st)
			pods := []*adoptPod{a}
			if b != nil {
				pods = append(pods, b)
			}
			saved := stampNow(t, dir, xName)

			// A publishes n.json while x.csv can't be stat'ed; that save is
			// a snapshot (no snapshot of its own, or another one's is newer).
			writeAt(t, dir, "n.json", nJSON, adoptMTime.Add(5*time.Hour))
			st.take()
			a.pollsHidden(2, dir, xName)
			saves, _ := st.take()
			if got := len(a.probe.forName("n.json")); got != 1 {
				t.Fatalf("premise: A published n.json %d times, want 1 (%s)", got, describe(a.probe.published()))
			}
			if saves[knownNode] == 0 {
				t.Fatalf("premise: A's save of n.json should be a snapshot: saves %v", saves)
			}
			snap := decodeSnapshot(t, rawRow(t, st.Store, knownTenant, knownConn, knownNode))
			if !containsName(snap.Known, xName) {
				t.Fatalf("x.csv missing from A's snapshot: %+v", snap)
			}
			got, stamped := snap.Files[xName]
			if tc.savedStamp {
				want := wireStamp{Size: saved.size, MTime: saved.mtimeSec, MTimeNsec: saved.mtimeNsec}
				if !stamped || got != want {
					t.Fatalf("A's snapshot has x.csv at %+v (stamped %v), want the saved %+v", got, stamped, want)
				}
			} else if stamped {
				t.Fatalf("A's snapshot has a stamp %+v for x.csv; the saved set had none", got)
			}

			if !tc.savedStamp {
				// No stamp anywhere: the first poll that can stat x.csv
				// takes it as it is, publishing nothing.
				a.polls(3)
				if got := len(a.probe.forName(xName)); got != 0 {
					t.Fatalf("x.csv with no saved stamp published %d times, want 0 (%s)", got, describe(a.probe.published()))
				}
			}
			// x.csv is replaced and can be stat'ed again.
			put(t, dir, xName, xV2)
			a.polls(4)
			if got := published(xName, xV2.content, pods...); got != 1 {
				t.Fatalf("the replacement was published %d times, want 1 (%s)", got, publishedAll(pods...))
			}
			wantAll := 2 + boolInt(b != nil) // n.json, the replacement, v1 by B
			total := 0
			for _, p := range pods {
				total += len(p.probe.published())
			}
			if got := total; got != wantAll {
				t.Fatalf("%d envelopes in all, want %d (%s)", got, wantAll, publishedAll(pods...))
			}
		})
	}
}
