package main

import (
	"context"
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

// TestHTTPProducer_OAuthRefreshOn401 verifies the #97 OAuth output path: an
// auth_type=oauth node attaches a Bearer token; on a 401 the producer refreshes
// the token (force) and retries once, succeeding without dead-lettering.
func TestHTTPProducer_OAuthRefreshOn401(t *testing.T) {
	var calls int32
	var lastAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		lastAuth.Store(r.Header.Get("Authorization"))
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized) // stale token
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	const connID = "conn-oauth"
	nodes := fmt.Sprintf(`[{"id":"prod1","type":"producer","config":{"type":"http","http":{"url":%q,"method":"POST","auth_type":"oauth","oauth_grant_id":"g1"}}}]`, srv.URL)
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).
				AddRow([]byte(nodes), []byte(`[]`)))
	}

	var forced atomic.Bool
	p := &httpProducer{
		resolveToken: func(_ context.Context, _, grantID string, force bool) (string, error) {
			if force {
				forced.Store(true)
				return "tok-refreshed", nil
			}
			return "tok-stale", nil
		},
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "http-producer", DB: db})

	env := envelope.New()
	env.ID = "oauth-env-1"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.Payload = []byte(`{"x":1}`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "endpoint hit twice (401 then 200)", func() bool {
		return atomic.LoadInt32(&calls) >= 2
	})
	time.Sleep(400 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected exactly 2 requests (401 then refreshed 200), got %d", got)
	}
	if !forced.Load() {
		t.Error("expected a forced token refresh on the 401 retry")
	}
	if a := lastAuth.Load(); a == nil || a.(string) != "Bearer tok-refreshed" {
		t.Errorf("retry Authorization = %v, want Bearer tok-refreshed", a)
	}
}

// TestHTTPProducer_OAuthEmptyGrantNoCall verifies the empty-grant guard: an
// auth_type=oauth node whose grant was cleared (e.g. revoked) must fail fast
// without ever resolving a token or hitting the destination — instead of
// calling /oauth/grants//token with an empty id and getting an opaque 500.
func TestHTTPProducer_OAuthEmptyGrantNoCall(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	const connID = "conn-oauth-empty"
	// auth_type=oauth but oauth_grant_id is absent (cleared on revoke).
	nodes := fmt.Sprintf(`[{"id":"prod1","type":"producer","config":{"type":"http","http":{"url":%q,"method":"POST","auth_type":"oauth"}}}]`, srv.URL)
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 5; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).
				AddRow([]byte(nodes), []byte(`[]`)))
	}

	var resolveCalls atomic.Int32
	p := &httpProducer{
		resolveToken: func(_ context.Context, _, _ string, _ bool) (string, error) {
			resolveCalls.Add(1)
			return "should-not-be-used", nil
		},
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "http-producer", DB: db})

	env := envelope.New()
	env.ID = "oauth-empty-1"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.Payload = []byte(`{"x":1}`)
	h.Publish(t, env)

	// Give the runner time to (not) act.
	time.Sleep(700 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("destination was hit %d times; expected 0 (no grant → no send)", got)
	}
	if got := resolveCalls.Load(); got != 0 {
		t.Errorf("resolveToken called %d times; expected 0 (guard should short-circuit before token resolution)", got)
	}
}
