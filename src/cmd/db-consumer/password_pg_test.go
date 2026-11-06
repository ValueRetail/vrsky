package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// A source whose password has a space, a quote and a backslash is read: the
// connection string quotes every value (#705 review). Before, it was
// `password=%s` unquoted and lib/pq could not parse it. The management DB is
// sqlmock as in TestDBConsumer_RoundTrip; the source is a real database on
// VRSKY_TEST_POSTGRES_URL (skipped without it).
func TestDBConsumer_PasswordWithSpaceAndQuote(t *testing.T) {
	const password = `hunter2 sec'ret\x`
	admin, err := sql.Open("postgres", os.Getenv(testdb.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	role := "dbcons_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := admin.Exec(`CREATE ROLE ` + pq.QuoteIdentifier(role) + ` LOGIN PASSWORD ` + pq.QuoteLiteral(password)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP OWNED BY ` + pq.QuoteIdentifier(role) + `; DROP ROLE IF EXISTS ` + pq.QuoteIdentifier(role)); err != nil {
			t.Logf("drop role %s: %v", role, err)
		}
	})
	// After the role: cleanups run last-first, so the database (and the
	// grant in it) is dropped before the role.
	srcURL := testdb.Empty(t)
	src, err := sql.Open("postgres", srcURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	for _, q := range []string{
		`CREATE TABLE widgets (id int, name text)`,
		`INSERT INTO widgets VALUES (1, 'alpha'), (2, 'beta')`,
		`GRANT SELECT ON widgets TO ` + pq.QuoteIdentifier(role),
	} {
		if _, err := src.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	u, _ := url.Parse(srcURL)
	port, _ := strconv.Atoi(u.Port())
	dbBlock, _ := json.Marshal(map[string]any{
		"host": u.Hostname(), "port": port, "user": role, "password": password,
		"database": strings.TrimPrefix(u.Path, "/"), "table": "widgets", "poll_interval_seconds": 0,
	})
	nodes := fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"database","database":%s}}]`, dbBlock)

	const connID, tenant = "conn-dbc-pw", "tenant-pw"
	mgmtDB, mgmtMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mgmtDB.Close()
	mgmtMock.MatchExpectationsInOrder(false)
	mgmtMock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "DB Conn", []byte(nodes), []byte(`[]`)))
	mgmtMock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mgmtMock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &dbConsumer{} // the real opener
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "db-consumer", DB: mgmtDB})
	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start command: %v", err)
	}
	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 10*time.Second)
	var rows []map[string]any
	if err := json.Unmarshal(got.Payload, &rows); err != nil || len(rows) != 2 {
		t.Fatalf("payload = %s (err %v), want the two widgets", got.Payload, err)
	}
}

// The editor's test-connection, sample-data and schema endpoints sign in with
// such a password too.
func TestDBConsumer_HandlersAcceptPasswordWithSpaceAndQuote(t *testing.T) {
	const password = `hunter2 sec'ret\x`
	admin, err := sql.Open("postgres", os.Getenv(testdb.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	role := "dbcons_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := admin.Exec(`CREATE ROLE ` + pq.QuoteIdentifier(role) + ` LOGIN PASSWORD ` + pq.QuoteLiteral(password)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP ROLE IF EXISTS ` + pq.QuoteIdentifier(role)); err != nil {
			t.Logf("drop role %s: %v", role, err)
		}
	})
	srcURL := testdb.Empty(t)
	src, err := sql.Open("postgres", srcURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	for _, q := range []string{
		`CREATE TABLE widgets (id int, name text)`,
		`INSERT INTO widgets VALUES (1, 'alpha')`,
		`GRANT SELECT ON widgets TO ` + pq.QuoteIdentifier(role),
	} {
		if _, err := src.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	u, _ := url.Parse(srcURL)
	port, _ := strconv.Atoi(u.Port())
	body, _ := json.Marshal(map[string]any{
		"host": u.Hostname(), "port": port, "user": role, "password": password,
		"database": strings.TrimPrefix(u.Path, "/"), "table": "widgets",
	})
	for name, h := range map[string]http.HandlerFunc{
		"test-connection": handleTestConnection(),
		"sample-data":     handleSampleData(),
		"schema":          handleSchema(),
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodPost, "/"+name+"/", strings.NewReader(string(body))))
			var resp struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !resp.OK {
				t.Fatalf("response %s (decode err %v), want ok", rec.Body.String(), err)
			}
		})
	}
}
