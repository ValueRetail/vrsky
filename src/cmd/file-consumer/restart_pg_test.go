package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// #391 against a real database: the connection lookup, the status writes and
// the saved done set all go through Postgres (checkpoint.PostgresStore, the
// store Configure wires up). Skipped without VRSKY_TEST_POSTGRES_URL; CI sets
// it. The fake-store tests in restart_test.go cover the acceptance criteria on
// their own.

type pgRestartEnv struct {
	t   *testing.T
	ctx context.Context
	db  *sql.DB
}

func newPGRestartEnv(t *testing.T) *pgRestartEnv {
	t.Helper()
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &pgRestartEnv{t: t, ctx: context.Background(), db: db}
}

func (e *pgRestartEnv) tenant() string {
	e.t.Helper()
	var owner, tenant string
	if err := e.db.QueryRowContext(e.ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('restart-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		e.t.Fatal(err)
	}
	if err := e.db.QueryRowContext(e.ctx, `INSERT INTO tenants (name, slug, owner_id)
		VALUES ('restart', 'restart-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		e.t.Fatal(err)
	}
	return tenant
}

func (e *pgRestartEnv) connection(id, tenant, path string) {
	e.t.Helper()
	if _, err := e.db.ExecContext(e.ctx, `INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
		VALUES ($1, $2, $3, 'running', $4::jsonb, '[]'::jsonb)`, id, tenant, "file-"+id, nodesWithPath(path)); err != nil {
		e.t.Fatal(err)
	}
}

// savedRows returns the tenant ids of the checkpoint rows for a connection.
func (e *pgRestartEnv) savedRows(connID string) []string {
	e.t.Helper()
	rows, err := e.db.QueryContext(e.ctx,
		`SELECT tenant_id FROM connection_node_checkpoints WHERE connection_id = $1`, connID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, tenant)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *pgRestartEnv) rig(base, connID, tenant, path string) *restartRig {
	r := newRestartRig(e.t, base, connID, tenant, checkpoint.NewPostgresStore(e.db))
	r.db = e.db
	r.path = path
	return r
}

// AC 1 to 3 end to end on Postgres, for two tenants sharing the database, the
// node id and the base directory, plus the cascade when a connection is
// deleted.
func TestRestartPG_DoneSetSurvivesRestartPerTenant(t *testing.T) {
	for _, mode := range []shutdownMode{viaStopCommand, viaPodShutdown} {
		t.Run(string(mode), func(t *testing.T) {
			e := newPGRestartEnv(t)
			base := t.TempDir()
			tenantA, tenantB := e.tenant(), e.tenant()
			connA, connB := uuid.New().String(), uuid.New().String()
			e.connection(connA, tenantA, "inbox")
			e.connection(connB, tenantB, "inbox")
			a := e.rig(base, connA, tenantA, "inbox")
			b := e.rig(base, connB, tenantB, "inbox")

			// A: first start next to an existing file, publishes p, then x
			// arrives and the connection stops before x is stable.
			old := []byte(`{"old":true}`)
			p := []byte(`{"p":"a"}`)
			x := []byte(`{"x":"a"}`)
			a.write("old.json", old)
			a.boot()
			a.polls(3)
			a.requireTotal("A first start", 0)
			a.write("p.json", p)
			a.polls(2)
			a.requireOnce("A publishes p", "p.json", p)
			a.write("x.json", x)
			a.poll()
			a.requireTotal("x sighted before the stop", 1)
			a.shutdown(mode)

			// The snapshot and its changes row (#426), both tenant A's.
			if got := e.savedRows(connA); len(got) != 2 || got[0] != tenantA || got[1] != tenantA {
				t.Fatalf("saved rows for A = %v, want two rows for tenant %s", got, tenantA)
			}
			store := checkpoint.NewPostgresStore(e.db)
			for _, node := range []string{"c1", "c1" + changesSuffix} {
				if cp, err := store.Get(e.ctx, tenantB, connA, node); err != nil || cp != nil {
					t.Fatalf("A's state (%s) readable as tenant B: %+v, %v", node, cp, err)
				}
			}

			// B: first start with p, x and y already in its folder. A's state
			// must not leak in (that would publish y and x).
			b.write("p.json", []byte(`{"p":"b"}`))
			b.write("x.json", []byte(`{"x":"b"}`))
			b.write("y.json", []byte(`{"y":"b"}`))
			b.boot()
			b.polls(5)
			b.requireTotal("B first start", 0)
			z := []byte(`{"z":"b"}`)
			b.write("z.json", z)
			b.polls(2)
			b.requireOnce("B publishes z", "z.json", z)
			b.shutdown(mode)

			// A restarts: x once, p not again, old never.
			a.boot()
			a.polls(5)
			a.requireOnce("A after the restart", "x.json", x)
			a.requireOnce("A after the restart", "p.json", p)
			a.requireCount("A after the restart", "old.json", 0)
			a.requireTotal("A after the restart", 2)
			a.shutdown(mode)

			// Deleting the connection deletes its saved state, and only its.
			if _, err := e.db.ExecContext(e.ctx, `DELETE FROM connections WHERE id = $1`, connA); err != nil {
				t.Fatal(err)
			}
			if got := e.savedRows(connA); len(got) != 0 {
				t.Fatalf("saved rows for A after deleting the connection = %v, want none", got)
			}
			if got := e.savedRows(connB); len(got) != 2 || got[0] != tenantB || got[1] != tenantB {
				t.Fatalf("saved rows for B after deleting A = %v, want two rows for tenant %s", got, tenantB)
			}

			// B still has its own state.
			b.boot()
			b.polls(4)
			b.requireOnce("B after its restart", "z.json", z)
			b.requireTotal("B after its restart", 1)
		})
	}
}
