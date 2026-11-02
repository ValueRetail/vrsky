package managementapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// A tenant API-key lookup that fails for a reason that says nothing about the
// key (the database is down) must not look like a rejected key: a CI/CD
// client treats a 401 as "my key is bad" and a 503 as "try again" (#643).
// These tests pin both halves for every caller of GetTenantByAPIKeyHash, and
// that no error path lets a request through.

// apiKeyStub scripts GetTenantByAPIKeyHash. Every other Repository method
// panics on the nil embedded interface, so nothing else may be called.
type apiKeyStub struct {
	Repository
	tenant *Tenant
	err    error
	calls  int
}

func (s *apiKeyStub) GetTenantByAPIKeyHash(_ context.Context, _ string) (*Tenant, error) {
	s.calls++
	return s.tenant, s.err
}

func (s *apiKeyStub) ValidateSession(_ context.Context, _ string) (*Session, *User, error) {
	return nil, nil, auth.ErrSessionInvalid
}

// A secret key and its hash must appear in neither a body nor a log line.
const secretAPIKey = "vrsky_acme_0123456789abcdef-secret"

func assertNoKeyMaterial(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, secretAPIKey) || strings.Contains(text, auth.HashToken(secretAPIKey)) {
		t.Fatalf("%s carries the API key or its hash: %s", where, text)
	}
}

// A failed API-key lookup is logged as that, never as a session failure.
func assertAPIKeyLog(t *testing.T, status int, logs string) {
	t.Helper()
	if status != http.StatusServiceUnavailable {
		return
	}
	if !strings.Contains(logs, "api key lookup failed") || strings.Contains(logs, "session validation failed") {
		t.Fatalf("log = %q, want 'api key lookup failed' and no 'session validation failed'", logs)
	}
}

var errDriverFailure = errors.New("pq: could not connect to 10.0.0.7: connection refused")

var apiKeyLookupCases = []struct {
	name string
	err  error
	want int
}{
	{"unknown, revoked or rotated key", ErrAPIKeyNotFound, http.StatusUnauthorized},
	{"wrapped not found", fmt.Errorf("lookup: %w", ErrAPIKeyNotFound), http.StatusUnauthorized},
	{"driver error", fmt.Errorf("get tenant by api key: %w", errDriverFailure), http.StatusServiceUnavailable},
	{"closed pool", fmt.Errorf("get tenant by api key: %w", sql.ErrConnDone), http.StatusServiceUnavailable},
	{"bad connection", fmt.Errorf("get tenant by api key: %w", driver.ErrBadConn), http.StatusServiceUnavailable},
	{"cancelled context", fmt.Errorf("get tenant by api key: %w", context.Canceled), http.StatusServiceUnavailable},
	{"deadline exceeded", fmt.Errorf("get tenant by api key: %w", context.DeadlineExceeded), http.StatusServiceUnavailable},
	{"plain error", errors.New("boom"), http.StatusServiceUnavailable},
	// The old string error and the tenant sentinel are not "no such key" any
	// more; a double that still returns them is a failed lookup, the safe side.
	{"old string error", errors.New("no tenant found for API key"), http.StatusServiceUnavailable},
	{"tenant sentinel", ErrTenantNotFound, http.StatusServiceUnavailable},
}

func TestTenantAPIKeyMiddleware_ErrorClassification(t *testing.T) {
	for _, tc := range apiKeyLookupCases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

			req := httptest.NewRequest(http.MethodPost, "/api/v1/tenant/t1/data", nil)
			req.Header.Set("Authorization", "Bearer "+secretAPIKey)
			rec := httptest.NewRecorder()
			repo := &apiKeyStub{err: tc.err}
			TenantAPIKeyMiddleware(repo)(next).ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if reached {
				t.Fatal("the request reached the handler although the key lookup failed")
			}
			assertNoLeak(t, rec.Body.String())
			assertNoKeyMaterial(t, "body", rec.Body.String())
			assertNoKeyMaterial(t, "log", logs.String())
			var body ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Status != tc.want {
				t.Fatalf("body = %s (%v)", rec.Body.String(), err)
			}
			if tc.want == http.StatusServiceUnavailable {
				if body.Message != sessionUnavailableMessage {
					t.Fatalf("message = %q, want the fixed text", body.Message)
				}
				if !strings.Contains(logs.String(), "api key lookup failed") {
					t.Fatalf("the error was not logged server-side: %q", logs.String())
				}
			}
		})
	}
}

func TestTenantAPIKeyMiddleware_Outcomes(t *testing.T) {
	cases := []struct {
		name    string
		tenant  *Tenant
		err     error
		want    int
		reached bool
	}{
		{"active tenant", &Tenant{ID: "t1", Status: "active"}, nil, http.StatusNoContent, true},
		{"tenant not active", &Tenant{ID: "t1", Status: "failed"}, nil, http.StatusForbidden, false},
		{"nil tenant, nil error", nil, nil, http.StatusUnauthorized, false},
		{"key of a deleted tenant", nil, ErrAPIKeyNotFound, http.StatusUnauthorized, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				if GetRequestingTenantFromContext(r.Context()) != tc.tenant {
					t.Error("tenant missing from context")
				}
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			req.Header.Set("Authorization", "Bearer "+secretAPIKey)
			rec := httptest.NewRecorder()
			TenantAPIKeyMiddleware(&apiKeyStub{tenant: tc.tenant, err: tc.err})(next).ServeHTTP(rec, req)
			if rec.Code != tc.want || reached != tc.reached {
				t.Fatalf("status = %d reached = %v, want %d / %v", rec.Code, reached, tc.want, tc.reached)
			}
		})
	}
}

func TestTenantAPIKeyMiddleware_NoKeyDoesNotLookUp(t *testing.T) {
	repo := &apiKeyStub{err: errors.New("must not be called")}
	rec := httptest.NewRecorder()
	TenantAPIKeyMiddleware(repo)(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusUnauthorized || repo.calls != 0 {
		t.Fatalf("status = %d, lookups = %d", rec.Code, repo.calls)
	}
}

// The tenant-header RBAC middleware falls through from a session to an API
// key. A rejected key is an anonymous caller (401); a failed lookup is a 503;
// neither reaches the handler.
func TestRBAC_APIKeyLookupErrors(t *testing.T) {
	cases := []struct {
		name   string
		tenant *Tenant
		err    error
		want   int
	}{
		{"key matches the tenant", &Tenant{ID: "tenant-A", Status: "active"}, nil, http.StatusOK},
		{"key of another tenant", &Tenant{ID: "tenant-B", Status: "active"}, nil, http.StatusUnauthorized},
		{"tenant not active", &Tenant{ID: "tenant-A", Status: "failed"}, nil, http.StatusUnauthorized},
		{"unknown key", nil, ErrAPIKeyNotFound, http.StatusUnauthorized},
		{"nil tenant, nil error", nil, nil, http.StatusUnauthorized},
		{"driver error", nil, fmt.Errorf("get tenant by api key: %w", errDriverFailure), http.StatusServiceUnavailable},
		{"closed pool", nil, fmt.Errorf("get tenant by api key: %w", sql.ErrConnDone), http.StatusServiceUnavailable},
		{"cancelled context", nil, fmt.Errorf("get tenant by api key: %w", context.Canceled), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/connections", nil)
			req = req.WithContext(ContextWithTenantID(req.Context(), "tenant-A"))
			req.Header.Set("Authorization", "Bearer "+secretAPIKey)
			rec := httptest.NewRecorder()
			reached := false
			final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true })
			RequireTenantRoleFromHeader(&apiKeyStub{tenant: tc.tenant, err: tc.err}, "editor")(final).ServeHTTP(rec, req)
			if tc.want == http.StatusOK {
				if !reached {
					t.Fatalf("a good key was refused: %d %s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != tc.want || reached {
				t.Fatalf("status = %d reached = %v, want %d and not reached: %s", rec.Code, reached, tc.want, rec.Body.String())
			}
			assertNoLeak(t, rec.Body.String())
			assertNoKeyMaterial(t, "body", rec.Body.String())
			assertNoKeyMaterial(t, "log", logs.String())
			assertAPIKeyLog(t, tc.want, logs.String())
		})
	}
}

// target_api_key on a connection request resolves through the same call.
func TestCreateConnectionRequest_TargetAPIKeyLookup(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unknown key", ErrAPIKeyNotFound, http.StatusNotFound},
		{"driver error", fmt.Errorf("get tenant by api key: %w", errDriverFailure), http.StatusServiceUnavailable},
		{"closed pool", fmt.Errorf("get tenant by api key: %w", sql.ErrConnDone), http.StatusServiceUnavailable},
		{"cancelled context", fmt.Errorf("get tenant by api key: %w", context.Canceled), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			h := NewHandler(&apiKeyStub{err: tc.err}, NewValidator())
			body := `{"target_api_key":"` + secretAPIKey + `","permission_type":"send"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/t1/connection-requests", strings.NewReader(body))
			req = req.WithContext(context.WithValue(req.Context(), TenantKey, &Tenant{ID: "t1", Status: "active"}))
			rec := httptest.NewRecorder()
			h.CreateConnectionRequest(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			assertNoLeak(t, rec.Body.String())
			assertNoKeyMaterial(t, "body", rec.Body.String())
			assertNoKeyMaterial(t, "log", logs.String())
			assertAPIKeyLog(t, tc.want, logs.String())
		})
	}
}

// The repository statement against sqlmock: no row is the sentinel, anything
// else is wrapped and is not. The real-Postgres version is in
// tenant_apikey_middleware_db_test.go.
func TestGetTenantByAPIKeyHash_ErrorMapping(t *testing.T) {
	tenantCols := []string{"id", "name", "slug", "owner_id", "subscription_plan", "is_verified",
		"max_integrations", "max_messages_per_month", "status", "nats_slug", "created_at", "updated_at"}
	now := time.Now()
	cases := []struct {
		name         string
		setup        func(m sqlmock.Sqlmock)
		wantNotFound bool
		wantErr      bool
	}{
		{"no row", func(m sqlmock.Sqlmock) {
			m.ExpectQuery(`FROM tenants t`).WillReturnRows(sqlmock.NewRows(tenantCols))
		}, true, true},
		{"row", func(m sqlmock.Sqlmock) {
			m.ExpectQuery(`FROM tenants t`).WillReturnRows(sqlmock.NewRows(tenantCols).
				AddRow("t1", "Acme", "acme", "u1", "free", true, 10, 1000, "active", "acme", now, now))
		}, false, false},
		{"query fails", func(m sqlmock.Sqlmock) {
			m.ExpectQuery(`FROM tenants t`).WillReturnError(errDriverFailure)
		}, false, true},
		{"connection done", func(m sqlmock.Sqlmock) {
			m.ExpectQuery(`FROM tenants t`).WillReturnError(sql.ErrConnDone)
		}, false, true},
		{"cancelled context", func(m sqlmock.Sqlmock) {
			m.ExpectQuery(`FROM tenants t`).WillReturnError(context.Canceled)
		}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			tc.setup(mock)
			tenant, err := NewPostgresRepository(db).GetTenantByAPIKeyHash(context.Background(), "hash")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if errors.Is(err, ErrAPIKeyNotFound) != tc.wantNotFound {
				t.Fatalf("err = %v, want not-found %v", err, tc.wantNotFound)
			}
			if err != nil && tenant != nil {
				t.Fatal("a tenant came back with an error")
			}
			if err == nil && (tenant == nil || tenant.ID != "t1") {
				t.Fatalf("tenant = %+v", tenant)
			}
		})
	}
}

// isAPIKeyRejection is an allow-list of one sentinel.
func TestIsAPIKeyRejection(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{ErrAPIKeyNotFound, true},
		{fmt.Errorf("x: %w", ErrAPIKeyNotFound), true},
		{nil, false},
		{ErrTenantNotFound, false},
		{sql.ErrNoRows, false},
		{sql.ErrConnDone, false},
		{context.Canceled, false},
		{errDriverFailure, false},
	}
	for _, tc := range cases {
		if got := isAPIKeyRejection(tc.err); got != tc.want {
			t.Errorf("isAPIKeyRejection(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
