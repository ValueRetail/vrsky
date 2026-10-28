package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Every node of a pipeline publishes on the same subject. With a converter in
// front of this destination (the "POS sales → ERP" template, #435) the
// producer receives the source's raw Sitoo page AND the converter's sales
// orders, and must write only the orders. Before the gate the raw page
// reached Business Central too.
func TestDeliver_TakesOnlyItsPredecessorsOutput(t *testing.T) {
	const viaConverter = `[{"source":"src","target":"conv"},{"source":"conv","target":"dst"}]`
	const direct = `[{"source":"src","target":"dst"}]`
	cases := []struct {
		name      string
		edges     string
		lastBy    string // _last_processed_by on the envelope; "" = straight from the source
		wantWrite bool
	}{
		{"converter in front: raw source page is skipped", viaConverter, "", false},
		{"converter in front: converted order is written", viaConverter, "conv", true},
		{"converter in front: another node's output is skipped", viaConverter, "other", false},
		{"source wired straight in: source record is written", direct, "", true},
		{"source wired straight in: a converter's output elsewhere is skipped", direct, "conv", false},
		{"no edges stored: everything is written, as before", `[]`, "conv", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := tokenServer(t)
			defer tok.Close()
			var writes int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					atomic.AddInt32(&writes, 1)
				}
				w.WriteHeader(http.StatusCreated)
			}))
			defer api.Close()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			nodes := fmt.Sprintf(`[
				{"id":"src","type":"consumer","config":{"type":"sitoo","sitoo":{}}},
				{"id":"conv","type":"converter","config":{"mappings":[]}},
				{"id":"dst","type":"producer","config":{"type":"business_central","business_central":{
					"aad_tenant_id":"t","company_id":"GUID","client_id":"cid","client_secret":"sec",
					"entity":"salesInvoices","api_base_url":%q,"token_url":%q}}}]`, api.URL, tok.URL)
			mock.ExpectQuery("SELECT nodes, edges FROM connections").
				WithArgs("conn-1", "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(tc.edges)))

			p := testProducer()
			p.db = db
			env := &envelope.Envelope{
				TenantID: "tenant-1", IntegrationID: "conn-1",
				Payload: []byte(`{"customerNumber":"C00010","externalDocumentNumber":"S1-2026-000412"}`),
			}
			if tc.lastBy != "" {
				env.Metadata = map[string]interface{}{"_last_processed_by": tc.lastBy}
			}
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if got := atomic.LoadInt32(&writes) == 1; got != tc.wantWrite {
				t.Errorf("wrote to Business Central = %v, want %v", got, tc.wantWrite)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}
