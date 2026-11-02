package managementapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseWriteTimeout stands in for the management API's 30 s WriteTimeout.
const sseWriteTimeout = 150 * time.Millisecond

// startSSEServer serves handler on a real server whose WriteTimeout is short
// enough to pass during a test. httptest.ResponseRecorder has no deadline, so
// only a real connection shows whether a stream survives it.
func startSSEServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.WriteTimeout = sseWriteTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// openSSE opens the stream and returns the response; the request is cancelled
// when the test ends.
func openSSE(t *testing.T, url string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	return resp
}

// waitForFrame reads the stream until a line contains want. It fails when the
// stream ends first, which is what a stream cut at the WriteTimeout does.
func waitForFrame(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	sc := bufio.NewScanner(resp.Body)
	var seen []string
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			seen = append(seen, line)
			if strings.Contains(line, want) {
				return
			}
		}
	}
	t.Fatalf("stream ended without a frame containing %q (err: %v); got: %q", want, sc.Err(), seen)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertSSEHeaders(t *testing.T, resp *http.Response) {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("X-Accel-Buffering not set to no — a proxy will buffer the stream")
	}
}

// TestMetricsStream_OutlivesWriteTimeout: the live metrics stream is one
// long response. A metrics update published after the server's WriteTimeout
// has passed must still reach the browser.
func TestMetricsStream_OutlivesWriteTimeout(t *testing.T) {
	const (
		tenant = "tenant-a"
		connID = "conn-1"
	)
	repo := NewMockRepository()
	repo.connections[connID] = &Connection{ID: connID, TenantID: tenant}
	h := NewHandler(repo, nil)
	registry := NewClientRegistry()
	h.InitializeWebSocketSupport(registry, nil)

	srv := startSSEServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", connID)
		h.HandleMetricsWebSocket(w, r.WithContext(ContextWithTenantID(r.Context(), tenant)))
	}))
	resp := openSSE(t, srv.URL)
	assertSSEHeaders(t, resp)
	waitForFrame(t, resp, `"type":"connected"`)

	waitUntil(t, "the client to register", func() bool { return registry.GetConnectionClientCount(connID) == 1 })
	time.Sleep(3 * sseWriteTimeout)
	registry.BroadcastToConnection(connID, []byte(`{"type":"metrics","data":"late"}`))

	waitForFrame(t, resp, `"late"`)
}

// TestMetricsStreamRouteRemoved: GET .../metrics/stream was dropped in #651
// because no client read it. The route must stay gone, while the polled
// snapshot at .../metrics and the stream at .../metrics/ws stay mounted.
func TestMetricsStreamRouteRemoved(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(NewMockRepository(), nil).RegisterRoutes(mux)

	for _, tc := range []struct {
		path        string
		wantMounted bool
	}{
		{"/api/v1/connections/conn-1/metrics/stream", false},
		{"/api/v1/connections/conn-1/metrics", true},
		{"/api/v1/connections/conn-1/metrics/ws", true},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		_, pattern := mux.Handler(req)
		if mounted := pattern != ""; mounted != tc.wantMounted {
			t.Errorf("GET %s: mounted = %v (pattern %q), want %v", tc.path, mounted, pattern, tc.wantMounted)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/connections/conn-1/metrics/stream", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET metrics/stream = %d, want 404", rec.Code)
	}
}

// TestTenantStatusSSE_OutlivesWriteTimeout: provisioning a workspace takes
// longer than the WriteTimeout, and the progress page follows it on this
// stream. An update broadcast after the timeout must still arrive.
func TestTenantStatusSSE_OutlivesWriteTimeout(t *testing.T) {
	tenant := &Tenant{ID: "tenant-a", Slug: "a", Status: "provisioning"}
	h := NewHandler(NewMockRepository(), nil)
	hub := NewTenantSSEHub()
	h.SetTenantSSEHub(hub)

	srv := startSSEServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleTenantStatusSSE(w, r.WithContext(ContextWithTenant(r.Context(), tenant, "admin")))
	}))
	resp := openSSE(t, srv.URL)
	assertSSEHeaders(t, resp)
	waitForFrame(t, resp, `"type":"status"`)

	waitUntil(t, "the client to subscribe", func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return len(hub.clients[tenant.ID]) == 1
	})
	time.Sleep(3 * sseWriteTimeout)
	hub.Broadcast(tenant.ID, ProvisioningStatusUpdate{TenantID: tenant.ID, CurrentStep: "late step"})

	waitForFrame(t, resp, "late step")
}
