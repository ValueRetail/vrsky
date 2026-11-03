package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// memberStub scripts the two lookups TenantMemberMiddleware makes. Every other
// Repository method panics on the nil embedded interface.
type memberStub struct {
	Repository
	role      string
	roleErr   error
	tenant    *Tenant
	tenantErr error
	tenantHit int
}

func (s *memberStub) GetUserTenantRole(_ context.Context, _, _ string) (string, error) {
	return s.role, s.roleErr
}

func (s *memberStub) GetTenantByID(_ context.Context, _ string) (*Tenant, error) {
	s.tenantHit++
	return s.tenant, s.tenantErr
}

// A failed tenant read or membership lookup is not a missing workspace and not
// a refused member: only a real not-found is a 404, only a real non-member is
// a 403, and a database error is a 503 with a fixed body (#662).
func TestTenantMemberMiddleware_LookupErrors(t *testing.T) {
	driverErr := errors.New("pq: could not connect to 10.0.0.7: connection refused")
	tenant := &Tenant{ID: "t1", Name: "One"}

	cases := []struct {
		name      string
		stub      memberStub
		want      int
		wantNext  bool
		wantLog   string
		wantTenRd bool // GetTenantByID must have been called
	}{
		{name: "member and tenant ok", stub: memberStub{role: "editor", tenant: tenant}, want: http.StatusOK, wantNext: true, wantTenRd: true},
		{name: "tenant not found (repository sentinel)", stub: memberStub{role: "editor", tenantErr: ErrTenantNotFound}, want: http.StatusNotFound, wantTenRd: true},
		{name: "tenant not found (wrapped sentinel)", stub: memberStub{role: "editor", tenantErr: fmt.Errorf("tenant t1: %w", ErrTenantNotFound)}, want: http.StatusNotFound, wantTenRd: true},
		{name: "tenant not found (sql.ErrNoRows)", stub: memberStub{role: "editor", tenantErr: sql.ErrNoRows}, want: http.StatusNotFound, wantTenRd: true},
		{name: "tenant read driver error", stub: memberStub{role: "editor", tenantErr: driverErr}, want: http.StatusServiceUnavailable, wantLog: "tenant lookup failed", wantTenRd: true},
		{name: "tenant read closed pool", stub: memberStub{role: "editor", tenantErr: fmt.Errorf("get tenant: %w", sql.ErrConnDone)}, want: http.StatusServiceUnavailable, wantLog: "tenant lookup failed", wantTenRd: true},
		{name: "tenant read cancelled", stub: memberStub{role: "editor", tenantErr: context.Canceled}, want: http.StatusServiceUnavailable, wantLog: "tenant lookup failed", wantTenRd: true},
		{name: "membership lookup driver error", stub: memberStub{roleErr: driverErr}, want: http.StatusServiceUnavailable, wantLog: "tenant membership lookup failed"},
		{name: "membership lookup cancelled", stub: memberStub{roleErr: context.Canceled}, want: http.StatusServiceUnavailable, wantLog: "tenant membership lookup failed"},
		{name: "non-member", stub: memberStub{role: "", tenant: tenant}, want: http.StatusForbidden},
		{name: "non-member, tenant also gone", stub: memberStub{role: "", tenantErr: ErrTenantNotFound}, want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			stub := tc.stub
			reached := false
			var gotTenant *Tenant
			var gotRole string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				gotTenant = GetTenantFromContext(r.Context())
				gotRole = GetTenantRoleFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			mux := http.NewServeMux()
			mux.Handle("GET /api/v1/tenants/{tenant_id}/things", TenantMemberMiddleware(&stub)(next))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/t1/things", nil)
			req = req.WithContext(context.WithValue(req.Context(), UserContextKey, activeUser()))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if reached != tc.wantNext {
				t.Fatalf("next reached = %v, want %v", reached, tc.wantNext)
			}
			if (stub.tenantHit > 0) != tc.wantTenRd {
				t.Fatalf("GetTenantByID calls = %d, want called = %v", stub.tenantHit, tc.wantTenRd)
			}
			if tc.wantNext && (gotTenant == nil || gotTenant.ID != "t1" || gotRole != "editor") {
				t.Fatalf("context tenant = %+v role = %q", gotTenant, gotRole)
			}

			if tc.want == http.StatusServiceUnavailable {
				assertNoLeak(t, rec.Body.String())
				var body ErrorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("body = %s (%v)", rec.Body.String(), err)
				}
				if body.Status != http.StatusServiceUnavailable || body.Message != sessionUnavailableMessage {
					t.Fatalf("body = %+v, want the generic 503 body", body)
				}
				wantErr := tc.stub.roleErr
				if wantErr == nil {
					wantErr = tc.stub.tenantErr
				}
				if !strings.Contains(logs.String(), tc.wantLog) || !strings.Contains(logs.String(), wantErr.Error()) {
					t.Fatalf("the error was not logged server-side: %q", logs.String())
				}
				return
			}
			if logs.Len() != 0 {
				t.Fatalf("a non-503 answer logged an error: %q", logs.String())
			}
		})
	}
}
