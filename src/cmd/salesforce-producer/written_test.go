package main

import (
	"testing"
	"time"
)

func TestWrittenSet(t *testing.T) {
	k := func(i int) writtenKey { return writtenKey{connID: "c", envelopeID: "e", nodeID: "n", record: i} }
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		run  func(w *writtenSet) (got, want bool)
	}{
		{"added is had", func(w *writtenSet) (bool, bool) {
			w.add(t0, k(0))
			return w.has(k(0), t0.Add(time.Minute)), true
		}},
		{"other record is not had", func(w *writtenSet) (bool, bool) {
			w.add(t0, k(0))
			return w.has(k(1), t0), false
		}},
		{"other envelope is not had", func(w *writtenSet) (bool, bool) {
			w.add(t0, k(0))
			other := k(0)
			other.envelopeID = "e2"
			return w.has(other, t0), false
		}},
		{"expires after the TTL", func(w *writtenSet) (bool, bool) {
			w.add(t0, k(0))
			return w.has(k(0), t0.Add(time.Hour)), false
		}},
		{"forgotten", func(w *writtenSet) (bool, bool) {
			w.add(t0, k(0), k(1))
			w.forget(k(0))
			return w.has(k(0), t0) || !w.has(k(1), t0), false
		}},
		{"full set drops the oldest", func(w *writtenSet) (bool, bool) {
			w.add(t0, k(0))
			w.add(t0.Add(time.Second), k(1), k(2))
			w.add(t0.Add(2*time.Second), k(3))
			return w.size() == 3 && !w.has(k(0), t0.Add(3*time.Second)) && w.has(k(3), t0.Add(3*time.Second)), true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWrittenSet(time.Hour, 3)
			if got, want := tc.run(w); got != want {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}
