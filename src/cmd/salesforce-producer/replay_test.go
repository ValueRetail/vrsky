package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Where a message ends up when it cannot be written (#734). A node with no
// object used to ack (drop) every message; like an upsert with no
// external_id_field it is retried and then kept in the DLQ, so the message
// can be replayed once the node is fixed. A payload that is not JSON is about
// the message itself and is still acked with no DLQ copy.
func TestSalesforceProducer_ConfigErrorEndsInDLQ(t *testing.T) {
	cases := []struct {
		name    string
		node    string // the node's salesforce block, %s is the instance URL
		payload string
		wantDLQ bool
		want    string // in X-Vrsky-Last-Error
		writes  int32
		// chase publishes a valid message after the first and waits for its
		// write, so an acked message is known to have been handled before
		// ExpectNoDLQ looks.
		chase bool
	}{
		{name: "no object", node: `{"instance_url":%q,"oauth_grant_id":"g1","operation":"insert"}`, payload: `{"Name":"Acme"}`, wantDLQ: true, want: "salesforce node has no object"},
		{name: "upsert with no external_id_field", node: `{"instance_url":%q,"oauth_grant_id":"g1","object":"Account","operation":"upsert"}`, payload: `{"Name":"Acme"}`, wantDLQ: true, want: "has no external_id_field"},
		{name: "payload not JSON", node: `{"instance_url":%q,"oauth_grant_id":"g1","object":"Account","operation":"insert"}`, payload: `not json`, chase: true, writes: 1},
		{name: "complete", node: `{"instance_url":%q,"oauth_grant_id":"g1","object":"Account","operation":"insert"}`, payload: `{"Name":"Acme"}`, writes: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"001","success":true}`))
			}))
			t.Cleanup(srv.Close)

			const connID = "sfp-replay"
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.MatchExpectationsInOrder(false)
			nodes := `[{"id":"p1","type":"producer","config":{"type":"salesforce","salesforce":` + fmt.Sprintf(tc.node, srv.URL) + `}}]`
			for i := 0; i < 5; i++ {
				mock.ExpectQuery("FROM connections WHERE id").
					WithArgs(connID).
					WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
			}

			p := &salesforceProducer{
				resolveToken: func(context.Context, string, string, bool) (string, error) { return "tok", nil },
			}
			fast := []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}
			h := harness.NewProducerHarness(t, p, harness.Options{Name: "salesforce-producer", DB: db, Backoff: fast})
			lastErr := dlqLastErrors(t, h.NATS())

			env := envelope.New()
			env.IntegrationID = connID
			env.TenantID = "tenant-1"
			env.Payload = []byte(tc.payload)
			h.Publish(t, env)

			if tc.wantDLQ {
				got := h.ExpectDLQ(t, 15*time.Second)
				if got.ID != env.ID || string(got.Payload) != tc.payload {
					t.Fatalf("DLQ holds %s %q, want %s with its payload", got.ID, got.Payload, env.ID)
				}
				select {
				case e := <-lastErr:
					if !strings.Contains(e, tc.want) {
						t.Errorf("X-Vrsky-Last-Error = %q, want it to contain %q", e, tc.want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("no X-Vrsky-Last-Error on the DLQ copy")
				}
			} else {
				if tc.chase {
					next := envelope.New()
					next.IntegrationID = connID
					next.TenantID = "tenant-1"
					next.Payload = []byte(`{"Name":"After"}`)
					h.Publish(t, next)
				}
				if tc.writes > 0 {
					harness.Eventually(t, 5*time.Second, "record written", func() bool { return writes.Load() >= tc.writes })
				}
				h.ExpectNoDLQ(t, time.Second)
			}
			if n := writes.Load(); n != tc.writes {
				t.Errorf("Salesforce received %d writes, want %d", n, tc.writes)
			}
		})
	}
}
