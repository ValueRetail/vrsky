package doneset

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Tests for #617: a checkpoint row that is there but can't be decoded counts
// as no saved set (or, for the changes row, as no changes), and the first
// save replaces it. A store error still keeps the Set unloaded.

// Rows of the wrong shape: valid JSON, so a jsonb column takes them, but not
// what a snapshot or changes row holds.
var undecodableStates = map[string]any{
	"array":       []string{"a", "b"},
	"string":      "not a state",
	"wrong type":  map[string]any{"scope": tScope, "gen": "g1", "files": "nope"},
	"wrong scope": map[string]any{"scope": 7, "files": map[string]any{}},
}

func goodSnapshot() snapshotState {
	return snapshotState{Scope: tScope, Gen: "g1", Files: map[string]Stamp{"a": st(1, 0), "b": st(1, 0)}}
}

func TestSet_UndecodableSnapshotIsNoSavedSet(t *testing.T) {
	for shape, bad := range undecodableStates {
		for _, withChanges := range []bool{false, true} {
			name := shape
			if withChanges {
				name += ", good-looking changes row"
			}
			t.Run(name, func(t *testing.T) {
				store := newTestStore()
				store.put(t, tKey, tNode, bad)
				if withChanges {
					store.put(t, tKey, tNode+changesSuffix, changesState{
						Scope: tScope, Base: "g1", Files: map[string]Stamp{"c": st(1, 0)},
					})
				}

				s := New(store, tKey, tScope)
				found, err := s.Load(context.Background())
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if found {
					t.Error("found = true for a row that can't be decoded")
				}
				if !s.Loaded() {
					t.Error("Loaded() = false: the poller would publish nothing for good")
				}
				if !errors.Is(s.Undecodable(), ErrUndecodable) {
					t.Errorf("Undecodable() = %v, want ErrUndecodable", s.Undecodable())
				}
				if s.Len() != 0 {
					t.Errorf("Len = %d, want 0: nothing can be read from the row", s.Len())
				}

				// The poll publishes the folder; the save replaces the row.
				s.Mark("x", st(5, 0))
				store.takeWrites()
				save(t, s)
				if w := store.takeWrites(); !slices.Contains(w, tNode) {
					t.Errorf("save wrote %v, want the snapshot row among them", w)
				}

				r, found := loaded(t, store, tKey, tScope)
				if !found || !r.Done("x", st(5, 0)) || r.Len() != 1 {
					t.Errorf("next start: found %v, len %d, x done %v; want the replaced set", found, r.Len(), r.Done("x", st(5, 0)))
				}
				if r.Undecodable() != nil {
					t.Errorf("next start still sees %v: the warning would repeat", r.Undecodable())
				}
			})
		}
	}
}

// Nothing published in the first poll: the save still replaces the row, or the
// next start would warn again.
func TestSet_UndecodableRowIsReplacedEvenIfNothingIsPublished(t *testing.T) {
	store := newTestStore()
	store.put(t, tKey, tNode, undecodableStates["array"])
	s, _ := loaded(t, store, tKey, tScope)
	save(t, s)

	r, found := loaded(t, store, tKey, tScope)
	if !found || r.Undecodable() != nil {
		t.Errorf("next start: found %v, undecodable %v; want the empty replaced set", found, r.Undecodable())
	}
}

func TestSet_UndecodableChangesRowKeepsTheSnapshot(t *testing.T) {
	for shape, bad := range undecodableStates {
		t.Run(shape, func(t *testing.T) {
			store := newTestStore()
			store.put(t, tKey, tNode, goodSnapshot())
			store.put(t, tKey, tNode+changesSuffix, bad)

			s := New(store, tKey, tScope)
			found, err := s.Load(context.Background())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !found || !s.Loaded() {
				t.Fatalf("found %v, loaded %v; want the snapshot used", found, s.Loaded())
			}
			if !errors.Is(s.Undecodable(), ErrUndecodable) {
				t.Errorf("Undecodable() = %v, want ErrUndecodable", s.Undecodable())
			}
			if !s.Done("a", st(1, 0)) || !s.Done("b", st(1, 0)) || s.Len() != 2 {
				t.Errorf("snapshot not used: len %d", s.Len())
			}

			// Even with nothing published the save replaces both rows.
			store.takeWrites()
			save(t, s)
			if w := store.takeWrites(); !slices.Equal(w, []string{tNode, tNode + changesSuffix}) {
				t.Errorf("save wrote %v, want the snapshot and the changes row", w)
			}
			r, found := loaded(t, store, tKey, tScope)
			if !found || r.Len() != 2 || r.Undecodable() != nil {
				t.Errorf("next start: found %v, len %d, undecodable %v", found, r.Len(), r.Undecodable())
			}
		})
	}
}

func TestSet_LoadDecodableRowsAreUnchanged(t *testing.T) {
	store := newTestStore()
	store.put(t, tKey, tNode, goodSnapshot())
	store.put(t, tKey, tNode+changesSuffix, changesState{Scope: tScope, Base: "g1", Files: map[string]Stamp{"c": st(1, 0)}, Gone: []string{"a"}})
	s, found := loaded(t, store, tKey, tScope)
	if !found || s.Undecodable() != nil {
		t.Fatalf("found %v, undecodable %v", found, s.Undecodable())
	}
	if s.Done("a", st(1, 0)) || !s.Done("b", st(1, 0)) || !s.Done("c", st(1, 0)) {
		t.Error("snapshot and changes not applied as before")
	}
	// Nothing to save: no write.
	store.takeWrites()
	save(t, s)
	if w := store.takeWrites(); len(w) != 0 {
		t.Errorf("an unchanged, decodable set wrote %v", w)
	}
}

// A store error is not a row that can't be decoded: the Set stays unloaded and
// writes nothing, whichever row fails.
func TestSet_StoreErrorStillKeepsItUnloaded(t *testing.T) {
	cases := []struct {
		name string
		make func(t *testing.T) (checkpoint.Store, *testStore)
	}{
		{"snapshot read fails", func(t *testing.T) (checkpoint.Store, *testStore) {
			raw := newTestStore()
			raw.setFailGet(errors.New("db down"))
			return raw, raw
		}},
		{"changes read fails", func(t *testing.T) (checkpoint.Store, *testStore) {
			raw := newTestStore()
			raw.put(t, tKey, tNode, goodSnapshot())
			return &failChangesStore{testStore: raw, fail: true}, raw
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, raw := tc.make(t)
			s := New(store, tKey, tScope)
			found, err := s.Load(context.Background())
			if err == nil {
				t.Fatal("Load succeeded though the store fails")
			}
			if errors.Is(err, ErrUndecodable) {
				t.Errorf("a store error is reported as undecodable: %v", err)
			}
			if found || s.Loaded() || s.Undecodable() != nil {
				t.Errorf("found %v, loaded %v, undecodable %v; want none of them", found, s.Loaded(), s.Undecodable())
			}
			raw.takeWrites()
			s.Mark("x", st(1, 0))
			if err := s.Save(context.Background()); err != nil {
				t.Errorf("Save of an unloaded Set: %v", err)
			}
			if w := raw.takeWrites(); len(w) != 0 {
				t.Errorf("an unloaded Set wrote %v", w)
			}
		})
	}
}

// A changes row that goes bad while the Set runs (it wrote a snapshot, someone
// or something rewrote the changes row): Refresh and Save carry on, and the
// save writes a snapshot over the damage.
func TestSet_ChangesRowGoesBadWhileRunning(t *testing.T) {
	store := newTestStore()
	s, _ := loaded(t, store, tKey, tScope)
	s.Mark("a", st(1, 0))
	save(t, s) // snapshot and an empty changes row
	s.Mark("b", st(1, 0))
	save(t, s) // changes only
	if w := store.takeWrites(); w[len(w)-1] != tNode+changesSuffix {
		t.Fatalf("setup: last write %v, want a changes write", w)
	}

	store.put(t, tKey, tNode+changesSuffix, undecodableStates["array"])
	s.Observe(map[string]Stamp{"a": st(1, 0), "b": st(1, 0), "c": st(1, 0)})
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	s.Mark("c", st(1, 0))
	store.takeWrites()
	save(t, s)
	if w := store.takeWrites(); !slices.Contains(w, tNode) {
		t.Errorf("save wrote %v, want a snapshot over the damaged changes row", w)
	}
	r, found := loaded(t, store, tKey, tScope)
	if !found || r.Len() != 3 || r.Undecodable() != nil {
		t.Errorf("next start: found %v, len %d, undecodable %v", found, r.Len(), r.Undecodable())
	}
}
