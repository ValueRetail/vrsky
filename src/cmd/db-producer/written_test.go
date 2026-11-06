package main

import (
	"testing"
	"time"
)

func TestWrittenSet(t *testing.T) {
	k := func(env, node string) writtenKey {
		return writtenKey{connID: "c", envelopeID: env, nodeID: node, host: "h", port: 5432, database: "d", table: "t"}
	}
	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		run  func(w *writtenSet) (got bool, size int)
		want bool
		size int
	}{
		{"unknown target", func(w *writtenSet) (bool, int) {
			return w.has(k("e1", "a"), t0), w.size()
		}, false, 0},
		{"added target", func(w *writtenSet) (bool, int) {
			w.add(t0, k("e1", "a"))
			return w.has(k("e1", "a"), t0.Add(time.Minute)), w.size()
		}, true, 1},
		{"same node, other message", func(w *writtenSet) (bool, int) {
			w.add(t0, k("e1", "a"))
			return w.has(k("e2", "a"), t0), w.size()
		}, false, 1},
		{"same message, node pointed elsewhere", func(w *writtenSet) (bool, int) {
			w.add(t0, k("e1", "a"))
			moved := k("e1", "a")
			moved.table = "other"
			return w.has(moved, t0), w.size()
		}, false, 1},
		{"expired entry", func(w *writtenSet) (bool, int) {
			w.add(t0, k("e1", "a"))
			return w.has(k("e1", "a"), t0.Add(time.Hour)), w.size()
		}, false, 0},
		{"forgotten entry", func(w *writtenSet) (bool, int) {
			w.add(t0, k("e1", "a"), k("e1", "b"))
			w.forget(k("e1", "a"), k("e1", "b"))
			return w.has(k("e1", "a"), t0), w.size()
		}, false, 0},
		{"full set drops the oldest", func(w *writtenSet) (bool, int) {
			w.add(t0, k("e1", "a"))
			w.add(t0.Add(time.Second), k("e2", "a"))
			w.add(t0.Add(2*time.Second), k("e3", "a"))
			w.add(t0.Add(3*time.Second), k("e4", "a"))
			return w.has(k("e1", "a"), t0.Add(4*time.Second)), w.size()
		}, false, 3},
		{"full set keeps the newest", func(w *writtenSet) (bool, int) {
			w.add(t0, k("e1", "a"))
			w.add(t0.Add(time.Second), k("e2", "a"))
			w.add(t0.Add(2*time.Second), k("e3", "a"))
			w.add(t0.Add(3*time.Second), k("e4", "a"))
			return w.has(k("e4", "a"), t0.Add(4*time.Second)), w.size()
		}, true, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWrittenSet(30*time.Minute, 3)
			got, size := tt.run(w)
			if got != tt.want {
				t.Errorf("has = %v, want %v", got, tt.want)
			}
			if size != tt.size {
				t.Errorf("size = %d, want %d", size, tt.size)
			}
		})
	}
}
