package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tests for #498: a stop command acts only when it names, in its payload and
// on its subject, the tenant the watcher was started for. They reuse the fake
// database and fixture in start_race_test.go, which count status writes.

const stOther = "tenant-other"

func stStop(subjectTenant, payloadTenant string) *nats.Msg {
	data, _ := json.Marshal(map[string]string{"connection_id": raceConnID, "tenant_id": payloadTenant})
	return &nats.Msg{Subject: "vrsky.commands." + subjectTenant + ".connection.stop", Data: data}
}

// stForeignStops are stops that must change nothing for raceTenant's watcher.
var stForeignStops = []struct {
	name string
	msg  func() *nats.Msg
}{
	{"payload and subject name another tenant", func() *nats.Msg { return stStop(stOther, stOther) }},
	{"payload names the tenant, subject another", func() *nats.Msg { return stStop(stOther, raceTenant) }},
	{"payload names another tenant, subject the tenant", func() *nats.Msg { return stStop(raceTenant, stOther) }},
}

// stCountCancels wraps the active entry's Cancel so the test sees a cancel.
func (f *raceFixture) stCountCancels(t *testing.T) (*ActiveConnection, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	ac := f.c.activeConnections[raceConnID]
	if ac == nil {
		t.Fatal("watcher not active after its start")
	}
	orig := ac.Cancel
	ac.Cancel = func() { n.Add(1); orig() }
	return ac, &n
}

func TestStopTenant_ForeignStopLeavesWatcherRunning(t *testing.T) {
	for _, tc := range stForeignStops {
		t.Run(tc.name, func(t *testing.T) {
			f := newRaceFixture(t)
			f.command()
			ac, cancels := f.stCountCancels(t)

			f.c.handleStopCommand(tc.msg())

			if got := f.c.getActiveConnection(raceConnID); got != ac {
				t.Fatalf("active entry after a foreign stop = %p, want the start's %p", got, ac)
			}
			if n := cancels.Load(); n != 0 {
				t.Errorf("watcher cancelled %d times by a foreign stop, want 0", n)
			}
			if _, _, stopped := f.fdb.counts(); stopped != 0 {
				t.Errorf(`a foreign stop wrote "stopped" %d times, want 0`, stopped)
			}
			// The watcher still picks up files.
			if err := os.WriteFile(filepath.Join(f.inbox, "still.json"), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			harness.Eventually(t, raceWaitStep, "the watcher to publish a file after a foreign stop", func() bool {
				return len(f.probe.forName("still.json")) > 0
			})

			// The tenant's own stop still stops it.
			f.c.handleStopCommand(stStop(raceTenant, raceTenant))
			if f.c.getActiveConnection(raceConnID) != nil {
				t.Error("watcher still active after its tenant's stop")
			}
			if n := cancels.Load(); n != 1 {
				t.Errorf("watcher cancelled %d times by its tenant's stop, want 1", n)
			}
			if _, _, stopped := f.fdb.counts(); stopped != 1 {
				t.Errorf(`"stopped" written %d times after the tenant's stop, want 1`, stopped)
			}
		})
	}
}

// A foreign stop while the start is held in its slow part does not abandon
// the start: it activates and writes "running".
func TestStopTenant_ForeignStopMidStartDoesNotAbandon(t *testing.T) {
	for _, tc := range stForeignStops {
		t.Run(tc.name, func(t *testing.T) {
			f := newRaceFixture(t)
			entered, release := f.holdFirstStart()

			done := make(chan struct{})
			go func() { defer close(done); f.command() }()
			waitDone(t, entered, "start reaching the start window")
			f.c.handleStopCommand(tc.msg())
			close(release)
			waitDone(t, done, "start")

			if f.c.getActiveConnection(raceConnID) == nil {
				t.Fatal("start abandoned by a foreign stop")
			}
			if _, running, stopped := f.fdb.counts(); running != 1 || stopped != 0 {
				t.Errorf(`status writes: running %d, stopped %d; want 1 and 0`, running, stopped)
			}
		})
	}
}
