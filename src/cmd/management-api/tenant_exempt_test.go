package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
)

// The platform routes are gated by RequireOperator and act on the workspace in
// the path. An operator with no active workspace has no X-Tenant-ID to send
// (#637), so the header must not be required there, while the operator check
// still holds and the workspace routes still want a header.
func TestTenantIDMiddleware_PlatformRoutesNeedNoWorkspaceHeader(t *testing.T) {
	h := managementapi.NewHandler(nil, managementapi.NewValidator())
	h.SetPlatformOperators([]string{"ops@example.com"})

	// What the session middleware does, without a database: put a user in the context.
	as := func(email string, next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), managementapi.UserContextKey, &managementapi.User{Email: email})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	var gotTenant string
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant, _ = managementapi.GetTenantIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	platform := h.RequireOperator()(ok)
	build := func(email string) http.Handler {
		mux := http.NewServeMux()
		mux.Handle("GET /api/v1/platform/tenants", as(email, platform))
		mux.Handle("PUT /api/v1/platform/tenants/{tenant_id}/plan", as(email, platform))
		mux.Handle("GET /api/v1/connections", as(email, ok))
		return TenantIDMiddleware("X-Tenant-ID")(mux)
	}
	do := func(hd http.Handler, method, path, tenant string) int {
		req := httptest.NewRequest(method, path, nil)
		if tenant != "" {
			req.Header.Set("X-Tenant-ID", tenant)
		}
		rec := httptest.NewRecorder()
		hd.ServeHTTP(rec, req)
		return rec.Code
	}

	op, member := build("ops@example.com"), build("member@example.com")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/platform/tenants"},
		{http.MethodPut, "/api/v1/platform/tenants/abc/plan"},
	} {
		if got := do(op, c.method, c.path, ""); got != http.StatusOK {
			t.Errorf("operator, %s %s without X-Tenant-ID: %d, want 200", c.method, c.path, got)
		}
		if got := do(member, c.method, c.path, ""); got != http.StatusForbidden {
			t.Errorf("non-operator, %s %s without X-Tenant-ID: %d, want 403", c.method, c.path, got)
		}
		if got := do(member, c.method, c.path, "t1"); got != http.StatusForbidden {
			t.Errorf("non-operator, %s %s with X-Tenant-ID: %d, want 403", c.method, c.path, got)
		}
	}

	// A header that is sent still reaches the context (the audit log reads it).
	if got := do(op, http.MethodGet, "/api/v1/platform/tenants", "t1"); got != http.StatusOK || gotTenant != "t1" {
		t.Errorf("operator with header: %d, tenant in context %q; want 200, t1", got, gotTenant)
	}

	// Workspace routes keep requiring the header, operator or not.
	for name, hd := range map[string]http.Handler{"operator": op, "member": member} {
		if got := do(hd, http.MethodGet, "/api/v1/connections", ""); got != http.StatusBadRequest {
			t.Errorf("%s, GET /api/v1/connections without X-Tenant-ID: %d, want 400", name, got)
		}
	}
}

// The agent download and its install scripts are fetched by a Windows machine
// with no login and no workspace header. If TenantIDMiddleware stops exempting
// them, the one-line installer fails with "Missing or invalid tenant ID header"
// on a machine where nobody can see that message's cause.
func TestTenantIDMiddleware_ExemptsPublicAgentRoutes(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := TenantIDMiddleware("X-Tenant-ID")(ok)

	for _, path := range []string{
		"/api/v1/agents/release",
		"/api/v1/agents/download/windows-amd64",
		"/api/v1/agents/install.ps1",
		"/api/v1/agents/uninstall.ps1",
	} {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s without X-Tenant-ID: status %d, want 200", path, rec.Code)
		}
	}

	// The management routes beside them stay workspace-scoped.
	for _, path := range []string{"/api/v1/agents", "/api/v1/agents/registration-tokens", "/api/v1/agents/abc"} {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s without X-Tenant-ID: status %d, want 400", path, rec.Code)
		}
	}
}
