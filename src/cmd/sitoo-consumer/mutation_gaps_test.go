package main

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Tests for gaps the #472 mutation run found: behaviour the other tests
// passed with or without.

// storedState decodes the checkpoint state for the test node.
func storedState(t *testing.T, store checkpoint.Store) (int64, incrementalState) {
	t.Helper()
	cp, err := store.Get(context.Background(), incTenant, incConn, incNode)
	if err != nil || cp == nil {
		t.Fatalf("checkpoint get: %v, %v", cp, err)
	}
	w, _ := strconv.ParseInt(cp.LastProcessedMessageID, 10, 64)
	var st incrementalState
	if err := json.Unmarshal(cp.State, &st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return w, st
}

// After raising cursor_overlap, a poll that saves must keep the stored floor:
// dropping it to W − new overlap would make the poll after that re-send the
// orders between the old window and the new one.
func TestGap_RaisedOverlapKeepsTheFloorAcrossSaves(t *testing.T) {
	for _, fm := range filterModes {
		t.Run(fm.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(1, 3000)...)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			poll(t, c, incTenant, incConn, incConfig(srv.URL, ov(1000), 1000))
			published(t, got, mu, incTenant, incConn)
			raised := incConfig(srv.URL, ov(2000), 1000)
			f.add(ords(3001)...)
			poll(t, c, incTenant, incConn, raised) // saves
			assertOnce(t, "poll with a new order", published(t, got, mu, incTenant, incConn), v1(3001)...)
			if _, st := storedState(t, store); st.Floor < 2000 {
				t.Errorf("floor after the save = %d, want it kept at 2000 (3000 − the old overlap)", st.Floor)
			}
			poll(t, c, incTenant, incConn, raised)
			assertOnce(t, "next poll", published(t, got, mu, incTenant, incConn))
		})
	}
}

// The stored set stays bounded by the overlap: everything in it is at or
// above the floor, and the floor follows the watermark.
func TestGap_StoredSetStaysWithinTheWindow(t *testing.T) {
	const overlap = 10
	f, srv := newFakeSitoo(t, false, ords(idRange(1, 50)...)...)
	store := checkpoint.NewInMemoryStore()
	c, _, _ := newIncConsumer(store)
	cfg := incConfig(srv.URL, ov(overlap), 7)
	for next := int64(51); next <= 200; next += 30 {
		poll(t, c, incTenant, incConn, cfg)
		w, st := storedState(t, store)
		if st.Floor != w-overlap {
			t.Errorf("watermark %d: floor = %d, want %d", w, st.Floor, w-overlap)
		}
		for _, id := range st.Sent {
			if id < st.Floor {
				t.Errorf("watermark %d: stored id %d is below the floor %d", w, id, st.Floor)
			}
		}
		if len(st.Sent) > overlap+1 {
			t.Errorf("watermark %d: %d stored ids, want at most %d", w, len(st.Sent), overlap+1)
		}
		f.add(ords(idRange(next, next+29)...)...)
	}
}

// A poll carrying on from Next that reads to the end must clear Next, or every
// later poll would start there and never re-read the overlap again.
func TestGap_NextIsClearedAtTheEnd(t *testing.T) {
	const n, overlap = 600, 300
	for _, fm := range filterModes {
		t.Run(fm.name, func(t *testing.T) {
			f, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(1, n)...)...)
			store := checkpoint.NewInMemoryStore()
			c, _, _ := newIncConsumer(store)
			cfg := incConfig(srv.URL, ov(overlap), 2)
			want := fromFor(n, ov(overlap))
			sweeps := 0
			for p := 1; p <= 40 && sweeps < 2; p++ {
				poll(t, c, incTenant, incConn, cfg)
				reqs := f.take()
				if w, _ := storedState(t, store); w == n && reqs[0].q.Get("orderidfrom") == want {
					sweeps++
				}
			}
			if sweeps < 2 {
				t.Errorf("polls stopped re-reading the overlap from %s once everything was read", want)
			}
		})
	}
}

// countingStore counts saves.
type countingStore struct {
	checkpoint.Store
	mu    sync.Mutex
	saves int
}

func (s *countingStore) Save(ctx context.Context, cp *checkpoint.Checkpoint) error {
	s.mu.Lock()
	s.saves++
	s.mu.Unlock()
	return s.Store.Save(ctx, cp)
}

// A poll that finds nothing new writes no checkpoint.
func TestGap_UnchangedPollDoesNotSave(t *testing.T) {
	_, srv := newFakeSitoo(t, false, ords(idRange(1, 2500)...)...)
	store := &countingStore{Store: checkpoint.NewInMemoryStore()}
	c, _, _ := newIncConsumer(store)
	cfg := incConfig(srv.URL, nil, 1000)
	poll(t, c, incTenant, incConn, cfg)
	store.mu.Lock()
	store.saves = 0
	store.mu.Unlock()
	poll(t, c, incTenant, incConn, cfg)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saves != 0 {
		t.Errorf("an unchanged poll saved the checkpoint %d times, want 0", store.saves)
	}
}

// The start warns when re-reading the overlap takes more than one poll.
func TestGap_OverlapWiderThanAPoll(t *testing.T) {
	cases := []struct {
		pageSize int
		overlap  *int64
		want     bool
	}{
		{0, nil, false},          // 1000 ids against 99 900 a poll
		{2, nil, true},           // 1000 against 100
		{11, nil, false},         // 1000 against 1000
		{10, nil, true},          // 1000 against 900
		{1000, ov(99900), false}, // exactly a poll
		{1000, ov(99901), true},
	}
	for _, tc := range cases {
		cfg := &SitooConfig{Resource: "orders", Incremental: true, PageSize: tc.pageSize, CursorOverlap: tc.overlap}
		if got := cfg.overlapWiderThanAPoll(); got != tc.want {
			t.Errorf("page size %d, overlap %d: wider = %v, want %v", tc.pageSize, cfg.overlap(), got, tc.want)
		}
	}
}
