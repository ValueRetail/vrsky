// Package testdb gives a test its own PostgreSQL database.
//
// Most tests in this repo run on sqlmock, which checks the SQL text and
// nothing else. The few that need a real database — tenant isolation as
// Postgres enforces it, the advisory lock, driver behaviour — use this
// package: each gets a fresh database on a server named by
// VRSKY_TEST_POSTGRES_URL, with every migration applied, dropped again when
// the test ends. No test shares rows with another, so there is nothing to
// clean up and nothing to collide with when packages run in parallel.
//
// Without the variable the test is skipped, so a plain `go test ./...` on a
// machine with no database still passes. CI sets it (and fails if one of
// these tests is ever skipped there — see the Go Tests job).
//
//	VRSKY_TEST_POSTGRES_URL=postgres://postgres:…@localhost:5432/postgres?sslmode=disable
//
// The role must be allowed to create databases.
package testdb

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// EnvVar names the Postgres server tests may create databases on.
const EnvVar = "VRSKY_TEST_POSTGRES_URL"

// SkipMessage is what a test logs when it is skipped for want of a database.
// CI greps for it: there, a skip means the variable or the service was lost.
const SkipMessage = EnvVar + " not set — this test needs a real Postgres"

// Fresh returns the URL of a new database with every migration applied.
func Fresh(t *testing.T) string {
	t.Helper()
	dbURL := Empty(t)
	schema, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("testdb: open the new database: %v", err)
	}
	defer schema.Close()
	dir := migrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("testdb: no migrations in %s: %v", dir, err)
	}
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("testdb: read %s: %v", f, err)
		}
		if _, err := schema.Exec(string(body)); err != nil {
			t.Fatalf("testdb: apply %s: %v", filepath.Base(f), err)
		}
	}
	return dbURL
}

// Empty returns the URL of a new database with nothing in it, for a test
// that builds its own schema.
func Empty(t *testing.T) string {
	t.Helper()
	adminURL := os.Getenv(EnvVar)
	if adminURL == "" {
		t.Skip(SkipMessage)
	}
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("testdb: parse %s: %v", EnvVar, err)
	}
	admin, err := sql.Open("postgres", adminURL)
	if err != nil {
		t.Fatalf("testdb: open %s: %v", EnvVar, err)
	}
	defer admin.Close()
	name := "vrsky_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := admin.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier(name)); err != nil {
		t.Fatalf("testdb: create a database on %s (the role must be allowed to): %v", u.Host, err)
	}
	// Registered here, so it runs after every cleanup the test itself adds:
	// by then the test's own connections are closed. FORCE covers one that
	// is not.
	t.Cleanup(func() {
		a, err := sql.Open("postgres", adminURL)
		if err != nil {
			return
		}
		defer a.Close()
		if _, err := a.Exec(`DROP DATABASE IF EXISTS ` + pq.QuoteIdentifier(name) + ` WITH (FORCE)`); err != nil {
			t.Logf("testdb: drop %s: %v", name, err)
		}
	})
	u.Path = "/" + name
	return u.String()
}

// migrationsDir finds infrastructure/migrations by walking up from the
// test's working directory (the package directory), so the helper works from
// any package depth.
func migrationsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("testdb: working directory: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "infrastructure", "migrations")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("testdb: infrastructure/migrations not found above %s", dir)
		}
		dir = parent
	}
}
