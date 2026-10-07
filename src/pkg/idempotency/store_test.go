package idempotency

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// The Postgres store against the real table: a repeat is seen with the first
// envelope id, remembering twice keeps the first, a key is scoped to its
// connection AND its tenant, and Expire removes only what is old. Skipped
// without VRSKY_TEST_POSTGRES_URL; CI sets it.
func TestPostgresStore(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	var owner, tenant string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('idem-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO tenants (name, slug, owner_id)
		VALUES ('idem', 'idem-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	connA, connB := uuid.New().String(), uuid.New().String()
	for _, id := range []string{connA, connB} {
		if _, err := db.ExecContext(ctx, `INSERT INTO connections (id, tenant_id, name, status) VALUES ($1, $2, $3, 'running')`, id, tenant, "wh-"+id); err != nil {
			t.Fatalf("seed connection: %v", err)
		}
	}
	s := NewPostgresStore(db)
	env1, env2 := uuid.New().String(), uuid.New().String()

	if _, seen, err := s.Seen(ctx, tenant, connA, "k1"); err != nil || seen {
		t.Fatalf("fresh key: seen=%v err=%v", seen, err)
	}
	if err := s.Remember(ctx, tenant, connA, "k1", env1); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember(ctx, tenant, connA, "k1", env2); err != nil {
		t.Fatalf("remembering twice must not fail: %v", err)
	}
	if id, seen, _ := s.Seen(ctx, tenant, connA, "k1"); !seen || id != env1 {
		t.Fatalf("repeat: seen=%v id=%s, want the FIRST envelope %s", seen, id, env1)
	}
	// Scoping: the same key on another connection is new; a wrong tenant
	// cannot read the row even with the right connection id.
	if _, seen, _ := s.Seen(ctx, tenant, connB, "k1"); seen {
		t.Fatal("a key leaked across connections")
	}
	if _, seen, _ := s.Seen(ctx, "00000000-0000-0000-0000-000000000000", connA, "k1"); seen {
		t.Fatal("a key was readable with another tenant id")
	}
	// Expire: only old rows go.
	if _, err := db.ExecContext(ctx, `UPDATE webhook_idempotency_keys SET created_at = NOW() - interval '31 days' WHERE idempotency_key = 'k1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember(ctx, tenant, connA, "k2", env2); err != nil {
		t.Fatal(err)
	}
	n, err := s.Expire(ctx, time.Now().Add(-Retention))
	if err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v, want 1", n, err)
	}
	if _, seen, _ := s.Seen(ctx, tenant, connA, "k1"); seen {
		t.Fatal("an expired key is still seen")
	}
	if _, seen, _ := s.Seen(ctx, tenant, connA, "k2"); !seen {
		t.Fatal("a fresh key was expired")
	}
	// Deleting the connection takes its keys with it.
	if _, err := db.ExecContext(ctx, `DELETE FROM connections WHERE id = $1`, connA); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM webhook_idempotency_keys WHERE connection_id = $1`, connA).Scan(&left)
	if left != 0 {
		t.Fatalf("%d keys survived their connection", left)
	}
}

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Unix(1_760_000_000, 0)
	s.now = func() time.Time { return now }

	if _, seen, _ := s.Seen(ctx, "t", "c", "k"); seen {
		t.Fatal("fresh key seen")
	}
	_ = s.Remember(ctx, "t", "c", "k", "e1")
	_ = s.Remember(ctx, "t", "c", "k", "e2")
	if id, seen, _ := s.Seen(ctx, "t", "c", "k"); !seen || id != "e1" {
		t.Fatalf("seen=%v id=%s, want e1", seen, id)
	}
	if _, seen, _ := s.Seen(ctx, "t", "other", "k"); seen {
		t.Fatal("leaked across connections")
	}
	if _, seen, _ := s.Seen(ctx, "other", "c", "k"); seen {
		t.Fatal("readable with another tenant")
	}
	now = now.Add(2 * time.Hour)
	_ = s.Remember(ctx, "t", "c", "k2", "e3")
	if n, _ := s.Expire(ctx, now.Add(-time.Hour)); n != 1 || s.Len() != 1 {
		t.Fatalf("expire removed %d, left %d; want 1 and 1", n, s.Len())
	}
}
