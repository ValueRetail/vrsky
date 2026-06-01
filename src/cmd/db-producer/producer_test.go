package main

import (
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
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
