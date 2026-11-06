package pgdsn

import (
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/testdb"
)

func TestBuild(t *testing.T) {
	tests := []struct {
		name string
		in   Config
		want string
	}{
		{"defaults", Config{Host: "db", User: "u", Database: "d"},
			`host='db' port=5432 user='u' password='' dbname='d' sslmode='disable'`},
		{"timeout and sslmode", Config{Host: "db", Port: 6543, User: "u", Password: "p", Database: "d", SSLMode: "require", ConnectTimeout: 10},
			`host='db' port=6543 user='u' password='p' dbname='d' sslmode='require' connect_timeout=10`},
		{"space, quote and backslash", Config{Host: "db", User: "u", Password: `a b'c\d`, Database: "d"},
			`host='db' port=5432 user='u' password='a b\'c\\d' dbname='d' sslmode='disable'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Build(tt.in); got != tt.want {
				t.Errorf("Build = %s\nwant    %s", got, tt.want)
			}
			// lib/pq must parse it.
			if _, err := pq.NewConnector(Build(tt.in)); err != nil {
				t.Errorf("lib/pq refuses the string: %v", err)
			}
		})
	}
}

// A role whose password has a space, a quote and a backslash signs in with
// the built string. Skipped without VRSKY_TEST_POSTGRES_URL.
func TestBuild_OddPasswordSignsIn(t *testing.T) {
	dbURL := testdb.Empty(t)
	admin, err := sql.Open("postgres", os.Getenv(testdb.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	role := "pgdsn_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	const password = `hunter2 sec'ret\x`
	if _, err := admin.Exec(`CREATE ROLE ` + pq.QuoteIdentifier(role) + ` LOGIN PASSWORD ` + pq.QuoteLiteral(password)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP ROLE IF EXISTS ` + pq.QuoteIdentifier(role)); err != nil {
			t.Logf("drop role %s: %v", role, err)
		}
	})

	u, _ := url.Parse(dbURL)
	port, _ := strconv.Atoi(u.Port())
	db, err := sql.Open("postgres", Build(Config{
		Host: u.Hostname(), Port: port, User: role, Password: password,
		Database: strings.TrimPrefix(u.Path, "/"), ConnectTimeout: 10,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("sign in with the odd password: %v", err)
	}
}
