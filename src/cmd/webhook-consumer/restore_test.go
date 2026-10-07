package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A webhook pipeline the database says is running is served again the
// moment the consumer is up — no start command, no redeploy
// (plans/stable-connections.md). A stopped one is not.
func TestWebhookConsumer_RestoresOnBoot(t *testing.T) {
	const (
		running = "conn-restored"
		stopped = "conn-stopped"
		tenant  = "tenant-r"
	)
	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)

	// The boot scan: only the running row comes back from the database.
	mock.ExpectQuery("SELECT id::text, tenant_id FROM connections").WithArgs("http").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow(running, tenant))
	nodes := `[{"id":"c1","type":"consumer","config":{"type":"http","http":{}}}]`
	mock.ExpectQuery("SELECT id, tenant_id, name, nodes, edges FROM connections").WithArgs(running, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "name", "nodes", "edges"}).
			AddRow(running, tenant, "WH", []byte(nodes), []byte(`[]`)))
	mock.ExpectExec("UPDATE connections SET status").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE connections SET last_payload").WillReturnResult(sqlmock.NewResult(0, 1))

	c := &webhookConsumer{}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "webhook-consumer", DB: mgmtDB})
	// Configure runs on the harness goroutine and allocates the active map;
	// give it the moment the other tests give it before reading that map.
	time.Sleep(200 * time.Millisecond)
	// No start command is published. The webhook is reachable anyway.
	harness.Eventually(t, 5*time.Second, "webhook restored from the database", func() bool {
		return c.getActiveConnection(running) != nil
	})
	if c.getActiveConnection(stopped) != nil {
		t.Fatal("a pipeline the database does not list as running was registered")
	}

	req := httptest.NewRequest("POST", "/webhook/"+running, strings.NewReader(`{"after":"restart"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleWebhook()(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restored webhook: want 202, got %d %s", rec.Code, rec.Body.String())
	}
	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 5*time.Second)
	if got.IntegrationID != running {
		t.Fatalf("published for %s, want %s", got.IntegrationID, running)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("database: %v", err)
	}
}
