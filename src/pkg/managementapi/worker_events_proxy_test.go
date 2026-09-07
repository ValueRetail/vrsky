package managementapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// newProxyRequest builds a request the way the mux would: tenant in context,
// {id} and {worker} as path values.
func newProxyRequest(t *testing.T, tenantID, connID, worker string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/connections/"+connID+"/workers/"+worker+"/events", nil)
	r.SetPathValue("id", connID)
	r.SetPathValue("worker", worker)
	return r.WithContext(ContextWithTenantID(context.Background(), tenantID))
}

// pointWorkerAt redirects one allowlist entry at a test server for the length
// of a test. The allowlist is the only place an upstream address comes from,
// which is what makes the proxy safe — and also what makes this the natural
// seam for a test.
func pointWorkerAt(t *testing.T, worker string, srv *httptest.Server) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("parse test server URL %q: %v", srv.URL, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	orig, existed := workerEventSources[worker]
	workerEventSources[worker] = workerEventSource{service: host, port: port}
	t.Cleanup(func() {
		if existed {
			workerEventSources[worker] = orig
		} else {
			delete(workerEventSources, worker)
		}
	})
}

// TestProxyWorkerEvents_StreamsForOwnedConnection is the happy path: the
// connection belongs to the caller's tenant, so the worker's frames come
// through with SSE headers.
func TestProxyWorkerEvents_StreamsForOwnedConnection(t *testing.T) {
	const (
		tenant = "tenant-a"
		connID = "11111111-1111-1111-1111-111111111111"
	)

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"connected\"}\n\ndata: {\"type\":\"message\"}\n\n"))
	}))
	defer upstream.Close()
	pointWorkerAt(t, "data-filter", upstream)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT id::text FROM connections").
		WithArgs(connID, tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(connID))

	h := &Handler{db: db}
	rec := httptest.NewRecorder()
	h.ProxyWorkerEvents(rec, newProxyRequest(t, tenant, connID, "data-filter"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	// Without this, nginx buffers the stream and the panel updates in bursts
	// or not at all — the failure the whole feature is trying to avoid.
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Error("X-Accel-Buffering not set to no — a proxy will buffer the stream")
	}
	if !strings.Contains(rec.Body.String(), `"type":"message"`) {
		t.Errorf("upstream frames did not reach the client, got: %q", rec.Body.String())
	}
	if gotPath != "/events/"+connID {
		t.Errorf("upstream path = %q, want /events/%s", gotPath, connID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("db expectations: %v", err)
	}
}

// TestProxyWorkerEvents_RefusesForeignConnection is the security case.
//
// The worker endpoints have no auth and no tenant check of their own, so this
// ownership check is the only thing standing between an authenticated user and
// another workspace's live payloads. If it regresses, the proxy hands them over
// on request.
func TestProxyWorkerEvents_RefusesForeignConnection(t *testing.T) {
	const connID = "22222222-2222-2222-2222-222222222222"

	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"secret\":\"other tenant's payload\"}\n\n"))
	}))
	defer upstream.Close()
	pointWorkerAt(t, "data-filter", upstream)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	// sqlmock does not execute SQL, so asserting only on the 404 would pass
	// even if the WHERE clause dropped its tenant filter entirely — the mock
	// returns whatever it is told regardless. The expectation therefore pins
	// the query TEXT, tenant predicate included, and the check that it was met
	// is what makes this test about isolation rather than about sqlmock.
	mock.ExpectQuery(`SELECT id::text FROM connections WHERE id::text = \$1 AND tenant_id::text = \$2`).
		WithArgs(connID, "tenant-intruder").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	h := &Handler{db: db}
	rec := httptest.NewRecorder()
	h.ProxyWorkerEvents(rec, newProxyRequest(t, "tenant-intruder", connID, "data-filter"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if reached {
		t.Error("the worker was contacted for a connection the caller does not own")
	}
	if strings.Contains(rec.Body.String(), "other tenant") {
		t.Error("another tenant's payload reached the response body")
	}
	// 404 rather than 403: a 403 would confirm that this connection ID exists
	// somewhere, which is itself information about another workspace.
	if strings.Contains(rec.Body.String(), "Forbidden") {
		t.Error("response distinguishes 'not yours' from 'not found'")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the ownership query is not the tenant-scoped one this test requires: %v", err)
	}
}

// TestProxyWorkerEvents_RejectsWorkerNotOnAllowlist: the upstream address is
// built from the allowlist, never from the request. A caller-supplied target
// would make this an SSRF endpoint inside the cluster network.
func TestProxyWorkerEvents_RejectsWorkerNotOnAllowlist(t *testing.T) {
	for _, worker := range []string{
		"nats",                   // a real in-cluster service, not on the list
		"../../secrets",          // path traversal into another route
		"http://169.254.169.254", // cloud instance metadata
		"",
	} {
		t.Run(worker, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			// No query expected: the allowlist check comes first, so an
			// unknown worker never reaches the database either.

			h := &Handler{db: db}
			rec := httptest.NewRecorder()
			h.ProxyWorkerEvents(rec, newProxyRequest(t, "tenant-a", "some-id", worker))

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for worker %q", rec.Code, worker)
			}
			// The specific code matters. A handler that accepted the name and
			// then failed later would also return 404 — as ConnectionNotFound,
			// having already built an upstream URL out of caller input.
			if !strings.Contains(rec.Body.String(), "UnknownWorker") {
				t.Errorf("worker %q was not rejected by the allowlist; body: %s", worker, rec.Body.String())
			}
			_ = mock
		})
	}
}

// TestWorkerEventSourcesMatchUI pins the Go allowlist to the TypeScript union
// the UI sends. A name in one and not the other is a panel that 404s at
// runtime with nothing in either codebase looking wrong.
func TestWorkerEventSourcesMatchUI(t *testing.T) {
	src, err := readRepoFile("ui", "src", "services", "workerEvents.ts")
	if err != nil {
		t.Skipf("UI client not available (%v) — allowlist drift guard skipped", err)
	}

	// The union members, as quoted string literals in the EventWorker type.
	block := src
	if i := strings.Index(block, "export type EventWorker"); i >= 0 {
		block = block[i:]
		if j := strings.Index(block, "\n\n"); j >= 0 {
			block = block[:j]
		}
	} else {
		t.Fatal("EventWorker type not found in workerEvents.ts — update this test")
	}

	uiWorkers := map[string]bool{}
	for _, part := range strings.Split(block, "'") {
		if strings.Contains(part, "-") && !strings.Contains(part, " ") {
			uiWorkers[part] = true
		}
	}
	if len(uiWorkers) == 0 {
		t.Fatal("parsed no workers from the EventWorker union — update this test")
	}

	for name := range workerEventSources {
		if !uiWorkers[name] {
			t.Errorf("worker %q is proxied by the API but missing from the UI's EventWorker union", name)
		}
	}
	for name := range uiWorkers {
		if _, ok := workerEventSources[name]; !ok {
			t.Errorf("the UI can request worker %q, which is not on the API allowlist — that panel would 404", name)
		}
	}
}

// readRepoFile reads a path relative to the repository root, which is three
// levels up from this package.
func readRepoFile(parts ...string) (string, error) {
	p := filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
	b, err := os.ReadFile(p)
	return string(b), err
}
