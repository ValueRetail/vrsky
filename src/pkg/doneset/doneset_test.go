package doneset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Unit tests for the done set (#538). Every assertion is about what a later
// reader of the rows finds done, or which rows a save writes; the row format
// is only looked at where a test has to plant a row another writer left.

const (
	tTenant = "tenant-a"
	tConn   = "conn-1"
	tNode   = "c1"
	tScope  = "sftp://u@host:22/in"
)

var tKey = Key{TenantID: tTenant, ConnectionID: tConn, NodeID: tNode}

// testStore is a goroutine-safe checkpoint.Store that copies on the way in
// and out, the way a database does, records which rows are written and read,
// and can fail reads or writes on demand.
type testStore struct {
	mu       sync.Mutex
	rows     map[[3]string]checkpoint.Checkpoint
	writes   []string // node ids, in order
	reads    []string // node ids, in order
	failGet  error
	failSave error
}

func newTestStore() *testStore {
	return &testStore{rows: make(map[[3]string]checkpoint.Checkpoint)}
}

func (s *testStore) Save(_ context.Context, cp *checkpoint.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSave != nil {
		return s.failSave
	}
	c := *cp
	c.State = append(json.RawMessage(nil), cp.State...)
	s.rows[[3]string{cp.TenantID, cp.ConnectionID, cp.NodeID}] = c
	s.writes = append(s.writes, cp.NodeID)
	return nil
}

func (s *testStore) Get(_ context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, nodeID)
	if s.failGet != nil {
		return nil, s.failGet
	}
	cp, ok := s.rows[[3]string{tenantID, connectionID, nodeID}]
	if !ok {
		return nil, nil
	}
	cp.State = append(json.RawMessage(nil), cp.State...)
	return &cp, nil
}

func (s *testStore) Delete(_ context.Context, tenantID, connectionID, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, [3]string{tenantID, connectionID, nodeID})
	return nil
}

func (s *testStore) DeleteForConnection(_ context.Context, tenantID, connectionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.rows {
		if k[0] == tenantID && k[1] == connectionID {
			delete(s.rows, k)
		}
	}
	return nil
}

// put plants a row as another writer would have left it.
func (s *testStore) put(t *testing.T, key Key, nodeID string, state any) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[[3]string{key.TenantID, key.ConnectionID, nodeID}] = checkpoint.Checkpoint{
		TenantID: key.TenantID, ConnectionID: key.ConnectionID, NodeID: nodeID, State: data,
	}
}

// takeWrites returns the node ids written since the last call.
func (s *testStore) takeWrites() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.writes
	s.writes = nil
	return w
}

// takeReads returns the node ids read since the last call.
func (s *testStore) takeReads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.reads
	s.reads = nil
	return r
}

func (s *testStore) setFailGet(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failGet = err
}

func (s *testStore) setFailSave(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failSave = err
}

var t0 = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

// st is the stamp of a listing entry of size n, modified i seconds after t0.
func st(n int64, i int) Stamp { return StampOf(n, t0.Add(time.Duration(i)*time.Second)) }

func names(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%04d", prefix, i)
	}
	return out
}

// loaded returns a Set for key and scope that has read the store.
func loaded(t *testing.T, store checkpoint.Store, key Key, scope string) (*Set, bool) {
	t.Helper()
	s := New(store, key, scope)
	found, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !s.Loaded() {
		t.Fatal("Loaded() = false after a Load without error")
	}
	return s, found
}

func save(t *testing.T, s *Set) {
	t.Helper()
	if err := s.Save(context.Background()); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// reader is what the next start finds: a fresh Set loaded from the store.
func reader(t *testing.T, store checkpoint.Store) *Set {
	t.Helper()
	s, _ := loaded(t, store, tKey, tScope)
	return s
}

func TestStampOf(t *testing.T) {
	mt := time.Date(2026, 1, 2, 3, 4, 5, 678, time.UTC)
	cases := []struct {
		name  string
		size  int64
		mtime time.Time
		want  Stamp
	}{
		{"no mtime keeps only the size", 10, time.Time{}, Stamp{Size: 10}},
		{"mtime down to the nanosecond", 10, mt, Stamp{Size: 10, MTime: mt.Unix(), MTimeNsec: 678}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StampOf(tc.size, tc.mtime); got != tc.want {
				t.Errorf("StampOf = %+v, want %+v", got, tc.want)
			}
		})
	}
	if StampOf(1, mt) == StampOf(1, mt.Add(time.Nanosecond)) {
		t.Error("stamps a nanosecond apart compare equal")
	}
}

func TestSet_NilStoreIsMemoryOnly(t *testing.T) {
	s := New(nil, tKey, tScope)
	if !s.Loaded() {
		t.Fatal("a Set without a store must count as loaded")
	}
	s.Mark("a", st(1, 0))
	if !s.Done("a", st(1, 0)) {
		t.Error("a not done after Mark")
	}
	for _, f := range []func(context.Context) error{s.Save, s.Refresh} {
		if err := f(context.Background()); err != nil {
			t.Errorf("without a store: %v", err)
		}
	}
	if found, err := s.Load(context.Background()); found || err != nil {
		t.Errorf("Load without a store = %v, %v; want false, nil", found, err)
	}
}

func TestSet_DoneComparesNameAndStamp(t *testing.T) {
	store := newTestStore()
	s, found := loaded(t, store, tKey, tScope)
	if found {
		t.Fatal("found a saved set in an empty store")
	}
	s.Mark("a", st(5, 1))
	save(t, s)

	r := reader(t, store)
	cases := []struct {
		name string
		file string
		st   Stamp
		want bool
	}{
		{"same name and stamp", "a", st(5, 1), true},
		{"new size", "a", st(6, 1), false},
		{"new mtime", "a", st(5, 2), false},
		{"new mtime nanoseconds", "a", StampOf(5, t0.Add(time.Second+time.Nanosecond)), false},
		{"other name", "b", st(5, 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, set := range []*Set{s, r} {
				if got := set.Done(tc.file, tc.st); got != tc.want {
					t.Errorf("Done(%s, %+v) = %v, want %v", tc.file, tc.st, got, tc.want)
				}
			}
		})
	}
}

// A first start is saved even when nothing is published, so the next start is
// not a first one.
func TestSet_FirstStartIsSavedEvenEmpty(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	save(t, s)
	if w := store.takeWrites(); len(w) == 0 {
		t.Fatal("first start wrote nothing")
	}
	if _, found := loaded(t, store, tKey, tScope); !found {
		t.Error("second start found no saved set")
	}
	// Nothing changed since: no write at all.
	s2, _ := loaded(t, store, tKey, tScope)
	save(t, s2)
	if w := store.takeWrites(); len(w) != 0 {
		t.Errorf("save with nothing changed wrote %v", w)
	}
}

// Which rows a save writes: only the changes row while the changes are few,
// a snapshot past min(512, len/2) changed names, and always a snapshot at the
// first save after a load. Whatever is written, the next reader sees it all.
func TestSet_ChangesRowOrSnapshot(t *testing.T) {
	changesOnly := []string{tNode + changesSuffix}
	snapshot := []string{tNode, tNode + changesSuffix}
	cases := []struct {
		name    string
		size    int // done names after the first save
		changed int // names marked with a new stamp before the second save
		gone    int // names pruned by Observe before the second save
		want    []string
	}{
		{"one change in ten", 10, 1, 0, changesOnly},
		{"half of ten", 10, 5, 0, changesOnly},
		{"more than half of ten", 10, 6, 0, snapshot},
		{"one gone in ten", 10, 0, 1, changesOnly},
		{"changed plus gone past half", 10, 3, 3, snapshot},
		{"512 of 2000", 2000, 512, 0, changesOnly},
		{"513 of 2000", 2000, 513, 0, snapshot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore()
			s, _ := loaded(t, store, tKey, tScope)
			all := names(tc.size, "f")
			listing := make(map[string]Stamp, len(all))
			for _, n := range all {
				listing[n] = st(1, 0)
				s.Mark(n, st(1, 0))
			}
			s.Observe(listing)
			save(t, s)
			if w := store.takeWrites(); strings.Join(w, ",") != strings.Join(snapshot, ",") {
				t.Fatalf("first save wrote %v, want %v", w, snapshot)
			}

			for _, n := range all[:tc.changed] {
				listing[n] = st(2, 1)
			}
			for _, n := range all[tc.changed : tc.changed+tc.gone] {
				delete(listing, n)
			}
			s.Observe(listing)
			for _, n := range all[:tc.changed] {
				s.Mark(n, st(2, 1))
			}
			save(t, s)
			if w := store.takeWrites(); strings.Join(w, ",") != strings.Join(tc.want, ",") {
				t.Errorf("second save wrote %v, want %v", w, tc.want)
			}

			r := reader(t, store)
			if r.Len() != len(listing) {
				t.Errorf("reader has %d done names, want %d", r.Len(), len(listing))
			}
			for n, want := range listing {
				if !r.Done(n, want) {
					t.Errorf("reader: %s not done with %+v", n, want)
					break
				}
			}
			for _, n := range all[tc.changed : tc.changed+tc.gone] {
				if _, ok := r.done[n]; ok {
					t.Errorf("reader: %s still done after it left the listing", n)
				}
			}
		})
	}
}

// The first save after a load is a snapshot even with one change, so the
// changes row never builds on a snapshot this Set did not write.
func TestSet_FirstSaveAfterLoadIsSnapshot(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	for _, n := range names(10, "f") {
		s.Mark(n, st(1, 0))
	}
	save(t, s)
	store.takeWrites()

	s2, found := loaded(t, store, tKey, tScope)
	if !found {
		t.Fatal("no saved set")
	}
	s2.Mark("new", st(1, 0))
	save(t, s2)
	if w := store.takeWrites(); len(w) != 2 || w[0] != tNode {
		t.Errorf("first save after a load wrote %v, want the snapshot then its changes row", w)
	}
}

// A changes row is applied only on the snapshot whose Gen it names.
func TestSet_LoadAppliesChangesOnlyOnTheirSnapshot(t *testing.T) {
	cases := []struct {
		name  string
		base  string
		scope string
		wantA bool // a, which the changes row lists as gone
		wantB bool // b, which the changes row lists as done
	}{
		{name: "base is the snapshot's gen", base: "g1", scope: tScope, wantA: false, wantB: true},
		{name: "stale base", base: "g0", scope: tScope, wantA: true, wantB: false},
		{name: "empty base", base: "", scope: tScope, wantA: true, wantB: false},
		{name: "changes row for another scope", base: "g1", scope: "sftp://u@host:22/other", wantA: true, wantB: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore()
			store.put(t, tKey, tNode, snapshotState{Scope: tScope, Gen: "g1", Files: map[string]Stamp{"a": st(1, 0)}})
			store.put(t, tKey, tNode+changesSuffix, changesState{
				Scope: tc.scope, Base: tc.base, Files: map[string]Stamp{"b": st(2, 0)}, Gone: []string{"a"},
			})
			r, found := loaded(t, store, tKey, tScope)
			if !found {
				t.Fatal("snapshot not found")
			}
			if got := r.Done("a", st(1, 0)); got != tc.wantA {
				t.Errorf("a done = %v, want %v", got, tc.wantA)
			}
			if got := r.Done("b", st(2, 0)); got != tc.wantB {
				t.Errorf("b done = %v, want %v", got, tc.wantB)
			}
		})
	}
}

// A saved set for another scope (the folder, host, bucket or prefix was
// edited) is not used: the start counts as a first one.
func TestSet_ScopeMismatchIsAFirstStart(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Mark("a", st(1, 0))
	save(t, s)

	other, found := loaded(t, store, tKey, "sftp://u@host:22/elsewhere")
	if found {
		t.Error("a saved set for another scope was found")
	}
	if other.Len() != 0 || other.Done("a", st(1, 0)) {
		t.Error("a saved set for another scope was used")
	}
	// The other scope's first save replaces it; the old scope starts over too.
	save(t, other)
	if _, found := loaded(t, store, tKey, tScope); found {
		t.Error("the old scope's set survived the new scope's save")
	}
	if _, found := loaded(t, store, tKey, "sftp://u@host:22/elsewhere"); !found {
		t.Error("the new scope's save is not found")
	}
}

func TestSet_KeyedByTenantConnectionAndNode(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Mark("a", st(1, 0))
	save(t, s)

	for _, k := range []Key{
		{TenantID: "tenant-b", ConnectionID: tConn, NodeID: tNode},
		{TenantID: tTenant, ConnectionID: "conn-2", NodeID: tNode},
		{TenantID: tTenant, ConnectionID: tConn, NodeID: "c2"},
	} {
		other, found := loaded(t, store, k, tScope)
		if found || other.Done("a", st(1, 0)) {
			t.Errorf("key %+v sees the set saved under %+v", k, tKey)
		}
	}
}

func TestSet_ObservePrunesNamesMissingFromTheListing(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Observe(map[string]Stamp{"a": st(1, 0), "b": st(1, 0)})
	s.Mark("a", st(1, 0))
	s.Mark("b", st(1, 0))
	save(t, s)

	s.Observe(map[string]Stamp{"b": st(1, 0)})
	if s.Done("a", st(1, 0)) {
		t.Error("a still done after it left the listing")
	}
	save(t, s)
	r := reader(t, store)
	if r.Done("a", st(1, 0)) || !r.Done("b", st(1, 0)) {
		t.Errorf("reader: a done = %v, b done = %v; want false, true", r.Done("a", st(1, 0)), r.Done("b", st(1, 0)))
	}
}

func TestSet_LoadErrorLeavesItUnloaded(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Mark("a", st(1, 0))
	save(t, s)

	store.setFailGet(errors.New("db down"))
	r := New(store, tKey, tScope)
	if _, err := r.Load(context.Background()); err == nil {
		t.Fatal("Load succeeded while reads fail")
	}
	if r.Loaded() {
		t.Error("Loaded() = true after a failed Load")
	}
	// An unloaded Set never saves: it would overwrite the saved set with an
	// empty one.
	store.takeWrites()
	r.Mark("b", st(1, 0))
	if err := r.Save(context.Background()); err != nil {
		t.Errorf("Save of an unloaded Set: %v", err)
	}
	if w := store.takeWrites(); len(w) != 0 {
		t.Errorf("an unloaded Set wrote %v", w)
	}

	store.setFailGet(nil)
	if found, err := r.Load(context.Background()); err != nil || !found {
		t.Fatalf("Load after reads work again = %v, %v", found, err)
	}
	if !r.Done("a", st(1, 0)) {
		t.Error("a not done after the retried Load")
	}
}

// A changes-row read that fails fails the Load too: the snapshot alone may
// miss what was published since.
func TestSet_LoadFailsWhenTheChangesRowCannotBeRead(t *testing.T) {
	store := &failChangesStore{testStore: newTestStore()}
	s, _ := loaded(t, store, tKey, tScope)
	s.Mark("a", st(1, 0))
	save(t, s)
	store.fail = true
	r := New(store, tKey, tScope)
	if _, err := r.Load(context.Background()); err == nil || r.Loaded() {
		t.Errorf("Load with an unreadable changes row = %v, loaded %v; want an error, not loaded", err, r.Loaded())
	}
}

type failChangesStore struct {
	*testStore
	fail bool
}

func (s *failChangesStore) Get(ctx context.Context, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	if s.fail && strings.HasSuffix(nodeID, changesSuffix) {
		return nil, errors.New("changes row unreadable")
	}
	return s.testStore.Get(ctx, tenantID, connectionID, nodeID)
}

func TestSet_SaveErrorIsRetriedAtTheNextSave(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	for _, n := range names(10, "f") {
		s.Mark(n, st(1, 0))
	}
	save(t, s)

	s.Mark("late", st(1, 0))
	store.setFailSave(errors.New("db down"))
	if err := s.Save(context.Background()); err == nil {
		t.Fatal("Save succeeded while writes fail")
	}
	store.setFailSave(nil)
	save(t, s)
	if !reader(t, store).Done("late", st(1, 0)) {
		t.Error("a name whose save failed is not saved by the next Save")
	}
}

// Save runs on a context detached from the poller's: a stopped poller still
// records what it published.
func TestSet_SaveSurvivesACancelledContext(t *testing.T) {
	store := &ctxStore{testStore: newTestStore()}
	s, _ := loaded(t, store, tKey, tScope)
	s.Mark("a", st(1, 0))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Save(ctx); err != nil {
		t.Fatalf("Save on a cancelled context: %v", err)
	}
	if !reader(t, store).Done("a", st(1, 0)) {
		t.Error("a not saved")
	}
}

// ctxStore fails a write whose context is done, as a database driver does.
type ctxStore struct{ *testStore }

func (s *ctxStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.testStore.Save(ctx, cp)
}

// Refresh takes what another Set on the same key saved, but only names the
// last listing still shows with the stamp they were saved with.
func TestSet_RefreshMergesOnlyWhatTheListingStillShows(t *testing.T) {
	cases := []struct {
		name     string
		theirs   Stamp // what the other Set saved x with
		listing  map[string]Stamp
		wantDone bool // x done for this Set after Refresh, with its listed stamp
	}{
		{"same stamp in the listing", st(1, 0), map[string]Stamp{"x": st(1, 0)}, true},
		{"replaced since (size)", st(1, 0), map[string]Stamp{"x": st(2, 0)}, false},
		{"replaced since (mtime)", st(1, 0), map[string]Stamp{"x": st(1, 5)}, false},
		{"gone from the listing", st(1, 0), map[string]Stamp{"y": st(1, 0)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore()
			mine, _ := loaded(t, store, tKey, tScope)
			theirs, _ := loaded(t, store, tKey, tScope)
			theirs.Observe(map[string]Stamp{"x": tc.theirs})
			theirs.Mark("x", tc.theirs)
			save(t, theirs)

			mine.Observe(tc.listing)
			if err := mine.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			listed, ok := tc.listing["x"]
			got := ok && mine.Done("x", listed)
			if got != tc.wantDone {
				t.Errorf("x done after Refresh = %v, want %v", got, tc.wantDone)
			}
			if mine.Done("x", tc.theirs) && !tc.wantDone {
				t.Error("x is done with the stamp the other Set saved, though the listing shows another")
			}
		})
	}
}

// While the changes row is on this Set's own snapshot, Refresh reads only
// that row; once another Set saved, it reads the whole set.
func TestSet_RefreshReadsOnlyTheChangesRowWhileItOwnsTheSave(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	listing := map[string]Stamp{}
	for _, n := range names(10, "f") {
		listing[n] = st(1, 0)
		s.Mark(n, st(1, 0))
	}
	s.Observe(listing)
	save(t, s)
	store.takeReads()

	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := store.takeReads(); strings.Join(r, ",") != tNode+changesSuffix {
		t.Errorf("Refresh on its own save read %v, want only the changes row", r)
	}

	other, _ := loaded(t, store, tKey, tScope)
	other.Mark("new", st(1, 0))
	save(t, other)
	listing["new"] = st(1, 0)
	s.Observe(listing)
	store.takeReads()
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := store.takeReads(); len(r) < 2 || !s.Done("new", st(1, 0)) {
		t.Errorf("Refresh after another save read %v, new done = %v; want the whole set read and new done", r, s.Done("new", st(1, 0)))
	}
}

// A read error in Refresh is returned and changes nothing.
func TestSet_RefreshError(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Observe(map[string]Stamp{"a": st(1, 0)})
	store.setFailGet(errors.New("db down"))
	if err := s.Refresh(context.Background()); err == nil {
		t.Error("Refresh succeeded while reads fail")
	}
	if s.Done("a", st(1, 0)) || !s.Loaded() {
		t.Error("a failed Refresh changed the Set")
	}
}

// Two Sets on one key, as in a rolling deploy: B wrote a snapshot after A's.
// A's next save must not write changes on its own, now stale, snapshot (every
// reader would ignore them); it merges B's and writes a snapshot.
func TestSet_SaveAfterAnotherSnapshotMergesInsteadOfWritingStaleChanges(t *testing.T) {
	store := newTestStore()
	a, _ := loaded(t, store, tKey, tScope)
	listing := map[string]Stamp{}
	for _, n := range names(10, "f") {
		listing[n] = st(1, 0)
		a.Mark(n, st(1, 0))
	}
	a.Observe(listing)
	save(t, a) // A's snapshot

	b, found := loaded(t, store, tKey, tScope)
	if !found {
		t.Fatal("B found no saved set")
	}
	listing["fromB"] = st(1, 0)
	b.Observe(listing)
	b.Mark("fromB", st(1, 0))
	save(t, b) // B's snapshot, its changes row on it
	store.takeWrites()

	listing["fromA"] = st(1, 0)
	a.Observe(listing)
	a.Mark("fromA", st(1, 0))
	save(t, a)
	if w := store.takeWrites(); len(w) == 0 || w[0] != tNode {
		t.Errorf("A's save after B's snapshot wrote %v, want a snapshot first", w)
	}

	r := reader(t, store)
	for _, n := range []string{"fromA", "fromB", "f0000"} {
		if !r.Done(n, st(1, 0)) {
			t.Errorf("reader: %s not done", n)
		}
	}

	// And A, owning the save again, goes back to the changes row.
	listing["again"] = st(1, 0)
	a.Observe(listing)
	a.Mark("again", st(1, 0))
	save(t, a)
	if w := store.takeWrites(); strings.Join(w, ",") != tNode+changesSuffix {
		t.Errorf("A's next save wrote %v, want only the changes row", w)
	}
	if !reader(t, store).Done("again", st(1, 0)) {
		t.Error("reader: again not done")
	}
}

// The first save after a Load or a Refresh writes a snapshot. A save another
// Set on the same key made since this one last read must survive it: A and B
// both load, B refreshes, A publishes x and saves, B publishes y and saves.
func TestSet_FirstSnapshotKeepsWhatAnotherSetSavedSinceItsRefresh(t *testing.T) {
	store := newTestStore()
	a, _ := loaded(t, store, tKey, tScope)
	b, _ := loaded(t, store, tKey, tScope)
	listing := map[string]Stamp{"x": st(1, 0), "y": st(1, 0)}
	a.Observe(listing)
	b.Observe(listing)
	if err := b.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	a.Mark("x", st(1, 0))
	save(t, a)
	b.Mark("y", st(1, 0))
	save(t, b)

	r := reader(t, store)
	if !r.Done("x", st(1, 0)) || !r.Done("y", st(1, 0)) {
		t.Errorf("saved set: x done = %v, y done = %v; want both", r.Done("x", st(1, 0)), r.Done("y", st(1, 0)))
	}
}

// Two Sets on one key taking turns: each publishes what the other has not,
// and the saved set ends up with both.
func TestSet_TwoSetsOneKeyTakingTurns(t *testing.T) {
	store := newTestStore()
	a, _ := loaded(t, store, tKey, tScope)
	b, _ := loaded(t, store, tKey, tScope)
	listing := map[string]Stamp{}
	published := map[string]int{}
	poll := func(s *Set) {
		s.Observe(listing)
		if err := s.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		for n, stamp := range listing {
			if !s.Done(n, stamp) {
				published[n]++
				s.Mark(n, stamp)
			}
		}
		save(t, s)
	}
	for i, n := range names(6, "x") {
		listing[n] = st(1, i)
		if i%2 == 0 {
			poll(a)
		} else {
			poll(b)
		}
	}
	poll(a)
	poll(b)
	for n, c := range published {
		if c != 1 {
			t.Errorf("%s published %d times, want 1", n, c)
		}
	}
	if len(published) != 6 {
		t.Errorf("published %d names, want 6", len(published))
	}
	if r := reader(t, store); r.Len() != 6 {
		t.Errorf("reader has %d done names, want 6", r.Len())
	}
}

// A node id too long to take the changes suffix only ever gets snapshots, and
// still round-trips.
func TestSet_LongNodeIDOnlySnapshots(t *testing.T) {
	store := newTestStore()
	key := Key{TenantID: tTenant, ConnectionID: tConn, NodeID: strings.Repeat("n", maxNodeIDLen-2)}
	s, _ := loaded(t, store, key, tScope)
	for _, n := range names(10, "f") {
		s.Mark(n, st(1, 0))
	}
	save(t, s)
	s.Mark("more", st(1, 0))
	save(t, s)
	for _, w := range store.takeWrites() {
		if w != key.NodeID {
			t.Errorf("wrote row %q, want only the snapshot row", w)
		}
	}
	r, found := loaded(t, store, key, tScope)
	if !found || !r.Done("more", st(1, 0)) || r.Len() != 11 {
		t.Errorf("reader: found %v, len %d, more done %v", found, r.Len(), r.Done("more", st(1, 0)))
	}
}
