package managementapi

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// The session lookup against a real Postgres: what a rejected session and a
// failing database look like through SessionAuthMiddleware. Skipped without
// VRSKY_TEST_POSTGRES_URL; CI sets it.
func TestSessionAuthMiddlewareDB(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewPostgresRepository(db)
	captureLog(t)

	user := NewUser("member@example.com", "x", "Member")
	if err := repo.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyUserEmail(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mk := func(id, raw string, active bool, expires time.Time) {
		t.Helper()
		err := repo.CreateSession(ctx, &Session{
			ID: id, UserID: user.ID, TokenHash: auth.HashToken(raw),
			CreatedAt: now, ExpiresAt: expires, LastActivity: now, IsActive: active,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	mk("00000000-0000-0000-0000-000000000001", "live", true, now.Add(time.Hour))
	mk("00000000-0000-0000-0000-000000000002", "revoked", false, now.Add(time.Hour))
	mk("00000000-0000-0000-0000-000000000003", "expired", true, now.Add(-time.Minute))

	call := func(r *PostgresRepository, token string, reqCtx context.Context) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil).WithContext(reqCtx)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		SessionAuthMiddleware(r)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rec, req)
		assertNoLeak(t, rec.Body.String())
		return rec.Code
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	closedDB, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = closedDB.Close()

	cases := []struct {
		name  string
		repo  *PostgresRepository
		token string
		ctx   context.Context
		want  int
	}{
		{"live session", repo, "live", ctx, http.StatusNoContent},
		{"unknown token", repo, "nobody", ctx, http.StatusUnauthorized},
		{"revoked session", repo, "revoked", ctx, http.StatusUnauthorized},
		{"expired session", repo, "expired", ctx, http.StatusUnauthorized},
		{"cancelled context", repo, "live", cancelled, http.StatusServiceUnavailable},
		{"closed pool", NewPostgresRepository(closedDB), "live", ctx, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(tc.repo, tc.token, tc.ctx); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}

	// The account is suspended after the session was issued.
	if _, err := db.ExecContext(ctx, `UPDATE users SET status = 'suspended' WHERE id = $1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if got := call(repo, "live", ctx); got != http.StatusForbidden {
		t.Fatalf("suspended user: status = %d, want 403", got)
	}
}
