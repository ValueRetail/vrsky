package testdb

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

func count(t *testing.T, dbURL, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// Fresh gives a migrated database, Empty a bare one, and both are gone from
// the server once the test that asked for them has ended — a helper that
// leaked a database per test would fill a developer's Postgres in a day.
func TestFreshAndEmpty_CreateAndDropTheirDatabase(t *testing.T) {
	admin := os.Getenv(EnvVar)
	if admin == "" {
		t.Skip(SkipMessage)
	}
	var freshURL, emptyURL string
	t.Run("inside the test", func(t *testing.T) {
		freshURL, emptyURL = Fresh(t), Empty(t)
		if n := count(t, freshURL, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('tenants', 'connections', 'agents')`); n != 3 {
			t.Errorf("Fresh: %d of the 3 expected tables exist — migrations were not applied", n)
		}
		if n := count(t, emptyURL, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'`); n != 0 {
			t.Errorf("Empty: %d tables, want none", n)
		}
		if freshURL == emptyURL {
			t.Error("two calls returned the same database")
		}
	})
	for _, u := range []string{freshURL, emptyURL} {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatalf("parse %q: %v", u, err)
		}
		name := strings.TrimPrefix(parsed.Path, "/")
		if n := count(t, admin, `SELECT count(*) FROM pg_database WHERE datname = $1`, name); n != 0 {
			t.Errorf("database %s still exists after its test ended", name)
		}
	}
}
