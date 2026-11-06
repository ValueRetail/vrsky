package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// What db-producer does with a message it cannot write (#705), against a real
// Postgres: the management database (the connection row) and every target are
// databases on the server VRSKY_TEST_POSTGRES_URL names. Skipped without it;
// CI sets it.
//
// Deliver's error is what the SDK acts on: nil acks, sdk.Permanent acks and
// drops, anything else is NAK'd, redelivered and after five attempts copied
// to the DLQ with the error text in X-Vrsky-Last-Error (pkg/sdk covers that
// hop on the embedded NATS server). So "dead-lettered with the row's error"
// is checked here as: not nil, not Permanent, and the error names the cause.

// lockedBuffer collects log output; slog writes from the delivering goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type pgEnv struct {
	t      *testing.T
	mgmt   *sql.DB
	tenant string
	logs   *lockedBuffer
	p      *dbProducer
}

func newPGEnv(t *testing.T) *pgEnv {
	t.Helper()
	mgmt, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgmt.Close() })
	var owner, tenant string
	if err := mgmt.QueryRow(`INSERT INTO users (email, password_hash, status)
		VALUES ('db-prod-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := mgmt.QueryRow(`INSERT INTO tenants (name, slug, owner_id)
		VALUES ('db-prod', 'db-prod-'||gen_random_uuid(), $1) RETURNING id`, owner).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	p := &dbProducer{
		db:              mgmt,
		logger:          slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		targetCache:     map[string][]*TargetConnection{},
		targetCacheTime: map[string]time.Time{},
		targetCacheTTL:  time.Minute,
		eventSubs:       map[string][]chan DBProdEvent{},
		recentEvents:    map[string][]DBProdEvent{},
		openTarget:      func(dsn string) (*sql.DB, error) { return sql.Open("postgres", dsn) },
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	return &pgEnv{t: t, mgmt: mgmt, tenant: tenant, logs: logs, p: p}
}

// node is one database destination node of a connection.
type node struct {
	id  string
	cfg map[string]any // the "database" block
}

// connection stores a connection whose producer nodes are nodes and returns
// its id.
func (e *pgEnv) connection(nodes ...node) string {
	e.t.Helper()
	var list []map[string]any
	for _, n := range nodes {
		list = append(list, map[string]any{
			"id": n.id, "type": "producer",
			"config": map[string]any{"type": "database", "database": n.cfg},
		})
	}
	body, err := json.Marshal(list)
	if err != nil {
		e.t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := e.mgmt.Exec(`INSERT INTO connections (id, tenant_id, name, status, nodes, edges)
		VALUES ($1, $2, $3, 'running', $4::jsonb, '[]'::jsonb)`, id, e.tenant, "db-"+id, string(body)); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *pgEnv) envelope(connID, payload string) *envelope.Envelope {
	env := envelope.New()
	env.IntegrationID = connID
	env.TenantID = e.tenant
	env.ContentType = "application/json"
	env.Payload = []byte(payload)
	return env
}

// target is a database a destination node writes to.
type target struct {
	url  string
	conn *sql.DB
}

func newTarget(t *testing.T) *target {
	t.Helper()
	u := testdb.Empty(t)
	db, err := sql.Open("postgres", u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &target{url: u, conn: db}
}

func (tg *target) exec(t *testing.T, q string) {
	t.Helper()
	if _, err := tg.conn.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (tg *target) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := tg.conn.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// dbConfig is the node's database block for a database URL.
func dbConfig(t *testing.T, dbURL, table, mode string) map[string]any {
	t.Helper()
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port in %s: %v", u.Redacted(), err)
	}
	pw, _ := u.User.Password()
	return map[string]any{
		"host": u.Hostname(), "port": port, "user": u.User.Username(), "password": pw,
		"database": strings.TrimPrefix(u.Path, "/"), "sslmode": "disable",
		"table": table, "mode": mode,
	}
}

// closedPort is a local TCP port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// wantRetried fails unless err sends the message back for another attempt
// (and so, in the end, to the DLQ) and names the cause.
func wantRetried(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Deliver acked the message (nil error); want a retryable error containing %q", contains)
	}
	if sdk.IsPermanent(err) {
		t.Fatalf("Deliver err = %v is Permanent (acked and dropped); want retryable", err)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("Deliver err = %q, want it to contain %q", err, contains)
	}
}

// A message db-producer cannot write is not acked: it is retried and ends
// in the DLQ with the reason. Before #705 every case but "written" acked.
func TestDeliver_WhatItCannotWriteIsRetried(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (cfg map[string]any, tg *target)
		payload string
		// wantErr is "" for a write that succeeds; otherwise a substring of
		// the error the message is dead-lettered with.
		wantErr string
		// wantRows is how many rows the target's items table has afterwards.
		wantRows int
		// wantErrorLog is a substring of an ERROR-level log line.
		wantErrorLog string
	}{
		{
			name: "written",
			setup: func(t *testing.T) (map[string]any, *target) {
				tg := newTarget(t)
				tg.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
				return dbConfig(t, tg.url, "items", "insert"), tg
			},
			payload:  `[{"id":1,"name":"a"},{"id":2,"name":"b"}]`,
			wantRows: 2,
		},
		{
			name: "target unreachable",
			setup: func(t *testing.T) (map[string]any, *target) {
				return map[string]any{
					"host": "127.0.0.1", "port": closedPort(t), "user": "u", "database": "d",
					"table": "items", "mode": "insert",
				}, nil
			},
			payload:      `{"id":1,"name":"a"}`,
			wantErr:      "connection refused",
			wantErrorLog: "connection refused",
		},
		{
			name: "target database missing",
			setup: func(t *testing.T) (map[string]any, *target) {
				cfg := dbConfig(t, testdb.Empty(t), "items", "insert")
				cfg["database"] = "vrsky_test_missing_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
				return cfg, nil
			},
			payload: `{"id":1,"name":"a"}`,
			wantErr: "does not exist",
		},
		{
			name: "row violates NOT NULL",
			setup: func(t *testing.T) (map[string]any, *target) {
				tg := newTarget(t)
				tg.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
				return dbConfig(t, tg.url, "items", "insert"), tg
			},
			// The first row is fine; it must not stay behind.
			payload:      `[{"id":1,"name":"a"},{"id":2}]`,
			wantErr:      `null value in column "name"`,
			wantRows:     0,
			wantErrorLog: `null value in column`,
		},
		{
			name: "row has the wrong type",
			setup: func(t *testing.T) (map[string]any, *target) {
				tg := newTarget(t)
				tg.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
				return dbConfig(t, tg.url, "items", "insert"), tg
			},
			payload:  `[{"id":1,"name":"a"},{"id":"two","name":"b"}]`,
			wantErr:  "invalid input syntax for type integer",
			wantRows: 0,
		},
		{
			name: "row duplicates a key",
			setup: func(t *testing.T) (map[string]any, *target) {
				tg := newTarget(t)
				tg.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
				tg.exec(t, `INSERT INTO items VALUES (7, 'already there')`)
				return dbConfig(t, tg.url, "items", "insert"), tg
			},
			payload:  `[{"id":1,"name":"a"},{"id":7,"name":"b"}]`,
			wantErr:  "duplicate key value",
			wantRows: 1, // the row that was there before
		},
		{
			name: "node config does not parse",
			setup: func(t *testing.T) (map[string]any, *target) {
				return map[string]any{"host": "db", "port": "5432", "table": "items"}, nil
			},
			payload:      `{"id":1,"name":"a"}`,
			wantErr:      "node config does not parse",
			wantErrorLog: "node config does not parse",
		},
		{
			name: "node has no host",
			setup: func(t *testing.T) (map[string]any, *target) {
				return map[string]any{"database": "d", "table": "items", "mode": "insert"}, nil
			},
			payload:      `{"id":1,"name":"a"}`,
			wantErr:      "no host",
			wantErrorLog: "no host",
		},
		{
			name: "table name is not table or schema.table",
			setup: func(t *testing.T) (map[string]any, *target) {
				tg := newTarget(t)
				return dbConfig(t, tg.url, "a.b.c", "insert"), nil
			},
			payload: `{"id":1,"name":"a"}`,
			wantErr: "schema.table",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPGEnv(t)
			cfg, tg := tt.setup(t)
			const nodeID = "db-node-1"
			connID := e.connection(node{id: nodeID, cfg: cfg})

			if !e.p.ServesConnection(context.Background(), e.tenant, connID) {
				t.Fatal("ServesConnection = false: the SDK would ack the message before Deliver runs")
			}
			err := e.p.Deliver(context.Background(), e.envelope(connID, tt.payload))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Deliver: %v", err)
				}
			} else {
				wantRetried(t, err, tt.wantErr)
				if !strings.Contains(err.Error(), nodeID) {
					t.Errorf("Deliver err = %q does not name the node %s", err, nodeID)
				}
			}
			if tg != nil {
				if got := tg.count(t, "items"); got != tt.wantRows {
					t.Errorf("items has %d rows, want %d", got, tt.wantRows)
				}
				// A failed transaction gives its connection back: the
				// target's pool is not left holding one per refused message.
				if db := e.p.targetCache[connID][0].DB; db == nil {
					t.Error("target pool was not opened")
				} else if n := db.Stats().InUse; n != 0 {
					t.Errorf("target pool has %d connections in use after Deliver, want 0", n)
				}
			}
			if tt.wantErrorLog != "" && !hasErrorLog(e.logs.String(), tt.wantErrorLog) {
				t.Errorf("no ERROR log line containing %q; logs:\n%s", tt.wantErrorLog, e.logs.String())
			}
		})
	}
}

func hasErrorLog(logs, contains string) bool {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, `"level":"ERROR"`) && strings.Contains(line, contains) {
			return true
		}
	}
	return false
}

// A target whose pool is open and that then goes away (the database is
// dropped, the server restarts) fails the delivery; before #705 the insert
// error was logged and the message acked.
func TestDeliver_TargetGoneAfterFirstWriteIsRetried(t *testing.T) {
	e := newPGEnv(t)
	tg := newTarget(t)
	tg.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
	connID := e.connection(node{id: "n1", cfg: dbConfig(t, tg.url, "items", "insert")})

	if err := e.p.Deliver(context.Background(), e.envelope(connID, `{"id":1,"name":"a"}`)); err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	_ = tg.conn.Close()
	admin, err := sql.Open("postgres", os.Getenv(testdb.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	u, _ := url.Parse(tg.url)
	if _, err := admin.Exec(`DROP DATABASE ` + pq.QuoteIdentifier(strings.TrimPrefix(u.Path, "/")) + ` WITH (FORCE)`); err != nil {
		t.Fatal(err)
	}

	err = e.p.Deliver(context.Background(), e.envelope(connID, `{"id":2,"name":"b"}`))
	if err == nil || sdk.IsPermanent(err) {
		t.Fatalf("Deliver after the target went away: err = %v, want retryable", err)
	}
}

// With two targets and one failing, the message is retried, and the
// redeliveries do not write it to the healthy target again. Once the second
// target is fixed, the next redelivery writes it there and the message is
// acked: one row in each.
func TestDeliver_RedeliveryDoesNotRewriteTargetsThatTookIt(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the failing node's config and a func that fixes it,
		// after which the target's items table has the row.
		setup   func(t *testing.T) (cfg map[string]any, fix func(t *testing.T) *target)
		wantErr string
	}{
		{
			name: "second target down",
			setup: func(t *testing.T) (map[string]any, func(t *testing.T) *target) {
				admin := os.Getenv(testdb.EnvVar)
				cfg := dbConfig(t, testdb.Empty(t), "items", "create_insert")
				name := "vrsky_test_later_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
				cfg["database"] = name
				return cfg, func(t *testing.T) *target {
					a, err := sql.Open("postgres", admin)
					if err != nil {
						t.Fatal(err)
					}
					defer a.Close()
					if _, err := a.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier(name)); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						a, err := sql.Open("postgres", admin)
						if err != nil {
							return
						}
						defer a.Close()
						_, _ = a.Exec(`DROP DATABASE IF EXISTS ` + pq.QuoteIdentifier(name) + ` WITH (FORCE)`)
					})
					u, _ := url.Parse(admin)
					u.Path = "/" + name
					db, err := sql.Open("postgres", u.String())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = db.Close() })
					return &target{url: u.String(), conn: db}
				}
			},
			wantErr: "does not exist",
		},
		{
			name: "second target refuses the row",
			setup: func(t *testing.T) (map[string]any, func(t *testing.T) *target) {
				tg := newTarget(t)
				tg.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL CHECK (name <> 'widget'))`)
				return dbConfig(t, tg.url, "items", "insert"), func(t *testing.T) *target {
					tg.exec(t, `ALTER TABLE items DROP CONSTRAINT items_name_check`)
					return tg
				}
			},
			wantErr: "violates check constraint",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPGEnv(t)
			healthy := newTarget(t)
			healthy.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
			failingCfg, fix := tt.setup(t)
			connID := e.connection(
				node{id: "healthy", cfg: dbConfig(t, healthy.url, "items", "insert")},
				node{id: "failing", cfg: failingCfg},
			)
			// One message, delivered again and again: the SDK hands the same
			// envelope to every redelivery.
			env := e.envelope(connID, `{"id":1,"name":"widget"}`)
			deliver := func() error {
				cp := *env
				cp.Payload = append([]byte(nil), env.Payload...)
				return e.p.Deliver(context.Background(), &cp)
			}

			for attempt := 1; attempt <= 3; attempt++ {
				wantRetried(t, deliver(), tt.wantErr)
				if got := healthy.count(t, "items"); got != 1 {
					t.Fatalf("attempt %d: healthy target has %d rows, want 1 (written once)", attempt, got)
				}
				if n := e.p.writtenSet().size(); n != 1 {
					t.Fatalf("attempt %d: skip set holds %d entries, want 1 (the healthy target)", attempt, n)
				}
			}

			fixed := fix(t)
			if err := deliver(); err != nil {
				t.Fatalf("Deliver after the fix: %v", err)
			}
			if got := healthy.count(t, "items"); got != 1 {
				t.Errorf("healthy target has %d rows, want 1", got)
			}
			if got := fixed.count(t, "items"); got != 1 {
				t.Errorf("fixed target has %d rows, want 1", got)
			}
			if n := e.p.writtenSet().size(); n != 0 {
				t.Errorf("skip set holds %d entries after the message was delivered, want 0", n)
			}
		})
	}
}

// Another message is not skipped because an earlier one was: the skip is per
// message.
func TestDeliver_SkipIsPerMessage(t *testing.T) {
	e := newPGEnv(t)
	healthy := newTarget(t)
	healthy.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
	connID := e.connection(
		node{id: "healthy", cfg: dbConfig(t, healthy.url, "items", "insert")},
		node{id: "down", cfg: map[string]any{"host": "127.0.0.1", "port": closedPort(t), "user": "u", "database": "d", "table": "items"}},
	)
	for i := 1; i <= 3; i++ {
		err := e.p.Deliver(context.Background(), e.envelope(connID, fmt.Sprintf(`{"id":%d,"name":"n"}`, i)))
		wantRetried(t, err, "connection refused")
	}
	if got := healthy.count(t, "items"); got != 3 {
		t.Errorf("healthy target has %d rows, want 3 (one per message)", got)
	}
}

// setNodes replaces a connection's producer nodes.
func (e *pgEnv) setNodes(connID string, nodes ...node) {
	e.t.Helper()
	var list []map[string]any
	for _, n := range nodes {
		list = append(list, map[string]any{
			"id": n.id, "type": "producer",
			"config": map[string]any{"type": "database", "database": n.cfg},
		})
	}
	body, err := json.Marshal(list)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.mgmt.Exec(`UPDATE connections SET nodes = $1::jsonb WHERE id = $2`, string(body), connID); err != nil {
		e.t.Fatal(err)
	}
}

// A node edited to point at another table is a new target: the redelivery
// writes the message there even though the node committed it to its old
// table before.
func TestDeliver_RepointedNodeIsANewTarget(t *testing.T) {
	e := newPGEnv(t)
	healthy := newTarget(t)
	healthy.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
	healthy.exec(t, `CREATE TABLE items_v2 (id int PRIMARY KEY, name text NOT NULL)`)
	down := node{id: "down", cfg: map[string]any{"host": "127.0.0.1", "port": closedPort(t), "user": "u", "database": "d", "table": "items"}}
	connID := e.connection(node{id: "healthy", cfg: dbConfig(t, healthy.url, "items", "insert")}, down)
	env := e.envelope(connID, `{"id":1,"name":"a"}`)

	wantRetried(t, e.p.Deliver(context.Background(), env), "connection refused")
	if got := healthy.count(t, "items"); got != 1 {
		t.Fatalf("items has %d rows after the first delivery, want 1", got)
	}

	// The node is edited and the pipeline redeployed (which evicts the cache).
	e.setNodes(connID, node{id: "healthy", cfg: dbConfig(t, healthy.url, "items_v2", "insert")}, down)
	e.p.evictTargetCache(connID)

	wantRetried(t, e.p.Deliver(context.Background(), env), "connection refused")
	if got := healthy.count(t, "items_v2"); got != 1 {
		t.Errorf("items_v2 has %d rows after the redelivery, want 1 (a re-pointed node is a new target)", got)
	}
	if got := healthy.count(t, "items"); got != 1 {
		t.Errorf("items has %d rows, want 1", got)
	}
}

// oddRole creates a role on the test server whose password has a space, a
// quote and a backslash, and returns its name and password.
func oddRole(t *testing.T, password string) string {
	t.Helper()
	admin, err := sql.Open("postgres", os.Getenv(testdb.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	role := "dbprod_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := admin.Exec(`CREATE ROLE ` + pq.QuoteIdentifier(role) + ` LOGIN PASSWORD ` + pq.QuoteLiteral(password)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP OWNED BY ` + pq.QuoteIdentifier(role) + `; DROP ROLE IF EXISTS ` + pq.QuoteIdentifier(role)); err != nil {
			t.Logf("drop role %s: %v", role, err)
		}
	})
	return role
}

// A password with a space, a quote and a backslash signs in. Before #705's
// review fix the connection string was `password=%s` unquoted and lib/pq
// could not parse it.
func TestDeliver_PasswordWithSpaceAndQuoteSignsIn(t *testing.T) {
	const password = `hunter2 sec'ret\x`
	// The role first: its cleanup then runs after the target database (and
	// the grant in it) is gone.
	role := oddRole(t, password)
	e := newPGEnv(t)
	tg := newTarget(t)
	tg.exec(t, `CREATE TABLE items (id int PRIMARY KEY, name text NOT NULL)`)
	tg.exec(t, `GRANT INSERT, SELECT ON items TO `+pq.QuoteIdentifier(role))

	cfg := dbConfig(t, tg.url, "items", "insert")
	cfg["user"], cfg["password"] = role, password
	connID := e.connection(node{id: "n1", cfg: cfg})
	if err := e.p.Deliver(context.Background(), e.envelope(connID, `{"id":1,"name":"a"}`)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := tg.count(t, "items"); got != 1 {
		t.Errorf("items has %d rows, want 1", got)
	}
}

// No part of the password reaches the error the message is dead-lettered
// with, the log or the activity feed, whatever the failure.
func TestDeliver_PasswordNeverInErrors(t *testing.T) {
	const password = `hunter2 s3cretPart'q`
	fragments := []string{"hunter2", "s3cretPart"}
	tests := []struct {
		name    string
		cfg     func(t *testing.T) map[string]any
		wantErr string
	}{
		{"target unreachable", func(t *testing.T) map[string]any {
			return map[string]any{"host": "127.0.0.1", "port": closedPort(t), "user": "u", "password": password, "database": "d", "table": "items"}
		}, "connection refused"},
		{"wrong password", func(t *testing.T) map[string]any {
			role := oddRole(t, "the right one")
			cfg := dbConfig(t, testdb.Empty(t), "items", "insert")
			cfg["user"], cfg["password"] = role, password
			return cfg
		}, "password authentication failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPGEnv(t)
			connID := e.connection(node{id: "n1", cfg: tt.cfg(t)})
			err := e.p.Deliver(context.Background(), e.envelope(connID, `{"id":1,"name":"a"}`))
			wantRetried(t, err, tt.wantErr)
			events, _ := json.Marshal(e.p.getRecentEvents(connID))
			for _, f := range fragments {
				for what, text := range map[string]string{"error": err.Error(), "log": e.logs.String(), "events": string(events)} {
					if strings.Contains(text, f) {
						t.Errorf("%s contains password fragment %q: %s", what, f, text)
					}
				}
			}
		})
	}
}

// A target that accepts the TCP connection and never answers (a stuck
// server, a proxy that drops what it gets) fails the delivery after the
// connect timeout instead of holding the worker. targetConnectTimeout is
// 10 s in production; the test shortens it.
func TestDeliver_ConnectTimeout(t *testing.T) {
	if targetConnectTimeout != 10 {
		t.Fatalf("targetConnectTimeout = %d, want 10", targetConnectTimeout)
	}
	old := targetConnectTimeout
	targetConnectTimeout = 1
	t.Cleanup(func() { targetConnectTimeout = old })

	// newPGEnv first: cleanups run last-first, so the listener (and the
	// connections it holds) closes before the producer's Stop, which waits for
	// a dial still in progress.
	e := newPGEnv(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c) // never read, never answered
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})

	connID := e.connection(node{id: "n1", cfg: map[string]any{
		"host": "127.0.0.1", "port": l.Addr().(*net.TCPAddr).Port, "user": "u", "database": "d", "table": "items",
	}})
	done := make(chan error, 1)
	go func() { done <- e.p.Deliver(context.Background(), e.envelope(connID, `{"id":1,"name":"a"}`)) }()
	select {
	case err := <-done:
		if err == nil || sdk.IsPermanent(err) {
			t.Fatalf("Deliver err = %v, want retryable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deliver still waiting on a target that never answers after 5 s (connect timeout 1 s)")
	}
}

// The editor's Test Connection signs in with such a password too.
func TestTestConnection_PasswordWithSpaceAndQuote(t *testing.T) {
	const password = `hunter2 sec'ret\x`
	role := oddRole(t, password)
	cfg := dbConfig(t, testdb.Empty(t), "items", "insert")
	cfg["user"], cfg["password"] = role, password
	body, _ := json.Marshal(cfg)
	rec := httptest.NewRecorder()
	testConnectionHandler()(rec, httptest.NewRequest(http.MethodPost, "/test-connection/", bytes.NewReader(body)))
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !resp.OK {
		t.Fatalf("response %s (decode err %v), want ok", rec.Body.String(), err)
	}
}
