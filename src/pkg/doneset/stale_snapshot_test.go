package doneset

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// The bound the issue promises, written out so a change to the constants has
// to change the tests too: recovery within 8 changes-only saves or 5 minutes.
const (
	boundSaves = 8
	boundTime  = 5 * time.Minute
)

// #723: a Set must not keep writing changes on a snapshot nobody reads. The
// changes-row check in ownsSaved can say "mine" after another poller wrote a
// snapshot (its changes reset was overwritten by this Set's write, or failed),
// so every verifyEvery changes-only saves, or verifyAfter, the snapshot's Gen
// is compared too.

// hookStore wraps a Store: afterGet runs after each Get of a node id (once
// armed), and saves of failSaveNode fail while it is set.
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

func (h *hookStore) set(f func(nodeID string)) {
	h.mu.Lock()
	h.afterGet = f
	h.mu.Unlock()
}

// staleScenario drives the shared story over any store: A has a snapshot of
// ten names; the stale state is produced by makeStale; then A saves one new
// name per poll. It returns after how many saves A wrote a snapshot (0 if it
// never did within limit saves) and the final reader.
type staleRig struct {
	a       *Set
	b       *Set
	listing map[string]Stamp
}

func newStaleRig(t *testing.T, storeA, storeB checkpoint.Store, key Key) *staleRig {
	t.Helper()
	a, _ := loaded(t, storeA, key, tScope)
	listing := map[string]Stamp{}
	for _, n := range names(100, "f") {
		listing[n] = st(1, 0)
		a.Mark(n, st(1, 0))
	}
	a.Observe(listing)
	save(t, a) // A's snapshot
	b, found := loaded(t, storeB, key, tScope)
	if !found {
		t.Fatal("B found no saved set")
	}
	return &staleRig{a: a, b: b, listing: listing}
}

// publishA makes A publish and save one more name; it reports whether the
// save wrote a snapshot.
func (r *staleRig) publishA(t *testing.T, store *testStore, name string) bool {
	t.Helper()
	r.listing[name] = st(1, 0)
	r.a.Observe(r.listing)
	if err := r.a.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.a.Mark(name, st(1, 0))
	before := r.a.gen
	save(t, r.a)
	// A's own snapshot has a new Gen; another poller's writes in between
	// don't count.
	return r.a.gen != before
}

func (r *staleRig) wantRecovered(t *testing.T, store *testStore, aNames []string) {
	t.Helper()
	rd := reader(t, store)
	for _, n := range append([]string{"fromB", "f0000"}, aNames...) {
		if !rd.Done(n, st(1, 0)) {
			t.Errorf("a restart finds %s not done, so it would be published again", n)
		}
	}
}

// B's snapshot and changes reset land between A's read of the changes row and
// its write. A's write overwrites B's reset, so the changes row says "A's"
// while the snapshot is B's. A has to notice within the bound.
func TestSet_ChangesWrittenOnAReplacedSnapshotAreRecoveredWithinTheBound(t *testing.T) {
	store := newTestStore()
	hooked := &hookStore{Store: store}
	r := newStaleRig(t, hooked, store, tKey)

	// One changes-only save first, so there is something to verify.
	if r.publishA(t, store, "a0") {
		t.Fatal("premise: A's first save after its snapshot wrote a snapshot")
	}
	// Fire on the second read of the changes row after this point: the first
	// is the poll's Refresh, the second the Save that is about to write.
	reads := 0
	hooked.set(func(node string) {
		if node != tNode+changesSuffix {
			return
		}
		if reads++; reads != 2 {
			return
		}
		r.listing["fromB"] = st(1, 0)
		r.b.Observe(r.listing)
		r.b.Mark("fromB", st(1, 0))
		if err := r.b.Save(context.Background()); err != nil {
			t.Error(err)
		}
	})
	aNames := []string{"a0"}
	recoveredAt := 0
	for i := 1; i <= boundSaves && recoveredAt == 0; i++ {
		n := "a" + string(rune('0'+i))
		aNames = append(aNames, n)
		if r.publishA(t, store, n) {
			recoveredAt = i
		}
	}
	if recoveredAt == 0 {
		t.Fatalf("A wrote changes on a snapshot nobody reads through %d saves", boundSaves)
	}
	t.Logf("A recovered at its save %d", recoveredAt)
	r.wantRecovered(t, store, aNames)

	// Back on the changes row afterwards.
	if r.publishA(t, store, "later") {
		t.Error("A's next save after recovering wrote a snapshot again")
	}
	if !reader(t, store).Done("later", st(1, 0)) {
		t.Error("a name saved after the recovery is not found by a restart")
	}
}

// B's snapshot lands but its changes reset fails, so the changes row is still
// on A's snapshot and A's check passes.
func TestSet_FailedChangesResetOfAnotherSnapshotIsRecoveredWithinTheBound(t *testing.T) {
	store := newTestStore()
	failing := &hookStore{Store: store}
	r := newStaleRig(t, store, failing, tKey)
	if r.publishA(t, store, "a0") {
		t.Fatal("premise: A's first save after its snapshot wrote a snapshot")
	}

	failing.mu.Lock()
	failing.failSaveNode = tNode + changesSuffix
	failing.mu.Unlock()
	r.listing["fromB"] = st(1, 0)
	r.b.Observe(r.listing)
	r.b.Mark("fromB", st(1, 0))
	if err := r.b.Save(context.Background()); err == nil {
		t.Fatal("premise: B's save succeeded though its changes reset fails")
	}

	aNames := []string{"a0"}
	recovered := false
	for i := 1; i <= boundSaves && !recovered; i++ {
		n := "a" + string(rune('0'+i))
		aNames = append(aNames, n)
		recovered = r.publishA(t, store, n)
	}
	if !recovered {
		t.Fatalf("A wrote changes on a snapshot nobody reads through %d saves", boundSaves)
	}
	r.wantRecovered(t, store, aNames)
}

// On a quiet folder there may be no further saves for days, so Refresh checks
// too once verifyAfter has passed since the last check.
func TestSet_RefreshRecoversAfterVerifyAfterOnAQuietFolder(t *testing.T) {
	store := newTestStore()
	hooked := &hookStore{Store: store}
	r := newStaleRig(t, hooked, store, tKey)
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	r.a.now = func() time.Time { return clock }
	r.a.verifiedAt = clock

	// Fire on the second read of the changes row after this point: the first
	// is the poll's Refresh, the second the Save that is about to write.
	reads := 0
	hooked.set(func(node string) {
		if node != tNode+changesSuffix {
			return
		}
		if reads++; reads != 2 {
			return
		}
		r.listing["fromB"] = st(1, 0)
		r.b.Observe(r.listing)
		r.b.Mark("fromB", st(1, 0))
		if err := r.b.Save(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if r.publishA(t, store, "a0") {
		t.Fatal("premise: A's save wrote a snapshot")
	}
	if reads < 2 {
		t.Fatal("premise: the other poller did not save between A's read and write")
	}
	// Quiet: polls with nothing to save. Before the time is up A still thinks
	// the changes row is its own.
	r.a.Observe(r.listing)
	clock = clock.Add(boundTime - time.Second)
	if err := r.a.Refresh(context.Background()); err != nil || r.a.gen == "" {
		t.Fatalf("Refresh before verifyAfter: err %v, gen %q; want it to keep its snapshot", err, r.a.gen)
	}
	clock = clock.Add(2 * time.Second)
	if err := r.a.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.a.gen != "" || !r.a.Done("fromB", st(1, 0)) {
		t.Fatalf("Refresh after verifyAfter: gen %q, fromB done %v; want B's snapshot merged", r.a.gen, r.a.Done("fromB", st(1, 0)))
	}
	r.a.Mark("a1", st(1, 0))
	r.listing["a1"] = st(1, 0)
	save(t, r.a)
	r.wantRecovered(t, store, []string{"a0", "a1"})
}

// The same on a folder where nothing is published: the poll's Save has nothing
// to write but still checks once the time is up, and writes a snapshot with
// what the stale writes held.
func TestSet_IdleSaveRecoversAfterVerifyAfter(t *testing.T) {
	store := newTestStore()
	hooked := &hookStore{Store: store}
	r := newStaleRig(t, hooked, store, tKey)
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	r.a.now = func() time.Time { return clock }
	r.a.verifiedAt = clock

	reads := 0
	hooked.set(func(node string) {
		if node != tNode+changesSuffix {
			return
		}
		if reads++; reads != 2 {
			return
		}
		r.listing["fromB"] = st(1, 0)
		r.b.Observe(r.listing)
		r.b.Mark("fromB", st(1, 0))
		if err := r.b.Save(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if r.publishA(t, store, "a0") {
		t.Fatal("premise: A's save wrote a snapshot")
	}
	store.takeWrites()
	clock = clock.Add(boundTime - time.Second)
	save(t, r.a)
	if w := store.takeWrites(); len(w) != 0 {
		t.Fatalf("idle Save before the bound wrote %v", w)
	}
	clock = clock.Add(2 * time.Second)
	r.a.Observe(r.listing)
	save(t, r.a)
	r.wantRecovered(t, store, []string{"a0"})
	store.takeWrites()
	save(t, r.a)
	if w := store.takeWrites(); len(w) != 0 {
		t.Errorf("a second idle Save wrote %v", w)
	}
}

// While nobody else saved, the check costs one read of the snapshot per
// verifyEvery changes-only saves and never more.
func TestSet_VerificationReadsTheSnapshotOncePerBound(t *testing.T) {
	store := newTestStore()
	r := newStaleRig(t, store, store, tKey)
	store.takeReads()
	snapshotReads := 0
	for i := 0; i < 3*boundSaves; i++ {
		if r.publishA(t, store, "n"+string(rune('a'+i%26))+string(rune('a'+i/26))) {
			t.Fatalf("save %d wrote a snapshot with nobody else saving", i)
		}
	}
	for _, rd := range store.takeReads() {
		if rd == tNode {
			snapshotReads++
		}
	}
	if snapshotReads == 0 || snapshotReads > 3 {
		t.Errorf("read the snapshot row %d times in %d saves, want at most 3 and at least 1", snapshotReads, 3*boundSaves)
	}
}

// A healthy Set with nothing to save reads the snapshot row at most once over
// an hour of idle Saves: the check only runs while changes-only saves are
// unchecked.
func TestSet_IdleHealthySetReadsTheSnapshotAtMostOnce(t *testing.T) {
	store := newTestStore()
	r := newStaleRig(t, store, store, tKey)
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	r.a.now = func() time.Time { return clock }
	r.a.verifiedAt = clock
	if r.publishA(t, store, "a0") {
		t.Fatal("premise: A's save wrote a snapshot")
	}
	store.takeReads()
	for i := 0; i < 12; i++ {
		clock = clock.Add(boundTime + time.Second)
		save(t, r.a)
	}
	n := 0
	for _, rd := range store.takeReads() {
		if rd == tNode {
			n++
		}
	}
	if n > 1 {
		t.Errorf("read the snapshot row %d times over 12 idle intervals, want at most 1", n)
	}
}
