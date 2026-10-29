package main

import (
	"database/sql"
	"testing"
)

// Tenant isolation, against a real database: a stop command for connection X
// that names tenant B, while X starts or runs for its own tenant A, changes
// nothing (#498). It cancels no watcher and writes no status, so A's
// "running" lands and stays, and A's own stop still stops X.
//
// Before #498 the handler cancelled by connection ID, and these tests pinned
// that: the wrong tenant's stop ended A's watcher and its UPDATE (filtered on
// tenant B) matched no row. Uses the env and consumer from
// start_status_pg_test.go; skipped without VRSKY_TEST_POSTGRES_URL.

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

		// A's "running" lands.
		if st, started := e.status(id); st != "running" || !started {
			t.Errorf("status = %q, started_at set = %v; want A's running", st, started)
		}
		if e.stoppedAtSet(id) {
			t.Error("stopped_at set: the wrong tenant's stop UPDATE matched A's row")
		}
		// Only A's running was written: the wrong tenant's stop wrote nothing.
		if n := sc.logs.count(statusUpdatedMsg, id); n != 1 {
			t.Errorf("status UPDATEs run = %d, want 1 (A's running only)", n)
		}
		// The wrong tenant's stop left A's watcher running.
		sc.requireWatchers(id, 1, 1)

		// A's own stop still stops it.
		sc.stop(id, tenantA)
		e.requireStatus(id, "stopped")
		sc.requireWatchers(id, 1, 0)
	})

	t.Run("A keeps running after a wrong-tenant stop", func(t *testing.T) {
		e := env.with(t)
		id := e.connection(tenantA, "stopped")
		sc := e.consumer()

		sc.start(id, tenantA)
		e.requireStatus(id, "running")
		sc.requireWatchers(id, 1, 1)

		sc.stop(id, tenantB)
		// Nothing changed: A's row, A's watcher, no UPDATE run.
		e.requireStatus(id, "running")
		if e.stoppedAtSet(id) {
			t.Error("stopped_at set: the wrong tenant's stop UPDATE matched A's row")
		}
		sc.requireWatchers(id, 1, 1)
		if n := sc.logs.count(statusUpdatedMsg, id); n != 1 {
			t.Errorf("status UPDATEs run = %d, want 1 (A's running only)", n)
		}

		// A start for A finds it active and starts no second watcher.
		sc.start(id, tenantA)
		e.requireStatus(id, "running")
		sc.requireWatchers(id, 1, 1)

		// A's own stop still stops it.
		sc.stop(id, tenantA)
		e.requireStatus(id, "stopped")
		if !e.stoppedAtSet(id) {
			t.Error("stopped_at not set after A's own stop")
		}
		sc.requireWatchers(id, 1, 0)
	})
}
