package main

import (
	"database/sql"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Tenant isolation in the status order, against a real database: a stop
// command for connection X that names tenant B, while X runs for its own
// tenant A, must not move A's status order. Its UPDATE filters on tenant B
// and matches no row; A's "running" written after it still lands.
//
// Pre-existing, and not changed here: the stop handler cancels by connection
// ID, so the wrong-tenant stop does stop A's watcher. The tests assert that
// as it is today. Uses the env and consumer from start_status_pg_test.go;
// skipped without VRSKY_TEST_POSTGRES_URL.

const statusUpdatedMsg = "Updated connection status"

// stoppedAtSet reports whether the row's stopped_at is set: a "stopped"
// UPDATE that matched the row sets it.
func (e *statusPGEnv) stoppedAtSet(id string) bool {
	e.t.Helper()
	var at sql.NullTime
	if err := e.db.QueryRowContext(e.ctx, `SELECT stopped_at FROM connections WHERE id = $1`, id).Scan(&at); err != nil {
		e.t.Fatal(err)
	}
	return at.Valid
}

func TestStartTenantPG_WrongTenantStop(t *testing.T) {
	env := newStatusPGEnv(t)
	tenantA, tenantB := env.tenant(), env.tenant()

	t.Run("wrong-tenant stop inside A's start window", func(t *testing.T) {
		e := env.with(t)
		id := e.connection(tenantA, "stopped")
		sc := e.consumer()

		// Between A's Activate and A's "running" write.
		sc.afterActivate(id, func() { sc.stop(id, tenantB) })
		sc.start(id, tenantA)

		// A's order is A's own: its "running" lands although the wrong
		// tenant's stop took a number and wrote first.
		if st, started := e.status(id); st != "running" || !started {
			t.Errorf("status = %q, started_at set = %v; want A's running", st, started)
		}
		// The wrong tenant's "stopped" matched no row.
		if e.stoppedAtSet(id) {
			t.Error("stopped_at set: the wrong tenant's stop UPDATE matched A's row")
		}
		// Both writes ran: the wrong tenant's stop (no row) and A's running.
		if n := sc.logs.count(statusUpdatedMsg, id); n != 2 {
			t.Errorf("status UPDATEs run = %d, want 2 (wrong tenant's stopped, then A's running)", n)
		}
		// Pre-existing: the stop cancelled A's watcher by connection ID.
		harness.Eventually(t, raceWaitStep, "A's watcher, cancelled by the stop, to end", func() bool {
			_, live := sc.logs.watchers(id)
			return live == 0
		})
		if sc.c.getActiveConnection(id) != nil {
			t.Error("connection still active after the stop (today's stop cancels by ID)")
		}
	})

	t.Run("new start for A after a wrong-tenant stop", func(t *testing.T) {
		e := env.with(t)
		id := e.connection(tenantA, "stopped")
		sc := e.consumer()

		sc.start(id, tenantA)
		e.requireStatus(id, "running")
		sc.requireWatchers(id, 1, 1)

		sc.stop(id, tenantB)
		// The UPDATE named tenant B: A's row is untouched.
		e.requireStatus(id, "running")
		if e.stoppedAtSet(id) {
			t.Error("stopped_at set: the wrong tenant's stop UPDATE matched A's row")
		}
		// Pre-existing: A's watcher was cancelled by connection ID.
		sc.requireWatchers(id, 1, 0)

		// A starts X again; its "running" lands and stays, with one watcher.
		sc.start(id, tenantA)
		e.requireStatus(id, "running")
		if e.stoppedAtSet(id) {
			t.Error("stopped_at set after A's new start")
		}
		sc.requireWatchers(id, 2, 1)
		if n := sc.logs.count(statusUpdatedMsg, id); n != 3 {
			t.Errorf("status UPDATEs run = %d, want 3 (A's running, wrong tenant's stopped, A's running)", n)
		}
	})
}
