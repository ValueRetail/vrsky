package doneset

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Tests for #718: a stamp carries the store's version (ETag, generation), is
// compared only when both sides have one, and done sets saved before that keep
// working in both directions.

// vst is st with a version.
func vst(n int64, i int, version string) Stamp {
	s := st(n, i)
	s.Version = version
	return s
}

func TestMatch_Same(t *testing.T) {
	cases := []struct {
		name     string
		a, b     Stamp
		wantTime bool // MatchTime
		wantVers bool // MatchVersion
	}{
		{"same, no versions", st(5, 1), st(5, 1), true, true},
		{"same, same version", vst(5, 1, "v1"), vst(5, 1, "v1"), true, true},
		{"same size and second, other version", vst(5, 1, "v1"), vst(5, 1, "v2"), false, false},
		{"other size, same version", vst(5, 1, "v1"), vst(6, 1, "v1"), false, false},
		{"other mtime, same version", vst(5, 1, "v1"), vst(5, 2, "v1"), false, true},
		{"one side without a version: mtime decides (same)", st(5, 1), vst(5, 1, "v1"), true, true},
		{"one side without a version: mtime decides (other)", st(5, 1), vst(5, 2, "v1"), false, false},
		{"neither has a version, other mtime", st(5, 1), st(5, 2), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, ab := range [][2]Stamp{{tc.a, tc.b}, {tc.b, tc.a}} {
				if got := MatchTime.Same(ab[0], ab[1]); got != tc.wantTime {
					t.Errorf("MatchTime.Same(%+v, %+v) = %v, want %v", ab[0], ab[1], got, tc.wantTime)
				}
				if got := MatchVersion.Same(ab[0], ab[1]); got != tc.wantVers {
					t.Errorf("MatchVersion.Same(%+v, %+v) = %v, want %v", ab[0], ab[1], got, tc.wantVers)
				}
			}
		})
	}
}

// A same-size replacement in the same second is not done once the published
// stamp has a version.
func TestSet_DoneTellsSameSecondReplacementsApart(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Mark("a", vst(5, 1, "v1"))
	save(t, s)
	for _, set := range []*Set{s, reader(t, store)} {
		if !set.Done("a", vst(5, 1, "v1")) {
			t.Error("a not done at the stamp it was published with")
		}
		if set.Done("a", vst(5, 1, "v2")) {
			t.Error("a same-size, same-second replacement (another version) counts as done")
		}
	}
}

// Rows saved before #718 have no version in their stamps. They must still
// read as done against a listing that has versions, or every object left in a
// bucket would be published again at the deploy.
func TestSet_ReadsDoneSetsSavedBeforeVersions(t *testing.T) {
	// The row format of a release before #718, written out by hand.
	const oldSnapshot = `{"scope":"` + tScope + `","gen":"g1","files":{"a":{"size":5,"mtime":%d,"mtime_nsec":0}}}`
	for _, m := range []Match{MatchTime, MatchVersion} {
		store := newTestStore()
		raw := []byte(fmt.Sprintf(oldSnapshot, st(5, 1).MTime))
		store.mu.Lock()
		store.rows[[3]string{tTenant, tConn, tNode}] = checkpoint.Checkpoint{
			TenantID: tTenant, ConnectionID: tConn, NodeID: tNode, State: raw,
		}
		store.mu.Unlock()

		s := New(store, tKey, tScope)
		s.UseMatch(m)
		found, err := s.Load(context.Background())
		if err != nil || !found {
			t.Fatalf("match %d: Load = %v, %v; want the old row", m, found, err)
		}
		if s.Undecodable() != nil {
			t.Fatalf("match %d: old row undecodable: %v", m, s.Undecodable())
		}
		if !s.Done("a", vst(5, 1, "etag-1")) {
			t.Errorf("match %d: an object saved before #718 is not done against its versioned listing: it would be published again", m)
		}
		if s.Done("a", vst(5, 2, "etag-1")) {
			t.Errorf("match %d: an old entry matched a listing with another mtime", m)
		}
	}
}

// A release before #718 reads a row written now: the version is a field it
// does not know, and its stamps still compare equal to its own listing's.
func TestSet_RowsStayReadableByOlderReleases(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Mark("a", vst(5, 1, "etag-1"))
	save(t, s)

	store.mu.Lock()
	cp := store.rows[[3]string{tTenant, tConn, tNode}]
	store.mu.Unlock()
	// The old release's types.
	type oldStamp struct {
		Size      int64 `json:"size"`
		MTime     int64 `json:"mtime"`
		MTimeNsec int64 `json:"mtime_nsec"`
	}
	var old struct {
		Scope string              `json:"scope"`
		Gen   string              `json:"gen"`
		Files map[string]oldStamp `json:"files"`
	}
	if err := json.Unmarshal(cp.State, &old); err != nil {
		t.Fatalf("an older release can't decode the row: %v\n%s", err, cp.State)
	}
	want := oldStamp{Size: 5, MTime: st(5, 1).MTime}
	if old.Files["a"] != want {
		t.Errorf("older release reads a as %+v, want %+v", old.Files["a"], want)
	}
	var now snapshotState
	if err := json.Unmarshal(cp.State, &now); err != nil {
		t.Fatal(err)
	}
	if now.Files["a"].Version != "etag-1" {
		t.Errorf("version not saved: %s", cp.State)
	}
}

// Under MatchVersion a metadata-only edit (another mtime, same version) leaves
// a name done, also for what another poller saved (Refresh).
func TestSet_MatchVersionIgnoresMTime(t *testing.T) {
	store := newTestStore()
	theirs, _ := loaded(t, store, tKey, tScope)
	theirs.Observe(map[string]Stamp{"x": vst(1, 0, "7")})
	theirs.Mark("x", vst(1, 0, "7"))
	save(t, theirs)

	mine := New(store, tKey, tScope)
	mine.UseMatch(MatchVersion)
	if _, err := mine.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	edited := vst(1, 9, "7")
	mine.Observe(map[string]Stamp{"x": edited})
	if !mine.Done("x", edited) {
		t.Error("x not done after a metadata-only edit (same generation)")
	}

	// Refresh merges what the other Set saved since, under the same rule.
	other := New(store, tKey, tScope)
	other.UseMatch(MatchVersion)
	if _, err := other.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	theirs.Observe(map[string]Stamp{"x": vst(1, 0, "7"), "y": vst(2, 0, "8")})
	theirs.Mark("y", vst(2, 0, "8"))
	save(t, theirs)
	other.Observe(map[string]Stamp{"x": edited, "y": vst(2, 4, "8")})
	if err := other.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !other.Done("y", vst(2, 4, "8")) {
		t.Error("y saved by another poller not merged across a metadata-only edit")
	}
}

func TestSet_Forget(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Observe(map[string]Stamp{"a": st(1, 0)})
	s.Mark("a", st(1, 0))
	s.RetryFailed("a", t0)
	save(t, s)

	s.Forget("a")
	if s.Done("a", st(1, 0)) {
		t.Error("a still done after Forget")
	}
	if s.Retrying() != 0 {
		t.Errorf("Retrying = %d after Forget, want 0", s.Retrying())
	}
	save(t, s)
	if reader(t, store).Done("a", st(1, 0)) {
		t.Error("a still done for a later reader: Forget was not saved")
	}
	s.Forget("never-done") // no panic, nothing to save
}
