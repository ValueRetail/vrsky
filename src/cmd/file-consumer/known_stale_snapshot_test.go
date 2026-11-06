package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// #723: a watcher must not keep writing changes on a snapshot nobody reads.
// ownsSaved looks at the changes row, which can say "mine" after another
// watcher wrote a snapshot: its changes reset landed between this watcher's
// read and write and was overwritten, or its reset failed. After
// staleBoundSaves changes-only saves, or staleBoundTime, the snapshot's Gen is
// compared too. The numbers are written out here so changing the constants
// means changing the tests.
const (
	staleBoundSaves = 8
	staleBoundTime  = 5 * time.Minute
)

// hookStore wraps a Store: afterGet runs after each Get, and saves of
// failSaveNode fail while it is set.
type hookStore struct {
	checkpoint.Store
	mu           sync.Mutex
	afterGet     func(nodeID string)
	failSaveNode string
}

func (h *hookStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	cp, err := h.Store.Get(ctx, tenantID, connectionID, nodeID)
	h.mu.Lock()
	hook := h.afterGet
	h.mu.Unlock()
	if hook != nil {
		hook(nodeID)
	}
	return cp, err
}

func (h *hookStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	h.mu.Lock()
	fail := h.failSaveNode != "" && cp.NodeID == h.failSaveNode
	h.mu.Unlock()
	if fail {
		return errors.New("injected save failure")
	}
	return h.Store.Save(ctx, cp)
}

func (h *hookStore) setHook(f func(nodeID string)) {
	h.mu.Lock()
	h.afterGet = f
	h.mu.Unlock()
}

func (h *hookStore) setFail(node string) {
	h.mu.Lock()
	h.failSaveNode = node
	h.mu.Unlock()
}

type staleRig struct {
	t      *testing.T
	dir    string
	store  *lockedStore
	hookA  *hookStore
	hookO  *hookStore
	probeA *recordingProbe
	probeO *recordingProbe
	a, o   *knownWatcher

	armedMu sync.Mutex
	armed   bool
}

func newStaleRig(t *testing.T) *staleRig {
	t.Helper()
	r := &staleRig{t: t, dir: t.TempDir(), store: newLockedStore(), probeA: &recordingProbe{}, probeO: &recordingProbe{}}
	r.hookA = &hookStore{Store: r.store}
	r.hookO = &hookStore{Store: r.store}
	writeBaseline(t, r.dir, 100)
	publishA := func(ctx context.Context, env *envelope.Envelope) error {
		r.armedMu.Lock()
		r.armed = true
		r.armedMu.Unlock()
		return r.probeA.publish(ctx, env)
	}
	r.a = newChangesWatcher(t, r.dir, r.hookA, publishA)
	r.o = newChangesWatcher(t, r.dir, r.hookO, r.probeO.publish)
	return r
}

func (r *staleRig) file(name string, minute int) {
	r.t.Helper()
	writeAt(r.t, r.dir, name, []byte(`{"name":"`+name+`"}`), changesMTime.Add(time.Duration(minute)*time.Minute))
}

// publishA makes A publish name and reports whether A wrote a snapshot of its
// own while doing so (its savedGen changed).
func (r *staleRig) publishA(name string, minute int) (snapshot bool) {
	r.t.Helper()
	ctx := context.Background()
	before := r.a.ac.savedGen
	r.file(name, minute)
	r.a.poll(ctx)
	r.a.poll(ctx)
	requireTimes(r.t, "A", r.probeA, name, 1)
	return r.a.ac.savedGen != before
}

// hookOnNextSave arms fn to run once, right after A's first read of the
// changes row that follows one of A's publishes: A's check in saveKnown, just
// before it writes.
func (r *staleRig) hookOnNextSave(fn func()) {
	r.armedMu.Lock()
	r.armed = false
	r.armedMu.Unlock()
	fired := false
	r.hookA.setHook(func(node string) {
		if node != knownNode+changesSuffix || fired {
			return
		}
		r.armedMu.Lock()
		ok := r.armed
		r.armedMu.Unlock()
		if !ok {
			return
		}
		fired = true
		fn()
	})
}

// oPublishesAndFlushes is another watcher publishing a file and writing its
// snapshot, as it does at its stop.
func (r *staleRig) oPublishesAndFlushes() {
	ctx := context.Background()
	r.file("o1.json", 50)
	r.o.poll(ctx)
	r.o.poll(ctx)
	r.o.c.flushKnown(ctx, r.o.ac, r.o.c.logger)
}

// recoversAndRestartIsQuiet runs saves until A writes a snapshot, within the
// bound, then starts a fresh watcher on the rows and checks that it
// publishes nothing again.
func (r *staleRig) recoversAndRestartIsQuiet(published []string, saves int) {
	t := r.t
	t.Helper()
	// Counting from A's first changes-only save (a1): the poll after the
	// staleBoundSaves-th makes the check.
	recovered := false
	for i := 0; saves+i <= staleBoundSaves && !recovered; i++ {
		name := fmt.Sprintf("a%d.json", saves+i)
		published = append(published, name)
		recovered = r.publishA(name, 10+saves+i)
	}
	if !recovered {
		t.Fatalf("A kept writing changes on a snapshot nobody reads through its %dth changes-only save", staleBoundSaves)
	}
	// While A was stale it did not see O's o1.json as published and sent it
	// again; that window is what the bound limits. After the recovery the saved
	// set is whole.
	pc := &recordingProbe{}
	c := newChangesWatcher(t, r.dir, r.store.snapshot(), pc.publish)
	for i := 0; i < 3; i++ {
		c.poll(context.Background())
	}
	requireNone(t, "start after the recovery", pc)
	for _, n := range append(published, "o1.json") {
		if !c.ac.knownFiles[n] {
			t.Errorf("a restart does not know %s", n)
		}
	}
}

// O's snapshot and changes reset land between A's read of the changes row and
// its write. A's write overwrites O's reset.
func TestKnownChanges_StaleChangesAfterAReplacedSnapshotAreRecovered(t *testing.T) {
	r := newStaleRig(t)
	r.publishA("a0.json", 1)
	r.publishA("a1.json", 2) // A owns the save now: changes-only from here
	r.hookOnNextSave(r.oPublishesAndFlushes)
	// The save that goes stale is this one.
	if r.publishA("a2.json", 3) {
		t.Fatal("premise: A's save wrote a snapshot instead of changes")
	}
	snap := decodeSnapshot(t, rawRow(t, r.store, knownTenant, knownConn, knownNode))
	if ch := decodeChanges(t, rawRow(t, r.store, knownTenant, knownConn, knownNode+changesSuffix)); ch.Base == snap.Gen {
		t.Fatal("premise: A's write did not land on a snapshot nobody reads")
	}
	r.recoversAndRestartIsQuiet([]string{"a0.json", "a1.json", "a2.json"}, 3)
}

// O's snapshot lands but its changes reset fails, so the changes row is still
// on A's snapshot.
func TestKnownChanges_FailedChangesResetOfAnotherSnapshotIsRecovered(t *testing.T) {
	r := newStaleRig(t)
	r.publishA("a0.json", 1)
	r.publishA("a1.json", 2)
	r.hookO.setFail(knownNode + changesSuffix)
	r.oPublishesAndFlushes()
	snap := decodeSnapshot(t, rawRow(t, r.store, knownTenant, knownConn, knownNode))
	if ch := decodeChanges(t, rawRow(t, r.store, knownTenant, knownConn, knownNode+changesSuffix)); ch.Base == snap.Gen {
		t.Fatal("premise: O's changes reset was written")
	}
	r.recoversAndRestartIsQuiet([]string{"a0.json", "a1.json"}, 2)
}

// On a quiet folder there may be no more saves for days: the first poll after
// the bound in time looks again.
func TestKnownChanges_QuietPollRecoversAfterTheTimeBound(t *testing.T) {
	r := newStaleRig(t)
	r.publishA("a0.json", 1)
	r.publishA("a1.json", 2)
	r.hookOnNextSave(r.oPublishesAndFlushes)
	r.publishA("a2.json", 3)
	gen := r.a.ac.savedGen

	ctx := context.Background()
	// The earlier polls moved the clock; measure from now.
	r.a.ac.verifiedAt = r.a.clock.now()
	r.a.clock.advance(staleBoundTime - time.Second)
	r.a.c.pollOnce(ctx, r.a.ac)
	if r.a.ac.savedGen != gen {
		t.Fatal("A looked again before the time bound")
	}
	r.a.clock.advance(2 * time.Second)
	r.a.c.pollOnce(ctx, r.a.ac) // reads, sees O's snapshot, merges, saves its own
	r.a.c.pollOnce(ctx, r.a.ac)
	if r.a.ac.savedGen == gen {
		t.Fatalf("A still on snapshot %q a poll after the time bound", gen)
	}
	requireTimes(t, "A", r.probeA, "o1.json", 0)
}

// countSnapshotReads makes A's store count reads of the snapshot row.
func (r *staleRig) countSnapshotReads() *int {
	n := new(int)
	r.hookA.setHook(func(node string) {
		if node == knownNode {
			*n++
		}
	})
	return n
}

// A changes row nobody can decode counts as no saved set (#563). The idle
// check finds it and replaces the rows, as a save with something to write
// would, so a restart does not adopt files that were never published.
func TestKnownChanges_IdlePollRepairsAnUndecodableChangesRow(t *testing.T) {
	r := newStaleRig(t)
	r.publishA("a0.json", 1)
	r.publishA("a1.json", 2)
	r.publishA("a2.json", 3)
	if err := r.store.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: knownTenant, ConnectionID: knownConn, NodeID: knownNode + changesSuffix, State: []byte(`"x"`),
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r.a.ac.verifiedAt = r.a.clock.now()
	r.a.clock.advance(staleBoundTime + time.Second)
	r.a.c.pollOnce(ctx, r.a.ac)
	r.a.c.pollOnce(ctx, r.a.ac)

	snap := decodeSnapshot(t, rawRow(t, r.store, knownTenant, knownConn, knownNode))
	ch := decodeChanges(t, rawRow(t, r.store, knownTenant, knownConn, knownNode+changesSuffix))
	if snap.Gen == "" || ch.Base != snap.Gen {
		t.Fatalf("snapshot gen %q, changes base %q; want a decodable changes row on the snapshot", snap.Gen, ch.Base)
	}
	have := map[string]bool{}
	for _, n := range snap.Known {
		have[n] = true
	}
	for n := range snap.Files {
		have[n] = true
	}
	for _, n := range []string{"a0.json", "a1.json", "a2.json"} {
		if !have[n] {
			t.Errorf("the snapshot lacks %s", n)
		}
	}
}

// A healthy watcher with nothing to save reads the snapshot row at most once
// over an hour of idle polls: the check only runs while changes-only saves are
// unchecked.
func TestKnownChanges_IdleHealthyWatcherReadsTheSnapshotAtMostOnce(t *testing.T) {
	r := newStaleRig(t)
	r.publishA("a0.json", 1)
	r.publishA("a1.json", 2)
	reads := r.countSnapshotReads()
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		r.a.clock.advance(staleBoundTime + time.Second)
		r.a.c.pollOnce(ctx, r.a.ac)
	}
	if *reads > 1 {
		t.Errorf("read the snapshot row %d times over 12 idle intervals, want at most 1", *reads)
	}
}

// While nobody else saves, the check costs one read of the snapshot per
// staleBoundSaves changes-only saves, not one per save after the first.
func TestKnownChanges_VerificationReadsTheSnapshotOncePerBound(t *testing.T) {
	r := newStaleRig(t)
	r.publishA("a0.json", 1)
	reads := r.countSnapshotReads()
	saves := 3 * staleBoundSaves
	for i := 1; i <= saves; i++ {
		if r.publishA(fmt.Sprintf("b%d.json", i), 1+i) {
			t.Fatalf("save %d wrote a snapshot with nobody else saving", i)
		}
	}
	if *reads < 1 || *reads > 3 {
		t.Errorf("read the snapshot row %d times in %d saves, want 1 to 3", *reads, saves)
	}
}
