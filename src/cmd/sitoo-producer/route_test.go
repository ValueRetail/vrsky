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
// front of this destination (the "ERP inventory → POS" template, #435) the
// producer receives the source's raw page AND the converter's result, and
// must write only the result. Before the gate both reached Sitoo.
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
		{"converter in front: converted page is written", viaConverter, "conv", true},
		{"converter in front: another node's output is skipped", viaConverter, "other", false},
		{"source wired straight in: source page is written", direct, "", true},
		{"source wired straight in: a converter's output elsewhere is skipped", direct, "conv", false},
		{"no edges stored: everything is written, as before", `[]`, "conv", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var writes int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&writes, 1)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			nodes := fmt.Sprintf(`[
				{"id":"src","type":"consumer","config":{"type":"business_central","business_central":{}}},
				{"id":"conv","type":"converter","config":{"mappings":[]}},
				{"id":"dst","type":"producer","config":{"type":"sitoo","sitoo":{
					"account_id":1,"site_id":2,"api_id":"id","api_password":"pw",
					"base_url":%q,"resource":"warehouses/1/warehouseitems"}}}]`, srv.URL)
			mock.ExpectQuery("SELECT nodes, edges FROM connections").
				WithArgs("conn-1", "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(tc.edges)))

			p := testProducer()
			p.db = db
			env := &envelope.Envelope{
				TenantID: "tenant-1", IntegrationID: "conn-1",
				Payload: []byte(`[{"sku":"1896-S","decimaltotal":"12"}]`),
			}
			if tc.lastBy != "" {
				env.Metadata = map[string]interface{}{"_last_processed_by": tc.lastBy}
			}
			if err := p.Deliver(context.Background(), env); err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if got := atomic.LoadInt32(&writes) == 1; got != tc.wantWrite {
				t.Errorf("wrote to Sitoo = %v, want %v", got, tc.wantWrite)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}
