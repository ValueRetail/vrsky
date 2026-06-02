package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// TestSalesforceConsumer_RoundTrip drives the SDK-refactored salesforce-consumer
// end-to-end with zero Docker: a connection start command makes it read its
// config from a mocked management DB, run a SOQL query against a fake Salesforce
// REST endpoint (token resolution stubbed), and publish the records onto the
// data stream.
func TestSalesforceConsumer_RoundTrip(t *testing.T) {
	const (
		connID = "sf-conn-1"
		tenant = "tenant-x"
	)

	// Fake Salesforce query endpoint.
	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalSize":2,"done":true,"records":[{"Id":"001","Name":"Acme"},{"Id":"002","Name":"Globex"}]}`))
	}))
	defer srv.Close()

	// Management DB: connection lookup + status write.
	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	nodes := fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"salesforce","salesforce":{"instance_url":%q,"oauth_grant_id":"g1","soql":"SELECT Id, Name FROM Account"}}}]`, srv.URL)
	for i := 0; i < 2; i++ {
		mock.ExpectQuery("SELECT nodes FROM connections").
			WithArgs(connID, tenant).
			WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))
	}
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &salesforceConsumer{
		resolveToken: func(context.Context, string, string, bool) (string, error) { return "fake-token", nil },
	}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "salesforce-consumer", DB: mgmtDB})

	time.Sleep(200 * time.Millisecond)
	cmd, _ := json.Marshal(map[string]string{"connection_id": connID, "tenant_id": tenant})
	if err := h.NATS().Publish("vrsky.commands."+tenant+".connection.start", cmd); err != nil {
		t.Fatalf("publish start: %v", err)
	}

	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != connID {
		t.Errorf("integration id = %q, want %q", got.IntegrationID, connID)
	}
	var records []map[string]any
	if err := json.Unmarshal(got.Payload, &records); err != nil {
		t.Fatalf("payload not a JSON array: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("expected 2 records, got %d", len(records))
	}
	if gotAuth != "Bearer fake-token" {
		t.Errorf("Salesforce request auth = %q, want Bearer fake-token", gotAuth)
	}
	if gotQuery != "SELECT Id, Name FROM Account" {
		t.Errorf("SOQL = %q", gotQuery)
	}
}
