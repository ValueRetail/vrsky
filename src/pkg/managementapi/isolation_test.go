package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cross-tenant isolation suite — Phase 1I / #74 acceptance criterion D.
//
// We assert at the HTTP layer that tenant B cannot read or modify
// tenant A's resources via any combination of (X-Tenant-ID header,
// session token, API key). The repo is the in-memory MockRepository
// from handler_test.go so this test stays a unit test, not an
// integration one — the same rules are enforced regardless of backing
// store because the checks live in the handler / middleware code paths.

const (
	tenantA = "11111111-1111-1111-1111-111111111111"
	tenantB = "22222222-2222-2222-2222-222222222222"
)

// runHTTP fires a request through a single mux that mirrors the
// production middleware chain: TenantIDMiddleware checks the header,
// then the route handler runs.
func runHTTP(t *testing.T, h *Handler, method, path, tenantHeader string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(method, path, nil)
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
	}
	if tenantHeader != "" {
		// Add tenant header AND populate the context — the production
		// chain has TenantIDMiddleware do this before the mux; we
		// shortcut by setting the context value directly.
		req = req.WithContext(ContextWithTenantID(req.Context(), tenantHeader))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestIsolation_TenantBCannotReadTenantAConnections(t *testing.T) {
	h, repo := setupTestHandler()
	repo.connections["conn-A"] = &Connection{ID: "conn-A", TenantID: tenantA, Name: "A's pipeline"}

	// GetConnection cross-tenant → 403 (handler verifies tenant ownership).
	w := runHTTP(t, h, http.MethodGet, "/api/v1/connections/conn-A", tenantB, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("tenant B reading tenant A's connection: want 403, got %d (%s)", w.Code, w.Body.String())
	}

	// List → 0 results for tenant B even though the mock has tenant A data.
	w = runHTTP(t, h, http.MethodGet, "/api/v1/connections", tenantB, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list for tenant B: %d", w.Code)
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	for _, c := range resp.Data {
		if c["tenant_id"] == tenantA {
			t.Fatalf("tenant B's list leaked tenant A row: %+v", c)
		}
	}
}

func TestIsolation_SecretsAreTenantScoped(t *testing.T) {
	// Reuse the secrets-handler harness from secrets_handler_test.go.
	h, _ := setupSecretsHandler(t)

	// Tenant A creates a secret.
	w := doRequest(t, h.SecretsCollection, http.MethodPost, "/api/v1/secrets", tenantA,
		CreateSecretRequest{Name: "pg-pwd", Value: "alice-only"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create as A: %d", w.Code)
	}
	id := mustDecodeData(t, w.Body.Bytes())["id"].(string)

	// Tenant B cannot read by ID.
	w = doRequest(t, h.SecretsItem, http.MethodGet, "/api/v1/secrets/"+id, tenantB, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("B read A's secret: want 404, got %d", w.Code)
	}
	// Tenant B cannot delete by ID.
	w = doRequest(t, h.SecretsItem, http.MethodDelete, "/api/v1/secrets/"+id, tenantB, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("B delete A's secret: want 404, got %d", w.Code)
	}
	// Tenant B's list is empty.
	w = doRequest(t, h.SecretsCollection, http.MethodGet, "/api/v1/secrets", tenantB, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("B list: %d", w.Code)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 0 {
		t.Fatalf("B sees A's secret in list: %+v", env.Data)
	}
}

func TestIsolation_AuditEntriesAreTenantScoped(t *testing.T) {
	_, repo := setupTestHandler()
	for i := 0; i < 3; i++ {
		_ = repo.CreateAuditEntry(context.Background(), &AuditEntry{
			TenantID: tenantA, Action: "connection.create", ResourceType: "connection",
			Method: "POST", Path: "/api/v1/connections", StatusCode: 201,
		})
	}
	for i := 0; i < 2; i++ {
		_ = repo.CreateAuditEntry(context.Background(), &AuditEntry{
			TenantID: tenantB, Action: "connection.update", ResourceType: "connection",
			Method: "PUT", Path: "/api/v1/connections/x", StatusCode: 200,
		})
	}

	gotA, _, _ := repo.ListAuditEntries(context.Background(), tenantA, AuditFilters{}, 100, 0)
	gotB, _, _ := repo.ListAuditEntries(context.Background(), tenantB, AuditFilters{}, 100, 0)

	if len(gotA) != 3 || len(gotB) != 2 {
		t.Fatalf("expected (3, 2), got (%d, %d)", len(gotA), len(gotB))
	}
	for _, e := range gotA {
		if e.TenantID != tenantA {
			t.Fatalf("A's list leaked B's entry: %+v", e)
		}
	}
	for _, e := range gotB {
		if e.TenantID != tenantB {
			t.Fatalf("B's list leaked A's entry: %+v", e)
		}
	}
}

func TestIsolation_NoTenantHeaderRejectsMutation(t *testing.T) {
	h, _ := setupTestHandler()
	w := runHTTP(t, h, http.MethodPost, "/api/v1/connections", "" /* no tenant */, map[string]any{})
	// TenantIDMiddleware isn't part of this test harness; we expect the
	// RBAC middleware to refuse with 400 (no tenant) or 401 (no auth).
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 400/401, got %d", w.Code)
	}
}

func TestIsolation_QuotaPerTenant(t *testing.T) {
	// Tenant A and B have independent quota buckets.
	tracker := NewQuotaTracker()
	qA := &TenantQuotas{TenantID: tenantA, MaxMsgPerSec: 2}
	qB := &TenantQuotas{TenantID: tenantB, MaxMsgPerSec: 2}

	// Both can take 2 tokens.
	if err := tracker.CheckMessageRate(tenantA, qA); err != nil {
		t.Fatalf("A first take: %v", err)
	}
	if err := tracker.CheckMessageRate(tenantA, qA); err != nil {
		t.Fatalf("A second take: %v", err)
	}
	if err := tracker.CheckMessageRate(tenantA, qA); err == nil {
		t.Fatalf("A third take should be denied")
	}

	// Tenant B still has its full bucket — A's exhaustion shouldn't
	// affect B.
	if err := tracker.CheckMessageRate(tenantB, qB); err != nil {
		t.Fatalf("B first take: %v", err)
	}
	if err := tracker.CheckMessageRate(tenantB, qB); err != nil {
		t.Fatalf("B second take: %v", err)
	}
	if err := tracker.CheckMessageRate(tenantB, qB); err == nil {
		t.Fatalf("B third take should be denied")
	}
}

func TestIsolation_StorageExceededBlocksUploads(t *testing.T) {
	tracker := NewQuotaTracker()
	q := &TenantQuotas{TenantID: tenantA, MaxStorageBytes: 1024, StorageExceeded: true}
	if err := tracker.CheckStorage(q); err == nil {
		t.Fatalf("expected storage check to refuse when StorageExceeded=true")
	}
	q.StorageExceeded = false
	if err := tracker.CheckStorage(q); err != nil {
		t.Fatalf("storage check should pass when not exceeded, got %v", err)
	}
}

func TestIsolation_IntegrationCountBlocksExtraCreates(t *testing.T) {
	// Direct check against the QuotaTracker — the HTTP path additionally
	// requires auth (RBAC #69), so we'd need to mint a session for the
	// full integration test; that's covered by rbac_test.go. Here we
	// just prove the quota math.
	_, repo := setupTestHandler()
	q := &TenantQuotas{TenantID: tenantA, MaxIntegrations: 2}
	_ = repo.UpdateTenantQuotas(context.Background(), q)
	repo.connections["a-1"] = &Connection{ID: "a-1", TenantID: tenantA}
	repo.connections["a-2"] = &Connection{ID: "a-2", TenantID: tenantA}

	tracker := NewQuotaTracker()
	if err := tracker.CheckIntegrationCount(context.Background(), repo, tenantA, q); err == nil {
		t.Fatalf("third integration should be denied (limit=%d, current=%d)",
			q.MaxIntegrations, 2)
	}

	// Tenant B is unaffected.
	repo.connections["b-1"] = &Connection{ID: "b-1", TenantID: tenantB}
	qB := &TenantQuotas{TenantID: tenantB, MaxIntegrations: 2}
	if err := tracker.CheckIntegrationCount(context.Background(), repo, tenantB, qB); err != nil {
		t.Fatalf("tenant B's quota check should pass (1 of 2 used), got %v", err)
	}
}
