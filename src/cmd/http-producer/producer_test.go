package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// mockConnRows seeds the management-DB query getHTTPConfigs runs, returning a
// single producer node pointing at url. The cache may query more than once
// across a run, so repeat the expectation.
func mockConnRows(mock sqlmock.Sqlmock, connID, url string) {
	nodes := fmt.Sprintf(`[{"id":"prod1","type":"producer","config":{"type":"http","http":{"url":%q,"method":"POST"}}}]`, url)
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).
				AddRow([]byte(nodes), []byte(`[]`)))
	}
}

// TestHTTPProducer_RoundTrip proves the SDK refactor end-to-end with zero
// Docker: an envelope published into embedded JetStream flows through the SDK
// runner → httpProducer.Deliver → an HTTP POST to a test endpoint, with
// per-connection config served from a mocked database.
func TestHTTPProducer_RoundTrip(t *testing.T) {
	var gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	const connID = "conn-http-1"
	mockConnRows(mock, connID, srv.URL)

	p := &httpProducer{}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "http-producer", DB: db})

	env := envelope.New()
	env.ID = "http-env-1"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.ContentType = "application/json"
	env.Payload = []byte(`{"hello":"http"}`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "endpoint received POST", func() bool {
		v := gotBody.Load()
		return v != nil && v.(string) == `{"hello":"http"}`
	})
}

// TestHTTPProducer_4xxIsAcked verifies a 4xx response does not retry: the
// message is acked (no DLQ), since retrying a malformed request can't help.
func TestHTTPProducer_4xxIsAcked(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	const connID = "conn-http-4xx"
	mockConnRows(mock, connID, srv.URL)

	p := &httpProducer{}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "http-producer", DB: db})

	env := envelope.New()
	env.ID = "http-env-4xx"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.Payload = []byte(`{"x":1}`)
	h.Publish(t, env)

	// The endpoint should be hit exactly once (no redelivery). Give the runner
	// a moment, then confirm it stayed at one hit.
	harness.Eventually(t, 3*time.Second, "endpoint hit once", func() bool {
		return atomic.LoadInt32(&hits) >= 1
	})
	time.Sleep(500 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected exactly 1 request (4xx acked, no retry), got %d", got)
	}
}
