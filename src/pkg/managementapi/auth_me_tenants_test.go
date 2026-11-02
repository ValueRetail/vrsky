package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tenantsStub answers the session check like a healthy database and scripts
// only the tenant lookup.
type tenantsStub struct {
	sessionStub
	tenants []*TenantResponse
	err     error
}

func (s *tenantsStub) GetUserTenants(_ context.Context, _ string) ([]*TenantResponse, error) {
	return s.tenants, s.err
}

// GET /auth/me must tell a failed tenant lookup from a user who has no
// workspaces. The first is a 503 the UI retries; the second is a 200 with an
// empty list (#645).
func TestGetMe_TenantLookup(t *testing.T) {
	driverErr := errors.New("pq: could not connect to 10.0.0.7: connection refused")
	two := []*TenantResponse{{ID: "t1", Name: "One"}, {ID: "t2", Name: "Two"}}

	cases := []struct {
		name        string
		tenants     []*TenantResponse
		err         error
		want        int
		wantTenants []string
		wantCurrent string
	}{
		{name: "lookup fails", err: driverErr, want: http.StatusServiceUnavailable},
		{name: "lookup fails and returns rows", tenants: two, err: driverErr, want: http.StatusServiceUnavailable},
		{name: "lookup cancelled", err: context.Canceled, want: http.StatusServiceUnavailable},
		{name: "no tenants (nil slice)", want: http.StatusOK},
		{name: "no tenants (empty slice)", tenants: []*TenantResponse{}, want: http.StatusOK},
		{name: "tenants", tenants: two, want: http.StatusOK, wantTenants: []string{"t1", "t2"}, wantCurrent: "t1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			repo := &tenantsStub{tenants: tc.tenants, err: tc.err}
			repo.validate = func(int) (*Session, *User, error) { return liveSession(), activeUser(), nil }
			mux := http.NewServeMux()
			NewHandler(repo, NewValidator()).RegisterAuthRoutes(mux)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req.Header.Set("Authorization", "Bearer tok")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			logged := strings.Contains(logs.String(), "tenant lookup failed")
			if tc.err != nil {
				assertNoLeak(t, rec.Body.String())
				var body ErrorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("body = %s (%v)", rec.Body.String(), err)
				}
				if body.Status != http.StatusServiceUnavailable || body.Message != sessionUnavailableMessage {
					t.Fatalf("body = %+v, want the generic 503 body", body)
				}
				if strings.Contains(rec.Body.String(), `"user"`) {
					t.Fatalf("a failed lookup must not return the user payload: %s", rec.Body.String())
				}
				if !logged || !strings.Contains(logs.String(), tc.err.Error()) {
					t.Fatalf("the error was not logged server-side: %q", logs.String())
				}
				return
			}
			if logged {
				t.Fatalf("a good lookup logged an error: %q", logs.String())
			}

			// The wire form carries [] for no workspaces, never null or absent.
			raw := rec.Body.String()
			if !strings.Contains(raw, `"tenants":[`) {
				t.Fatalf("tenants is not a JSON array: %s", raw)
			}
			var me MeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
				t.Fatalf("decode: %v: %s", err, raw)
			}
			var ids []string
			for _, tn := range me.Tenants {
				ids = append(ids, tn.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantTenants, ",") {
				t.Fatalf("tenants = %v, want %v", ids, tc.wantTenants)
			}
			switch {
			case tc.wantCurrent == "" && me.CurrentTenant != nil:
				t.Fatalf("current_tenant = %+v, want null", me.CurrentTenant)
			case tc.wantCurrent != "" && (me.CurrentTenant == nil || me.CurrentTenant.ID != tc.wantCurrent):
				t.Fatalf("current_tenant = %+v, want %s", me.CurrentTenant, tc.wantCurrent)
			}
		})
	}
}
