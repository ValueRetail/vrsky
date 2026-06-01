package main

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// TestDBConsumer_RoundTrip drives the SDK-refactored db-consumer end-to-end with
// zero Docker: a connection start command on the embedded NATS makes it read the
// connection config from a mocked management DB, query a mocked source DB, and
// publish the rows as an envelope onto the data stream.
func TestDBConsumer_RoundTrip(t *testing.T) {
	const (
		connID = "conn-dbc-1"
		tenant = "tenant-x"
	)

	// Management DB: connection lookup + status/last_payload writes.
	mgmtDB, mgmtMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock mgmt: %v", err)
	}
	defer mgmtDB.Close()
	mgmtMock.MatchExpectationsInOrder(false)

	nodes := `[{"id":"c1","type":"consumer","config":{"type":"database","database":{"host":"localhost","table":"widgets","poll_interval_seconds":0}}}]`
	mgmtMock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(connID, tenant, "DB Conn", []byte(nodes), []byte(`[]`)))
	mgmtMock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mgmtMock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	// Source DB: the table query the consumer runs.
	srcDB, srcMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock src: %v", err)
	}
	defer srcDB.Close()
	srcMock.ExpectQuery("SELECT \\* FROM widgets").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).
			AddRow(1, "alpha").
			AddRow(2, "beta"))

	c := &dbConsumer{
		openSource: func(string) (*sql.DB, error) { return srcDB, nil },
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "db-consumer", DB: mgmtDB})

	// Give the command subscription a moment to register, then fire the start command.
	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start command: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	if got.ContentType != "application/json" {
		t.Errorf("content-type = %q", got.ContentType)
	}
	// The payload is the JSON array of the two source rows.
	var rows []map[string]any
	if err := json.Unmarshal(got.Payload, &rows); err != nil {
		t.Fatalf("payload is not a JSON array: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows in payload, got %d", len(rows))
	}
}
