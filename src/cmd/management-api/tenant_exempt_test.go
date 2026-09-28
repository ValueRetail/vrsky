package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
