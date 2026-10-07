package sdk

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"sort"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// RestoreRunning against the real table: only running rows, only those whose
// graph mentions the type, each with its own tenant. Skipped without
// VRSKY_TEST_POSTGRES_URL; CI sets it.
func TestRestoreRunning(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	var owner, tenantA, tenantB string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('restore-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	for _, dst := range []*string{&tenantA, &tenantB} {
		if err := db.QueryRowContext(ctx, `INSERT INTO tenants (name, slug, owner_id)
			VALUES ('restore', 'restore-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	add := func(tenant, status, nodes string) string {
		id := uuid.New().String()
		if _, err := db.ExecContext(ctx, `INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
			VALUES ($1, $2, $3, $4, $5::jsonb, '[]'::jsonb)`, id, tenant, "c-"+id, status, nodes); err != nil {
			t.Fatal(err)
		}
		return id
	}
	http := `[{"id":"in","type":"consumer","config":{"type":"http","http":{}}}]`
	file := `[{"id":"in","type":"consumer","config":{"type":"file","file":{"path":"/in"}}}]`
	runningA := add(tenantA, "running", http)
	runningB := add(tenantB, "running", http)
	add(tenantA, "stopped", http)
	add(tenantA, "error", http)
	add(tenantA, "running", file)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	var got []string
	n := RestoreRunning(ctx, db, quiet, "http", func(_ context.Context, id, tenant string) {
		got = append(got, id+"@"+tenant)
	})
	want := []string{runningA + "@" + tenantA, runningB + "@" + tenantB}
	sort.Strings(got)
	sort.Strings(want)
	if n != 2 || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("restored %d: %v\nwant %v", n, got, want)
	}
	// A type nobody runs restores nothing; a nil database is a no-op.
	if n := RestoreRunning(ctx, db, quiet, "kafka", func(context.Context, string, string) { t.Fatal("started") }); n != 0 {
		t.Fatalf("kafka: %d", n)
	}
	if n := RestoreRunning(ctx, nil, quiet, "http", func(context.Context, string, string) { t.Fatal("started") }); n != 0 {
		t.Fatalf("nil db: %d", n)
	}
}
