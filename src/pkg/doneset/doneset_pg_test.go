package doneset

import (
	"context"
	"database/sql"
	"errors"
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

// A snapshot or changes row of the wrong shape in a real jsonb column (#617):
// the Set loads as if nothing were saved (or, for the changes row, as if the
// snapshot had no changes), the first save replaces the row, and the next
// start finds it. A store failure is still an error and leaves the Set
// unloaded.
func TestSetPG_UndecodableRowIsReplaced(t *testing.T) {
	cases := []struct {
		name      string
		snapshot  string // jsonb for the snapshot row; "" keeps the good one
		changes   string // jsonb for the changes row; "" keeps the good one
		wantFound bool
		wantLen   int // names loaded
	}{
		{name: "snapshot is an array", snapshot: `["a","b"]`, wantFound: false, wantLen: 0},
		{name: "snapshot has a wrong field type", snapshot: `{"scope":"sftp://u@host:22/in","files":"x"}`, wantFound: false, wantLen: 0},
		{name: "changes row is a string", changes: `"x"`, wantFound: true, wantLen: 2},
		{name: "changes row has a wrong field type", changes: `{"scope":"sftp://u@host:22/in","base":7}`, wantFound: true, wantLen: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := pgDB(t)
			store := checkpoint.NewPostgresStore(db)
			tenant, conn := pgConnection(t, db)
			key := Key{TenantID: tenant, ConnectionID: conn, NodeID: tNode}

			// A good set, as an earlier run left it: two names in the snapshot.
			good, _ := loaded(t, store, key, tScope)
			good.Mark("a", st(1, 0))
			good.Mark("b", st(1, 0))
			save(t, good)

			for node, bad := range map[string]string{tNode: tc.snapshot, tNode + changesSuffix: tc.changes} {
				if bad == "" {
					continue
				}
				res, err := db.Exec(`UPDATE connection_node_checkpoints SET state = $1::jsonb
					WHERE connection_id = $2 AND node_id = $3`, bad, conn, node)
				if err != nil {
					t.Fatal(err)
				}
				if n, _ := res.RowsAffected(); n != 1 {
					t.Fatalf("planted the bad row for %s in %d rows, want 1", node, n)
				}
			}

			s := New(store, key, tScope)
			found, err := s.Load(context.Background())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if found != tc.wantFound || s.Len() != tc.wantLen || !s.Loaded() || s.Undecodable() == nil {
				t.Fatalf("found %v, len %d, loaded %v, undecodable %v; want found %v, len %d, loaded, a cause",
					found, s.Len(), s.Loaded(), s.Undecodable(), tc.wantFound, tc.wantLen)
			}
			s.Mark("x", st(5, 0))
			save(t, s)

			r, found := loaded(t, store, key, tScope)
			if !found || r.Undecodable() != nil || !r.Done("x", st(5, 0)) {
				t.Errorf("next start: found %v, undecodable %v, x done %v", found, r.Undecodable(), r.Done("x", st(5, 0)))
			}
		})
	}

	t.Run("store error", func(t *testing.T) {
		db := pgDB(t)
		store := checkpoint.NewPostgresStore(db)
		tenant, conn := pgConnection(t, db)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		s := New(store, Key{TenantID: tenant, ConnectionID: conn, NodeID: tNode}, tScope)
		if _, err := s.Load(context.Background()); err == nil || errors.Is(err, ErrUndecodable) {
			t.Errorf("Load on a closed database = %v, want a store error", err)
		}
		if s.Loaded() {
			t.Error("Loaded() = true after a store error")
		}
	})
}

// #723 on a real Postgres: another poller's snapshot and changes reset land
// between this Set's read of the changes row and its write. The Set notices
// within the bound, a restart finds every name, and nothing is published
// again.
func TestSetPG_ChangesOnAReplacedSnapshotAreRecovered(t *testing.T) {
	db := pgDB(t)
	pg := checkpoint.NewPostgresStore(db)
	tenant, conn := pgConnection(t, db)
	key := Key{TenantID: tenant, ConnectionID: conn, NodeID: tNode}
	hooked := &hookStore{Store: pg}

	a, _ := loaded(t, hooked, key, tScope)
	listing := map[string]Stamp{}
	for _, n := range names(100, "f") {
		listing[n] = st(1, 0)
		a.Mark(n, st(1, 0))
	}
	a.Observe(listing)
	save(t, a)
	b, _ := loaded(t, pg, key, tScope)

	reads := 0
	hooked.set(func(node string) {
		if node != tNode+changesSuffix {
			return
		}
		if reads++; reads != 2 { // 1: Refresh, 2: the Save about to write
			return
		}
		listing["fromB"] = st(1, 0)
		b.Observe(listing)
		b.Mark("fromB", st(1, 0))
		if err := b.Save(context.Background()); err != nil {
			t.Error(err)
		}
	})

	var published []string
	recovered := false
	for i := 0; i < boundSaves+1 && !recovered; i++ {
		n := names(boundSaves+1, "a")[i]
		listing[n] = st(1, 0)
		a.Observe(listing)
		if err := a.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		a.Mark(n, st(1, 0))
		published = append(published, n)
		before := a.gen
		save(t, a)
		recovered = a.gen != before
	}
	// The save that went stale is the first of the window; boundSaves more may
	// follow it before the check.
	if !recovered || len(published) > boundSaves+1 {
		t.Fatalf("recovered %v after %d saves, want within %d", recovered, len(published), boundSaves+1)
	}

	r, found := loaded(t, pg, key, tScope)
	if !found {
		t.Fatal("restart found no saved set")
	}
	for _, n := range append([]string{"fromB", "f0000"}, published...) {
		if !r.Done(n, st(1, 0)) {
			t.Errorf("after the restart %s is not done, so it would be published again", n)
		}
	}
}
