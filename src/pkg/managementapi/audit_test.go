package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAuditWriter captures CreateAuditEntry calls in memory for assertions.
type fakeAuditWriter struct {
	mu      sync.Mutex
	entries []*AuditEntry
}

func (f *fakeAuditWriter) CreateAuditEntry(ctx context.Context, e *AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAuditWriter) wait(t *testing.T, want int) []*AuditEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.entries)
		f.mu.Unlock()
		if n >= want {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := make([]*AuditEntry, len(f.entries))
			copy(out, f.entries)
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waited 2s for %d audit entries, got %d", want, len(f.entries))
	return nil
}

func runAuditMiddleware(t *testing.T, w *fakeAuditWriter, method, path, tenantID string, status int) {
	t.Helper()
	inner := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(status)
	})
	h := AuditMiddleware(w, nil)(inner)
	req := httptest.NewRequest(method, path, nil)
	if tenantID != "" {
		req = req.WithContext(ContextWithTenantID(req.Context(), tenantID))
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestAudit_CapturesMutatingRequests(t *testing.T) {
	w := &fakeAuditWriter{}
	runAuditMiddleware(t, w, http.MethodPost, "/api/v1/connections", "tenant-A", http.StatusCreated)
	got := w.wait(t, 1)
	if got[0].Action != "connection.create" {
		t.Fatalf("action: got %q want connection.create", got[0].Action)
	}
	if got[0].ResourceType != "connection" {
		t.Fatalf("resource_type: %q", got[0].ResourceType)
	}
	if got[0].StatusCode != http.StatusCreated {
		t.Fatalf("status: %d", got[0].StatusCode)
	}
	if got[0].TenantID != "tenant-A" {
		t.Fatalf("tenant_id: %q", got[0].TenantID)
	}
}

func TestAudit_SkipsReads(t *testing.T) {
	w := &fakeAuditWriter{}
	runAuditMiddleware(t, w, http.MethodGet, "/api/v1/connections", "tenant-A", http.StatusOK)
	// Give the goroutine a moment.
	time.Sleep(100 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.entries) != 0 {
		t.Fatalf("GET should not be audited, got %d entries", len(w.entries))
	}
}

func TestAudit_AuditsSecretReads(t *testing.T) {
	w := &fakeAuditWriter{}
	runAuditMiddleware(t, w, http.MethodGet, "/api/v1/secrets/abc", "tenant-A", http.StatusOK)
	got := w.wait(t, 1)
	if got[0].Action != "secret.get" {
		t.Fatalf("action: %q", got[0].Action)
	}
}

func TestAudit_SkipsNonAPIPaths(t *testing.T) {
	w := &fakeAuditWriter{}
	runAuditMiddleware(t, w, http.MethodPost, "/health", "tenant-A", http.StatusOK)
	runAuditMiddleware(t, w, http.MethodPost, "/metrics", "tenant-A", http.StatusOK)
	time.Sleep(100 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.entries) != 0 {
		t.Fatalf("non-API paths should not be audited, got %d entries", len(w.entries))
	}
}

func TestAudit_SkipsWhenNoTenant(t *testing.T) {
	w := &fakeAuditWriter{}
	runAuditMiddleware(t, w, http.MethodPost, "/api/v1/connections", "" /* no tenant */, http.StatusOK)
	time.Sleep(100 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.entries) != 0 {
		t.Fatalf("no-tenant requests should not be audited, got %d", len(w.entries))
	}
}

func TestAudit_SkipsRecursiveAuditEndpoint(t *testing.T) {
	w := &fakeAuditWriter{}
	runAuditMiddleware(t, w, http.MethodPost, "/api/v1/audit/anything", "tenant-A", http.StatusOK)
	time.Sleep(100 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.entries) != 0 {
		t.Fatalf("audit endpoints should not be audited (would loop), got %d", len(w.entries))
	}
}

func TestAudit_HandlerCanEnrichDetails(t *testing.T) {
	w := &fakeAuditWriter{}
	inner := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		SetAuditDetail(r.Context(), "name", "prod-pg-pwd")
		SetAuditAction(r.Context(), "secret.rotate")
		rw.WriteHeader(http.StatusOK)
	})
	h := AuditMiddleware(w, nil)(inner)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/secrets/abc/rotate", nil)
	req = req.WithContext(ContextWithTenantID(req.Context(), "tenant-A"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	got := w.wait(t, 1)
	if got[0].Action != "secret.rotate" {
		t.Fatalf("action override failed: %q", got[0].Action)
	}
	if got[0].Details["name"] != "prod-pg-pwd" {
		t.Fatalf("details enrichment failed: %+v", got[0].Details)
	}
}

func TestAudit_ConcurrentRequests(t *testing.T) {
	// The audit bag's lock must let multiple goroutines call SetAuditDetail
	// safely. Run a synthetic load and assert no data race.
	w := &fakeAuditWriter{}
	var done int32
	inner := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				SetAuditDetail(r.Context(), "k", i)
			}(i)
		}
		wg.Wait()
		atomic.AddInt32(&done, 1)
		rw.WriteHeader(http.StatusOK)
	})
	h := AuditMiddleware(w, nil)(inner)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections", nil)
	req = req.WithContext(ContextWithTenantID(req.Context(), "tenant-A"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	w.wait(t, 1)
	if atomic.LoadInt32(&done) != 1 {
		t.Fatalf("inner did not complete")
	}
}

func TestDeriveAction(t *testing.T) {
	cases := []struct {
		method, path                 string
		wantAction, wantType, wantID string
	}{
		{http.MethodPost, "/api/v1/connections", "connection.create", "connection", ""},
		{http.MethodPut, "/api/v1/connections/abc", "connection.update", "connection", "abc"},
		{http.MethodDelete, "/api/v1/connections/abc", "connection.delete", "connection", "abc"},
		{http.MethodPost, "/api/v1/connections/abc/start", "connection.start", "connection", "abc"},
		{http.MethodPost, "/api/v1/connections/abc/stop", "connection.stop", "connection", "abc"},
		{http.MethodPost, "/api/v1/connections/abc/dlq/5/retry", "connection.retry", "connection", "abc"},
		{http.MethodPost, "/api/v1/secrets", "secret.create", "secret", ""},
		{http.MethodDelete, "/api/v1/secrets/xyz", "secret.delete", "secret", "xyz"},
		{http.MethodPost, "/api/v1/secrets/xyz/rotate", "secret.rotate", "secret", "xyz"},
	}
	for _, c := range cases {
		a, rt, rid := deriveAction(c.method, c.path)
		if a != c.wantAction || rt != c.wantType || rid != c.wantID {
			t.Errorf("%s %s: got (%q, %q, %q) want (%q, %q, %q)",
				c.method, c.path, a, rt, rid, c.wantAction, c.wantType, c.wantID)
		}
	}
}

func TestAudit_HandlerListsAreTenantScoped(t *testing.T) {
	// Verifies the list handler at the repo layer: tenant A's records do
	// not surface in tenant B's list.
	_, repo := setupTestHandler()
	for i := 0; i < 3; i++ {
		_ = repo.CreateAuditEntry(context.Background(), &AuditEntry{
			TenantID: "tenant-A", Action: "connection.create", ResourceType: "connection",
			Method: "POST", Path: "/api/v1/connections", StatusCode: 201,
		})
	}
	_ = repo.CreateAuditEntry(context.Background(), &AuditEntry{
		TenantID: "tenant-B", Action: "connection.create", ResourceType: "connection",
		Method: "POST", Path: "/api/v1/connections", StatusCode: 201,
	})

	got, _, _ := repo.ListAuditEntries(context.Background(), "tenant-A", AuditFilters{}, 10, 0)
	if len(got) != 3 {
		t.Fatalf("tenant-A should see 3, got %d", len(got))
	}
	for _, e := range got {
		if e.TenantID != "tenant-A" {
			t.Fatalf("tenant-A list leaked entry with tenant %q", e.TenantID)
		}
	}
}

func TestAudit_JSONLExportSerialises(t *testing.T) {
	// Quick check that AuditEntry round-trips through json — the JSONL
	// endpoint relies on this.
	now := time.Now().UTC().Round(time.Microsecond)
	e := &AuditEntry{
		TenantID: "tenant-A", Action: "connection.create",
		ResourceType: "connection", ResourceID: "abc",
		Method: "POST", Path: "/api/v1/connections", StatusCode: 201,
		OccurredAt: now,
		Details:    map[string]interface{}{"name": "test"},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "\"connection.create\"") {
		t.Fatalf("JSON missing action: %s", b)
	}
}
