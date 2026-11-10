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

	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// Where a message ends up after the target refuses it (#734). A 401 or 403
// is about the node's credential, and a URL no request can be built from
// about its config: both are retried and then kept in the DLQ, so the
// message can be replayed once the node is fixed. Any other 4xx is about the
// message itself and is acked, as before.
func TestHTTPProducer_RefusedMessageEndsInDLQOrIsAcked(t *testing.T) {
	cases := []struct {
		name    string
		status  int    // what the target answers; 0: no target
		url     string // used when status is 0
		oauth   bool   // auth_type=oauth with a grant
		tls     string // the node's tls block, if any
		wantDLQ bool
		want    string // in X-Vrsky-Last-Error
		hits    int32  // requests the target gets
	}{
		{name: "401", status: http.StatusUnauthorized, wantDLQ: true, want: "upstream 401", hits: 5},
		{name: "403", status: http.StatusForbidden, wantDLQ: true, want: "upstream 403", hits: 5},
		// The OAuth path refreshes the token once per attempt; a 401 after
		// that used to count as taken.
		{name: "401 after the OAuth refresh", status: http.StatusUnauthorized, oauth: true, wantDLQ: true, want: "upstream 401", hits: 10},
		{name: "URL no request can be built from", url: "http://host:80/%zz", wantDLQ: true, want: "cannot build request"},
		// A node with no URL used to count as "not ours" and was acked.
		{name: "no URL", url: "", wantDLQ: true, want: "destination refused"},
		// So did a node whose mTLS client cannot be built; nothing is sent
		// without the certificate.
		{name: "mTLS cert that does not parse", status: http.StatusOK, tls: `{"cert":"not a cert","key":"not a key"}`, wantDLQ: true, want: "build mTLS client"},
		{name: "400", status: http.StatusBadRequest, hits: 1},
		{name: "404", status: http.StatusNotFound, hits: 1},
		{name: "422", status: http.StatusUnprocessableEntity, hits: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowLocalTarget(t)
			var hits atomic.Int32
			url := tc.url
			if tc.status != 0 {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.WriteHeader(tc.status)
				}))
				t.Cleanup(srv.Close)
				url = srv.URL
			}

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			const connID = "conn-http-replay"
			auth := ""
			if tc.oauth {
				auth = `,"auth_type":"oauth","oauth_grant_id":"g1"`
			}
			tlsBlock := ""
			if tc.tls != "" {
				tlsBlock = `,"tls":` + tc.tls
			}
			nodes := fmt.Sprintf(`[{"id":"prod1","type":"producer","config":{"type":"http","http":{"url":%q,"method":"POST"%s}%s}}]`, url, auth, tlsBlock)
			mock.MatchExpectationsInOrder(false)
			for i := 0; i < 5; i++ {
				mock.ExpectQuery("FROM connections WHERE id").
					WithArgs(connID).
					WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
			}

			p := &httpProducer{
				resolveToken: func(context.Context, string, string, bool) (string, error) { return "tok", nil },
			}
			h := harness.NewProducerHarness(t, p, harness.Options{Name: "http-producer", DB: db, Backoff: fastBackoff})
			lastErr := dlqErrors(t, h.NATS())
			env := fanOutEnv(connID)
			h.Publish(t, env)

			if tc.wantDLQ {
				got := h.ExpectDLQ(t, 15*time.Second)
				if got.ID != env.ID || string(got.Payload) != string(env.Payload) {
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
				harness.Eventually(t, 5*time.Second, "target answered", func() bool { return hits.Load() >= 1 })
				h.ExpectNoDLQ(t, time.Second)
			}
			if n := hits.Load(); n != tc.hits {
				t.Errorf("target received %d requests, want %d", n, tc.hits)
			}
		})
	}
}
