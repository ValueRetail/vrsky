package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Tests for #617: a done-set row that is there but can't be decoded counts as
// no saved set, so the poller follows its first-start rule (publishes the
// folder) instead of publishing nothing for good. The row is replaced at the
// next save, and the warning is logged once with the connection id.

// pdPlant overwrites a checkpoint row with state, as a hand edit or another
// version would have left it.
func pdPlant(t *testing.T, store *pdStore, nodeID, state string) {
	t.Helper()
	if err := store.Save(context.Background(), &checkpoint.Checkpoint{
		TenantID: pdTenant, ConnectionID: pdConn, NodeID: nodeID, State: json.RawMessage(state),
	}); err != nil {
		t.Fatal(err)
	}
}

// pdWarnLogger replaces the rig's logger with one that keeps its output, and
// carries the connection id the way runPoller's does.
func pdWarnLogger(r *pdRig) *bytes.Buffer {
	var buf bytes.Buffer
	r.c.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).With("connection_id", r.conn)
	return &buf
}

func TestDonePersist_UndecodableRow(t *testing.T) {
	cases := []struct {
		name string
		// plant damages the rows the first lifetime left (a and b done in the
		// snapshot, c published after it, so in the changes row).
		plant func(t *testing.T, store *pdStore)
		// want is the total published after the damaged start: a, b and c
		// once from the first lifetime plus what the start publishes again.
		want map[string]int
	}{
		{
			name:  "snapshot is not an object",
			plant: func(t *testing.T, s *pdStore) { pdPlant(t, s, "c1", `["a","b"]`) },
			// No saved set: the first-start rule publishes all three again.
			want: map[string]int{"a.json": 2, "b.json": 2, "c.json": 2},
		},
		{
			name:  "snapshot field has the wrong type",
			plant: func(t *testing.T, s *pdStore) { pdPlant(t, s, "c1", `{"scope":"x","files":"nope"}`) },
			want:  map[string]int{"a.json": 2, "b.json": 2, "c.json": 2},
		},
		{
			name:  "changes row is not an object",
			plant: func(t *testing.T, s *pdStore) { pdPlant(t, s, "c1#changes", `"nope"`) },
			// The snapshot (a, b) is used; only c, which sat in the damaged
			// changes row, is published once more.
			want: map[string]int{"a.json": 1, "b.json": 1, "c.json": 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newPDStore()
			fake := &fakeSFTP{}
			pdPut(fake, "a.json", `{"a":1}`, 0)
			pdPut(fake, "b.json", `{"b":1}`, 0)
			r := newPDRig(t, store, fake)
			r.boot()
			r.polls(2)
			pdPut(fake, "c.json", `{"c":1}`, 1)
			r.polls(2)
			requireCounts(t, r.log, "before the damage", map[string]int{"a.json": 1, "b.json": 1, "c.json": 1})
			tc.plant(t, store)

			// The pod starts over the damaged row and keeps polling.
			r.boot()
			logs := pdWarnLogger(r)
			r.polls(4)
			requireCounts(t, r.log, "after the damaged start", tc.want)
			if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
				t.Errorf("%d warnings over four polls, want 1:\n%s", n, logs)
			}
			if !strings.Contains(logs.String(), "can't be decoded") || !strings.Contains(logs.String(), "connection_id="+pdConn) {
				t.Errorf("the warning does not name the problem and the connection:\n%s", logs)
			}
			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("an error was logged:\n%s", logs)
			}
			if !r.done.Loaded() {
				t.Error("done set never loaded")
			}

			// The save replaced the row: the next start resumes from it and
			// publishes nothing, with no warning.
			r.boot()
			logs = pdWarnLogger(r)
			r.polls(2)
			requireCounts(t, r.log, "after the next start", tc.want)
			if strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("the next start warned again:\n%s", logs)
			}
		})
	}
}

// A store error is not a damaged row: nothing is published, an error is
// logged at every poll, and the damaged-row handling is not entered.
func TestDonePersist_StoreErrorStillPublishesNothing(t *testing.T) {
	store := newPDStore()
	pdPlant(t, store, "c1", `["a"]`)
	store.setFailGet(true)
	fake := &fakeSFTP{}
	pdPut(fake, "a.json", `{"a":1}`, 0)
	r := newPDRig(t, store, fake)
	r.boot()
	logs := pdWarnLogger(r)
	r.polls(3)
	requireCounts(t, r.log, "while reads fail", map[string]int{})
	if r.done.Loaded() {
		t.Error("done set counts as loaded while reads fail")
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 3 {
		t.Errorf("%d errors over three polls, want 3:\n%s", n, logs)
	}

	store.setFailGet(false)
	r.polls(2)
	requireCounts(t, r.log, "once reads work", map[string]int{"a.json": 1})
}
