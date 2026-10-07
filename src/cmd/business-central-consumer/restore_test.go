package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// A Business Central poller the database says is running polls again on boot,
// with no start command (plans/stable-connections.md).
func TestBCConsumer_RestoresOnBoot(t *testing.T) {
	const (
		connID = "conn-bc-restored"
		tenant = "tenant-r"
	)
	tok := bcTestToken(t)
	defer tok.Close()
	var polls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls.Add(1)
		fmt.Fprint(w, `{"value":[{"id":"1","number":"IT-1","lastModifiedDateTime":"2026-10-07T10:00:00Z"}]}`)
	}))
	defer api.Close()

	mgmtDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mgmtDB.Close()
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("SELECT id::text, tenant_id FROM connections").WithArgs("business_central").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow(connID, tenant))
	nodes := fmt.Sprintf(`[{"id":"in","type":"consumer","config":{"type":"business_central","business_central":{
		"aad_tenant_id":"t","company_id":"GUID","client_id":"cid","client_secret":"sec","entity":"items",
		"api_base_url":%q,"token_url":%q,"poll_interval_seconds":1}}}]`, api.URL, tok.URL)
	mock.ExpectQuery("SELECT nodes FROM connections").WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"nodes"}).AddRow([]byte(nodes)))

	// Checkpoints in memory, as the other tests here keep them; the restore
	// path under test is the boot scan and the start that follows it.
	c := &bcConsumer{checkpoints: checkpoint.NewInMemoryStore()}
	h := harness.NewConsumerHarness(t, c, harness.Options{Name: "business-central-consumer", DB: mgmtDB})
	got := h.ExpectEnvelope(t, harness.MatchTenant(tenant), 10*time.Second)
	if got.IntegrationID != connID {
		t.Fatalf("published for %s, want %s", got.IntegrationID, connID)
	}
	if polls.Load() == 0 {
		t.Fatal("Business Central was never polled")
	}
}
