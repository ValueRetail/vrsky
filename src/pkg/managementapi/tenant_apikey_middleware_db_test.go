package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/ValueRetail/vrsky/pkg/testdb"
)

// The API-key lookup against a real Postgres: that sql.ErrNoRows becomes
// ErrAPIKeyNotFound, and what a rejected key and a failing database look like
// through TenantAPIKeyMiddleware. Skipped without VRSKY_TEST_POSTGRES_URL; CI
// sets it.
func TestTenantAPIKeyMiddlewareDB(t *testing.T) {
	db, err := sql.Open("postgres", testdb.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewPostgresRepository(db)
	captureLog(t)

	var owner string
	if err := db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, status)
		VALUES ('key-'||gen_random_uuid()||'@example.com', 'x', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	mkTenant := func(slug, rawKey string) *Tenant {
		t.Helper()
		ten, err := repo.CreateTenant(ctx, owner, slug, slug)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateTenantStatus(ctx, ten.ID, "active", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.UpsertTenantAPIKey(ctx, ten.ID, auth.HashToken(rawKey)); err != nil {
			t.Fatal(err)
		}
		return ten
	}
	live := mkTenant("live-tenant", "live-key")
	mkTenant("rotated-tenant", "rotated-old")
	if _, err := repo.UpsertTenantAPIKey(ctx, mustTenantID(t, ctx, db, "rotated-tenant"), auth.HashToken("rotated-new")); err != nil {
		t.Fatal(err)
	}
	revoked := mkTenant("revoked-tenant", "revoked-key")
	if _, err := db.ExecContext(ctx, `UPDATE tenant_api_keys SET is_active = false WHERE tenant_id = $1`, revoked.ID); err != nil {
		t.Fatal(err)
	}
	gone := mkTenant("gone-tenant", "gone-key")
	if err := repo.DeleteTenant(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	parked := mkTenant("failed-tenant", "failed-key")
	if err := repo.UpdateTenantStatus(ctx, parked.ID, "failed", nil); err != nil {
		t.Fatal(err)
	}

	// The sentinel itself.
	if got, err := repo.GetTenantByAPIKeyHash(ctx, auth.HashToken("live-key")); err != nil || got.ID != live.ID {
		t.Fatalf("live key: %+v, %v", got, err)
	}
	for _, raw := range []string{"never-issued", "rotated-old", "revoked-key", "gone-key"} {
		if got, err := repo.GetTenantByAPIKeyHash(ctx, auth.HashToken(raw)); !errors.Is(err, ErrAPIKeyNotFound) || got != nil {
			t.Fatalf("%s: got %+v, %v; want ErrAPIKeyNotFound", raw, got, err)
		}
	}

	call := func(r *PostgresRepository, key string, reqCtx context.Context) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenant/x/data", nil).WithContext(reqCtx)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		TenantAPIKeyMiddleware(r)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		name string
		repo *PostgresRepository
		key  string
		ctx  context.Context
		want int
	}{
		{"live key", repo, "live-key", ctx, http.StatusNoContent},
		{"unknown key", repo, "never-issued", ctx, http.StatusUnauthorized},
		{"rotated-away key", repo, "rotated-old", ctx, http.StatusUnauthorized},
		{"rotated-in key", repo, "rotated-new", ctx, http.StatusNoContent},
		{"revoked key", repo, "revoked-key", ctx, http.StatusUnauthorized},
		{"key of a deleted tenant", repo, "gone-key", ctx, http.StatusUnauthorized},
		{"key of a tenant that is not active", repo, "failed-key", ctx, http.StatusForbidden},
		{"cancelled context", repo, "live-key", cancelled, http.StatusServiceUnavailable},
		{"closed pool", NewPostgresRepository(closedDB), "live-key", ctx, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(tc.repo, tc.key, tc.ctx); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}

	// A lookup that outlives its deadline is a failed lookup, not a bad key.
	slow, stop := context.WithTimeout(ctx, time.Nanosecond)
	defer stop()
	<-slow.Done()
	if got := call(repo, "live-key", slow); got != http.StatusServiceUnavailable {
		t.Fatalf("expired deadline: status = %d, want 503", got)
	}
}

func mustTenantID(t *testing.T, ctx context.Context, db *sql.DB, slug string) string {
	t.Helper()
	var id string
	// lint:tenant-ok — test fixture lookup by slug.
	if err := db.QueryRowContext(ctx, `SELECT id FROM tenants WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
