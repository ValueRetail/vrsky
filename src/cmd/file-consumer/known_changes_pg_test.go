package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// #426 against Postgres (AC 8): the snapshot and changes rows in
// connection_node_checkpoints, a restart after a stop and after a crash-like
// end of the old pod, and the previous image reading the snapshot row as it is
// in the table. Skipped without VRSKY_TEST_POSTGRES_URL.

// tableRow reads one checkpoint row's state straight from the table, nil if
// there is none.
func (e *pgRestartEnv) tableRow(tenant, connID, node string) []byte {
	e.t.Helper()
	var state []byte
	err := e.db.QueryRowContext(e.ctx, `SELECT state FROM connection_node_checkpoints
		WHERE tenant_id = $1 AND connection_id = $2 AND node_id = $3`, tenant, connID, node).Scan(&state)
	if err != nil {
		e.t.Fatalf("read %s from the table: %v", node, err)
	}
	return state
}

func TestKnownChangesPG_RestartAndRollbackRead(t *testing.T) {
	for _, mode := range []string{"stop", "crash-like"} {
		t.Run(mode, func(t *testing.T) {
			e := newPGRestartEnv(t)
			tenant := e.tenant()
			connID := uuid.New().String()
			e.connection(connID, tenant, "inbox")
			base := t.TempDir()
			a := e.rig(base, connID, tenant, "inbox")
			writeBaseline(t, a.dir(), 30)
			a.boot()
			a.polls(2)
			a.requireTotal("first start", 0)

			for i := 0; i < 3; i++ {
				writeAt(t, a.dir(), fmt.Sprintf("n%d.json", i), []byte(`{"n":1}`), changesMTime.Add(time.Duration(i+1)*time.Minute))
				a.polls(2)
			}
			v2 := []byte("replaced\n")
			writeAt(t, a.dir(), baseName(2), v2, changesMTime.Add(time.Hour))
			a.polls(2)
			a.remove(baseName(4))
			a.poll()
			u := []byte(`{"u":"unpublished at the stop"}`)
			writeAt(t, a.dir(), "u.json", u, changesMTime.Add(2*time.Hour))
			a.poll()
			a.requireTotal("before the stop", 4)

			snap := decodeSnapshot(t, e.tableRow(tenant, connID, "c1"))
			ch := decodeChanges(t, e.tableRow(tenant, connID, "c1"+changesSuffix))
			if snap.Gen == "" || ch.Base != snap.Gen || ch.Files["n2.json"] == (wireStamp{}) || len(ch.Gone) != 1 {
				t.Fatalf("premise: snapshot gen %q, changes %+v; want n2 and a gone name in a changes row on it", snap.Gen, ch)
			}
			ac := a.c.getActiveConnection(connID)
			live := make(map[string]bool, len(ac.knownFiles))
			liveStamps := make(map[string]fileStamp, len(ac.knownStamps))
			for k := range ac.knownFiles {
				live[k] = true
			}
			for k, v := range ac.knownStamps {
				liveStamps[k] = v
			}

			next := a
			switch mode {
			case "stop":
				a.shutdown(viaStopCommand)
				// The previous image reads the row as it is in the table.
				names, stamps := prevImageLoad(t, e.tableRow(tenant, connID, "c1"), a.dir())
				assertSameSet(t, "previous image's set after the stop", names, live)
				for k, v := range liveStamps {
					if stamps[k] != v {
						t.Fatalf("previous image: stamp of %s = %+v, want %+v", k, stamps[k], v)
					}
				}
				if got := prevImageWouldPublish(t, a.dir(), names, stamps); len(got) != 1 || got[0] != "u.json" {
					t.Fatalf("previous image would publish %v, want only u.json", got)
				}
				a.boot()
			case "crash-like":
				// A new pod on the same database while the old one is still
				// there but does nothing more: no stop, no last save.
				next = e.rig(base, connID, tenant, "inbox")
				next.probe = a.probe
				next.boot()
			}
			next.polls(3)
			next.requireOnce("after the restart", "u.json", u)
			for i := 0; i < 3; i++ {
				next.requireOnce("after the restart", fmt.Sprintf("n%d.json", i), []byte(`{"n":1}`))
			}
			next.requireOnce("after the restart", baseName(2), v2)
			next.requireTotal("after the restart", 5)

			if mode == "crash-like" {
				// Only now let the old pod's goroutine go. It crashed, so it
				// must not write on its way out: cut it off from the store
				// first (the cancel orders this write before its stop).
				old := a.c.getActiveConnection(connID)
				a.c.checkpoints = nil
				old.Cancel()
				a.waitWatcherExit()
				a.c = nil
			}
			next.shutdown(viaStopCommand)
			next.boot()
			next.polls(3)
			next.requireTotal("after another restart", 5)
			next.shutdown(viaStopCommand)
		})
	}
}
