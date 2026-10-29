package doneset

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// The done set through connection_node_checkpoints on a real Postgres
// (checkpoint.PostgresStore, what both consumers use). Skipped without
// VRSKY_TEST_POSTGRES_URL.

func pgDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// pgConnection creates a tenant and a connection in it (the checkpoint rows
// reference both) and returns their ids.
func pgConnection(t *testing.T, db *sql.DB) (tenant, conn string) {
	t.Helper()
	ctx := context.Background()
	var owner string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('doneset-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO tenants (name, slug, owner_id)
		VALUES ('doneset', 'doneset-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	conn = uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
		VALUES ($1, $2, $3, 'running', '[]'::jsonb, '[]'::jsonb)`, conn, tenant, "doneset-"+conn); err != nil {
		t.Fatal(err)
	}
	return tenant, conn
}

func pgRowTenants(t *testing.T, db *sql.DB, conn string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT node_id, tenant_id FROM connection_node_checkpoints WHERE connection_id = $1`, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var node, tenant string
		if err := rows.Scan(&node, &tenant); err != nil {
			t.Fatal(err)
		}
		out[node] = tenant
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSetPG_RestartAndTenants(t *testing.T) {
	db := pgDB(t)
	store := checkpoint.NewPostgresStore(db)
	tenantA, connA := pgConnection(t, db)
	tenantB, connB := pgConnection(t, db)
	keyA := Key{TenantID: tenantA, ConnectionID: connA, NodeID: tNode}
	keyB := Key{TenantID: tenantB, ConnectionID: connB, NodeID: tNode}

	// A: first start, publishes ten names, saves (snapshot), then one more
	// (changes row only).
	a, found := loaded(t, store, keyA, tScope)
	if found {
		t.Fatal("A found a saved set on its first start")
	}
	listing := map[string]Stamp{}
	for _, n := range names(10, "f") {
		listing[n] = st(1, 0)
		a.Mark(n, st(1, 0))
	}
	a.Observe(listing)
	save(t, a)
	listing["late"] = st(3, 2)
	a.Observe(listing)
	a.Mark("late", st(3, 2))
	save(t, a)

	if got := pgRowTenants(t, db, connA); len(got) != 2 || got[tNode] != tenantA || got[tNode+changesSuffix] != tenantA {
		t.Fatalf("rows for A = %v, want the snapshot and changes rows, both tenant A's", got)
	}

	// A restarts: everything is done, with its stamp.
	ra, found := loaded(t, store, keyA, tScope)
	if !found || ra.Len() != 11 || !ra.Done("late", st(3, 2)) || !ra.Done("f0003", st(1, 0)) {
		t.Fatalf("A after the restart: found %v, len %d", found, ra.Len())
	}
	if ra.Done("late", st(3, 3)) {
		t.Error("A after the restart: late counts as done with another stamp")
	}

	// A's rows are not readable under tenant B, not even with A's connection id.
	for _, k := range []Key{
		{TenantID: tenantB, ConnectionID: connA, NodeID: tNode},
		keyB,
	} {
		other, found := loaded(t, store, k, tScope)
		if found || other.Len() != 0 {
			t.Errorf("key %+v sees A's saved set", k)
		}
	}

	// B saves its own; A's is untouched.
	b, _ := loaded(t, store, keyB, tScope)
	b.Mark("f0000", st(9, 9))
	save(t, b)
	if r, _ := loaded(t, store, keyA, tScope); r.Len() != 11 || !r.Done("f0000", st(1, 0)) {
		t.Errorf("A's set after B saved: len %d", r.Len())
	}
	if r, _ := loaded(t, store, keyB, tScope); r.Len() != 1 || !r.Done("f0000", st(9, 9)) {
		t.Errorf("B's set: len %d", r.Len())
	}

	// Deleting A's connection deletes its rows, and only those.
	if _, err := db.Exec(`DELETE FROM connections WHERE id = $1`, connA); err != nil {
		t.Fatal(err)
	}
	if got := pgRowTenants(t, db, connA); len(got) != 0 {
		t.Errorf("rows for A after deleting its connection = %v", got)
	}
	if got := pgRowTenants(t, db, connB); len(got) != 2 {
		t.Errorf("rows for B after deleting A = %v, want 2", got)
	}
}
