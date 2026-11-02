package managementapi

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// A session lookup that fails for a reason that says nothing about the token
// (the database is down) must not look like a rejected session: the UI signs
// the user out on a 401 from /auth/me (#611). These tests pin both halves:
// every real rejection stays a 401, everything else is a 503 that carries no
// error text.

// sessionStub scripts ValidateSession. Every other Repository method panics on
// the nil embedded interface unless overridden below, which is the point:
// nothing else may be called.
type sessionStub struct {
	Repository
	validate func(call int) (*Session, *User, error)
	calls    int
	roleErr  error
	role     string
}

func (s *sessionStub) ValidateSession(_ context.Context, _ string) (*Session, *User, error) {
	s.calls++
	return s.validate(s.calls)
}

func (s *sessionStub) GetUserTenantRole(_ context.Context, _, _ string) (string, error) {
	return s.role, s.roleErr
}

func (s *sessionStub) GetTenantByAPIKeyHash(_ context.Context, _ string) (*Tenant, error) {
	return nil, ErrAPIKeyNotFound
}

func (s *sessionStub) GetUserTenants(_ context.Context, _ string) ([]*TenantResponse, error) {
	return nil, nil
}

func stubFailing(err error) *sessionStub {
	return &sessionStub{validate: func(int) (*Session, *User, error) { return nil, nil, err }}
}

func activeUser() *User {
	return &User{ID: "u1", Email: "u1@example.com", Status: UserStatusActive, EmailVerified: true}
}

func liveSession() *Session {
	return &Session{ID: "s1", UserID: "u1", IsActive: true, ExpiresAt: time.Now().Add(time.Hour)}
}

// leakMarkers are fragments of the errors below that must never reach a client.
var leakMarkers = []string{"pq:", "connection", "refused", "sql:", "failed to get", "deadline", "canceled", "10.0.0.7"}

func assertNoLeak(t *testing.T, body string) {
	t.Helper()
	low := strings.ToLower(body)
	for _, m := range leakMarkers {
		if strings.Contains(low, m) {
			t.Fatalf("response body leaks %q: %s", m, body)
		}
	}
}

// captureLog collects what the package logs through slog.Default().
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

var sessionErrorCases = []struct {
	name string
	err  error
	want int
}{
	{"unknown token", auth.ErrSessionInvalid, http.StatusUnauthorized},
	{"revoked session", fmt.Errorf("lookup: %w", auth.ErrSessionInvalid), http.StatusUnauthorized},
	{"expired session", auth.ErrSessionExpired, http.StatusUnauthorized},
	{"user of the session is gone", auth.ErrUserNotFound, http.StatusUnauthorized},
	{"not found, repository double", &NotFoundError{ResourceType: "Session", ResourceID: "x"}, http.StatusUnauthorized},
	{"unauthorized, repository double", &UnauthorizedError{Message: "no session"}, http.StatusUnauthorized},
	{"driver error", fmt.Errorf("failed to get session: %w", errors.New("pq: could not connect to 10.0.0.7: connection refused")), http.StatusServiceUnavailable},
	{"closed pool", fmt.Errorf("failed to get session: %w", sql.ErrConnDone), http.StatusServiceUnavailable},
	{"bad connection", fmt.Errorf("failed to get user: %w", driver.ErrBadConn), http.StatusServiceUnavailable},
	{"cancelled context", fmt.Errorf("failed to get session: %w", context.Canceled), http.StatusServiceUnavailable},
	{"deadline exceeded", fmt.Errorf("failed to get session: %w", context.DeadlineExceeded), http.StatusServiceUnavailable},
	{"plain error", errors.New("boom"), http.StatusServiceUnavailable},
}

func TestSessionAuthMiddleware_ErrorClassification(t *testing.T) {
	for _, tc := range sessionErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req.Header.Set("Authorization", "Bearer some-token")
			rec := httptest.NewRecorder()
			SessionAuthMiddleware(stubFailing(tc.err))(next).ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if reached {
				t.Fatal("the request reached the handler although the session lookup failed")
			}
			if tc.want == http.StatusServiceUnavailable {
				assertNoLeak(t, rec.Body.String())
				var body ErrorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Status != http.StatusServiceUnavailable {
					t.Fatalf("body = %s (%v)", rec.Body.String(), err)
				}
				if !strings.Contains(logs.String(), "credential lookup failed") {
					t.Fatalf("the error was not logged server-side: %q", logs.String())
				}
			}
		})
	}
}

func TestSessionAuthMiddleware_AcceptsAndSuspended(t *testing.T) {
	cases := []struct {
		name   string
		status UserStatus
		want   int
	}{
		{"active user", UserStatusActive, http.StatusOK},
		{"suspended user", UserStatusSuspended, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := activeUser()
			u.Status = tc.status
			repo := &sessionStub{validate: func(int) (*Session, *User, error) { return liveSession(), u, nil }}
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				if GetUserFromContext(r.Context()) != u {
					t.Error("user missing from context")
				}
			})
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Authorization", "Bearer tok")
			rec := httptest.NewRecorder()
			SessionAuthMiddleware(repo)(next).ServeHTTP(rec, req)
			if rec.Code != tc.want || reached != (tc.want == http.StatusOK) {
				t.Fatalf("status = %d reached = %v, want %d", rec.Code, reached, tc.want)
			}
		})
	}
}

func TestSessionAuthMiddleware_NoToken(t *testing.T) {
	repo := stubFailing(errors.New("must not be called"))
	rec := httptest.NewRecorder()
	SessionAuthMiddleware(repo)(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized || repo.calls != 0 {
		t.Fatalf("status = %d, lookups = %d", rec.Code, repo.calls)
	}
}

// /auth/me is the route the UI signs out on. GetMe validates the session a
// second time after the middleware; a blip on that second lookup used to be a
// 401 whose body was the raw error text.
func TestMeRoute_DatabaseErrorIsNot401(t *testing.T) {
	driverErr := fmt.Errorf("failed to get session: %w", errors.New("pq: could not connect to 10.0.0.7: connection refused"))
	cases := []struct {
		name     string
		validate func(call int) (*Session, *User, error)
		want     int
	}{
		{"first lookup fails", func(int) (*Session, *User, error) { return nil, nil, driverErr }, http.StatusServiceUnavailable},
		{"second lookup fails", func(call int) (*Session, *User, error) {
			if call == 1 {
				return liveSession(), activeUser(), nil
			}
			return nil, nil, driverErr
		}, http.StatusServiceUnavailable},
		{"session revoked between the two lookups", func(call int) (*Session, *User, error) {
			if call == 1 {
				return liveSession(), activeUser(), nil
			}
			return nil, nil, auth.ErrSessionInvalid
		}, http.StatusUnauthorized},
		{"unknown token", func(int) (*Session, *User, error) { return nil, nil, auth.ErrSessionInvalid }, http.StatusUnauthorized},
		{"expired", func(int) (*Session, *User, error) { return nil, nil, auth.ErrSessionExpired }, http.StatusUnauthorized},
		{"valid", func(int) (*Session, *User, error) { return liveSession(), activeUser(), nil }, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			repo := &sessionStub{validate: tc.validate}
			mux := http.NewServeMux()
			NewHandler(repo, NewValidator()).RegisterAuthRoutes(mux)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req.Header.Set("Authorization", "Bearer tok")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want != http.StatusOK {
				assertNoLeak(t, rec.Body.String())
			}
		})
	}
}

// Called without the middleware in front, the handlers' own check still
// rejects a request that carries no token with a 401.
func TestHandlers_NoTokenIs401(t *testing.T) {
	repo := stubFailing(errors.New("must not be called"))
	h := NewHandler(repo, NewValidator())
	for name, fn := range map[string]http.HandlerFunc{
		"GetMe":          h.GetMe,
		"ChangePassword": h.ChangePassword,
		"DeleteAccount":  h.DeleteAccount,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fn(rec, httptest.NewRequest(http.MethodGet, "/x", strings.NewReader("{}")))
			if rec.Code != http.StatusUnauthorized || repo.calls != 0 {
				t.Fatalf("status = %d, lookups = %d", rec.Code, repo.calls)
			}
		})
	}
}

func TestChangePasswordAndDeleteAccount_DatabaseErrorIs503(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/auth/change-password"},
		{http.MethodDelete, "/api/v1/auth/me"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			captureLog(t)
			// Passes the middleware, fails in the handler's own lookup.
			repo := &sessionStub{validate: func(call int) (*Session, *User, error) {
				if call == 1 {
					return liveSession(), activeUser(), nil
				}
				return nil, nil, fmt.Errorf("failed to get session: %w", sql.ErrConnDone)
			}}
			mux := http.NewServeMux()
			NewHandler(repo, NewValidator()).RegisterAuthRoutes(mux)
			req := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer tok")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			assertNoLeak(t, rec.Body.String())
		})
	}
}

func TestRBAC_SessionLookupErrors(t *testing.T) {
	liveLookup := func(int) (*Session, *User, error) { return liveSession(), activeUser(), nil }
	cases := []struct {
		name string
		repo *sessionStub
		want int
	}{
		{"unknown token falls through to a 401", stubFailing(auth.ErrSessionInvalid), http.StatusUnauthorized},
		{"expired session is a 401", stubFailing(auth.ErrSessionExpired), http.StatusUnauthorized},
		{"database error on the session is a 503", stubFailing(fmt.Errorf("failed to get session: %w", sql.ErrConnDone)), http.StatusServiceUnavailable},
		{"database error on the role is a 503", &sessionStub{validate: liveLookup, roleErr: errors.New("pq: connection refused")}, http.StatusServiceUnavailable},
		{"no role in the tenant is a 401", &sessionStub{validate: liveLookup}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/connections", nil)
			req = req.WithContext(ContextWithTenantID(req.Context(), "tenant-A"))
			req.Header.Set("Authorization", "Bearer tok")
			rec := httptest.NewRecorder()
			final := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("request reached the handler") })
			RequireTenantRoleFromHeader(tc.repo, "editor")(final).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			assertNoLeak(t, rec.Body.String())
		})
	}
}

// The same classification against the real PostgresRepository statements, so
// the sentinels the repository returns and the ones the middleware accepts
// cannot drift apart.
func TestSessionAuthMiddleware_PostgresRepositoryErrors(t *testing.T) {
	sessionCols := []string{"id", "user_id", "token_hash", "ip_address", "user_agent",
		"created_at", "expires_at", "last_activity", "is_active"}
	userCols := []string{"id", "email", "password_hash", "full_name", "status",
		"email_verified", "email_verified_at", "created_at", "updated_at", "last_login_at", "deleted_at"}
	now := time.Now()
	sessionRow := func(active bool, expires time.Time) *sqlmock.Rows {
		return sqlmock.NewRows(sessionCols).AddRow("s1", "u1", "h", nil, nil, now, expires, now, active)
	}

	cases := []struct {
		name  string
		setup func(m sqlmock.Sqlmock, db *sql.DB)
		want  int
	}{
		{"unknown token", func(m sqlmock.Sqlmock, _ *sql.DB) {
			m.ExpectQuery(`FROM sessions`).WillReturnRows(sqlmock.NewRows(sessionCols))
		}, http.StatusUnauthorized},
		{"revoked session", func(m sqlmock.Sqlmock, _ *sql.DB) {
			m.ExpectQuery(`FROM sessions`).WillReturnRows(sessionRow(false, now.Add(time.Hour)))
		}, http.StatusUnauthorized},
		{"expired session", func(m sqlmock.Sqlmock, _ *sql.DB) {
			m.ExpectQuery(`FROM sessions`).WillReturnRows(sessionRow(true, now.Add(-time.Minute)))
		}, http.StatusUnauthorized},
		{"user is gone", func(m sqlmock.Sqlmock, _ *sql.DB) {
			m.ExpectQuery(`FROM sessions`).WillReturnRows(sessionRow(true, now.Add(time.Hour)))
			m.ExpectQuery(`FROM users`).WillReturnRows(sqlmock.NewRows(userCols))
		}, http.StatusUnauthorized},
		{"session query fails", func(m sqlmock.Sqlmock, _ *sql.DB) {
			m.ExpectQuery(`FROM sessions`).WillReturnError(errors.New("pq: connection refused"))
		}, http.StatusServiceUnavailable},
		{"user query fails", func(m sqlmock.Sqlmock, _ *sql.DB) {
			m.ExpectQuery(`FROM sessions`).WillReturnRows(sessionRow(true, now.Add(time.Hour)))
			m.ExpectQuery(`FROM users`).WillReturnError(errors.New("pq: connection refused"))
		}, http.StatusServiceUnavailable},
		{"pool is closed", func(m sqlmock.Sqlmock, db *sql.DB) {
			m.ExpectClose()
			_ = db.Close()
		}, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(mock, db)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req.Header.Set("Authorization", "Bearer tok")
			rec := httptest.NewRecorder()
			SessionAuthMiddleware(NewPostgresRepository(db))(http.NotFoundHandler()).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			assertNoLeak(t, rec.Body.String())
		})
	}
}
