package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// TestDBProducer_RoundTrip proves the SDK refactor end-to-end with zero Docker:
// an envelope published into embedded JetStream flows through the SDK runner →
// dbProducer.Deliver → CREATE TABLE + INSERT against a mocked target database.
// The management-DB lookup and the target DB are both go-sqlmock; the target
// opener is injected via the connector's openTarget hook.
func TestDBProducer_RoundTrip(t *testing.T) {
	// Management DB: returns one database producer node.
	mgmtDB, mgmtMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock mgmt: %v", err)
	}
	defer mgmtDB.Close()

	const connID = "conn-db-1"
	nodes := `[{"id":"prod1","type":"producer","config":{"type":"database","database":{"host":"localhost","table":"events","mode":"create_insert"}}}]`
	mgmtMock.MatchExpectationsInOrder(false)
	for i := 0; i < 3; i++ {
		mgmtMock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).
				AddRow([]byte(nodes), []byte(`[]`)))
	}

	// Target DB: EXISTS check (false) → CREATE TABLE → INSERT.
	targetDB, targetMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock target: %v", err)
	}
	defer targetDB.Close()
	targetMock.MatchExpectationsInOrder(false)
	targetMock.ExpectQuery("information_schema.tables").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	targetMock.ExpectExec("CREATE TABLE").
		WillReturnResult(sqlmock.NewResult(0, 0))
	targetMock.ExpectExec("INSERT INTO").
		WillReturnResult(sqlmock.NewResult(1, 1))

	p := &dbProducer{
		openTarget: func(string) (*sql.DB, error) { return targetDB, nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "db-producer", DB: mgmtDB})

	env := envelope.New()
	env.ID = "db-env-1"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.ContentType = "application/json"
	env.Payload = []byte(`{"name":"widget"}`)
	h.Publish(t, env)

	// Confirm the full path ran by watching for the "inserted" SSE event.
	harness.Eventually(t, 5*time.Second, "row inserted into target", func() bool {
		for _, e := range p.getRecentEvents(connID) {
			if e.Type == "inserted" && e.Count == 1 {
				return true
			}
		}
		return false
	})

	if err := targetMock.ExpectationsWereMet(); err != nil {
		t.Errorf("target DB expectations: %v", err)
	}
}

// TestDBProducer_BadPayloadIsDropped verifies a non-JSON payload is treated as
// a poison message (Permanent → dropped), not retried forever.
func TestDBProducer_BadPayloadIsDropped(t *testing.T) {
	mgmtDB, mgmtMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock mgmt: %v", err)
	}
	defer mgmtDB.Close()

	const connID = "conn-db-bad"
	nodes := `[{"id":"prod1","type":"producer","config":{"type":"database","database":{"host":"localhost","table":"events"}}}]`
	mgmtMock.MatchExpectationsInOrder(false)
	for i := 0; i < 3; i++ {
		mgmtMock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).
				AddRow([]byte(nodes), []byte(`[]`)))
	}

	targetDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock target: %v", err)
	}
	defer targetDB.Close()

	p := &dbProducer{
		openTarget: func(string) (*sql.DB, error) { return targetDB, nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "db-producer", DB: mgmtDB})

	env := envelope.New()
	env.ID = "db-env-bad"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.Payload = []byte(`this is not json`)
	h.Publish(t, env)

	// An error event should be emitted; the SDK then acks (Permanent) — no panic,
	// no infinite retry. We assert the error surfaced via the event stream.
	harness.Eventually(t, 5*time.Second, "bad payload reported", func() bool {
		for _, e := range p.getRecentEvents(connID) {
			if e.Type == "error" {
				return true
			}
		}
		return false
	})
}

func TestParseTableName(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantQuery string // qualifiedTable(schema, table)
		wantErr   bool
	}{
		{"bare", "orders", `"orders"`, false},
		{"schema-qualified", "public.orders", `"public"."orders"`, false},
		{"case kept", "Sales.Orders", `"Sales"."Orders"`, false},
		{"embedded quote in table", `my"table`, `"my""table"`, false},
		{"embedded quotes in both parts", `s"x.t"y`, `"s""x"."t""y"`, false},
		{"two dots", "db.public.orders", "", true},
		{"leading dot", ".orders", "", true},
		{"trailing dot", "public.", "", true},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema, table, err := parseTableName(tt.in)
			if tt.wantErr {
				if !errors.Is(err, errBadTableName) {
					t.Fatalf("parseTableName(%q) err = %v, want errBadTableName", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTableName(%q): %v", tt.in, err)
			}
			if got := qualifiedTable(schema, table); got != tt.wantQuery {
				t.Errorf("qualifiedTable(parseTableName(%q)) = %s, want %s", tt.in, got, tt.wantQuery)
			}
		})
	}
}

// newCachedProducer returns a dbProducer whose target cache already holds one
// connection for connID, so Deliver goes straight to the target mock.
func newCachedProducer(connID string, targetDB *sql.DB, table string) *dbProducer {
	return &dbProducer{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		targetCache: map[string][]*TargetConnection{
			connID: {{DB: targetDB, Config: TargetDBConfig{Table: table, Mode: "create_insert"}}},
		},
		targetCacheTime: map[string]time.Time{connID: time.Now()},
		targetCacheTTL:  time.Minute,
		eventSubs:       map[string][]chan DBProdEvent{},
		recentEvents:    map[string][]DBProdEvent{},
	}
}

func dbEnvelope(connID string) *envelope.Envelope {
	env := envelope.New()
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.Payload = []byte(`{"name":"widget"}`)
	return env
}

// TestDBProducer_TableNameReachesSQL checks what the target database is sent:
// the existence lookup, CREATE TABLE and INSERT all target the schema the user
// typed, and a bare name stays unqualified.
func TestDBProducer_TableNameReachesSQL(t *testing.T) {
	tests := []struct {
		name         string
		table        string
		lookupSchema string
		lookupTable  string
		ident        string
	}{
		{"schema-qualified", "public.orders", "public", "orders", `"public"."orders"`},
		{"other schema", "sales.orders", "sales", "orders", `"sales"."orders"`},
		{"bare", "orders", "public", "orders", `"orders"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer targetDB.Close()
			mock.ExpectQuery(regexp.QuoteMeta(
				"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2)")).
				WithArgs(tt.lookupSchema, tt.lookupTable).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectExec("^" + regexp.QuoteMeta(`CREATE TABLE `+tt.ident+` ("name" TEXT)`) + "$").
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec("^" + regexp.QuoteMeta(`INSERT INTO `+tt.ident+` ("name") VALUES ($1)`) + "$").
				WithArgs("widget").
				WillReturnResult(sqlmock.NewResult(1, 1))

			const connID = "conn-table"
			p := newCachedProducer(connID, targetDB, tt.table)
			if err := p.Deliver(context.Background(), dbEnvelope(connID)); err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("target DB expectations: %v", err)
			}
		})
	}
}

// TestDBProducer_BadTableNameIsPermanent: a malformed table name surfaces in
// the live log and drops the message, and no SQL reaches the target.
func TestDBProducer_BadTableNameIsPermanent(t *testing.T) {
	targetDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer targetDB.Close()

	const connID = "conn-bad-table"
	p := newCachedProducer(connID, targetDB, "db.public.orders")
	err = p.Deliver(context.Background(), dbEnvelope(connID))
	if !sdk.IsPermanent(err) || !errors.Is(err, errBadTableName) {
		t.Fatalf("Deliver err = %v, want Permanent errBadTableName", err)
	}
	var reported bool
	for _, e := range p.getRecentEvents(connID) {
		if e.Type == "error" && strings.Contains(e.Message, "schema.table") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("no error event naming the expected form; events = %+v", p.getRecentEvents(connID))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected target DB calls: %v", err)
	}
}
