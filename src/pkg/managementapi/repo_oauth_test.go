package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/oauth"
)

// repo_oauth.go's methods read/write encrypted secrets via pkg/crypto, which
// pulls the key from ENCRYPTION_KEY. Tests set a stable 64-hex-char key.
const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func init() { _ = os.Setenv("ENCRYPTION_KEY", testEncryptionKey) }

// newRepoMock wraps sqlmock in a PostgresRepository so we exercise the real
// SQL strings. Returns the repo, the mock controller, and a cleanup func.
func newRepoMock(t *testing.T) (*PostgresRepository, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	repo := &PostgresRepository{db: db}
	return repo, mock, func() { _ = db.Close() }
}

func TestRepoGetProviderConfig_Found(t *testing.T) {
	repo, mock, cleanup := newRepoMock(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{
		"id", "tenant_id", "name", "provider_type", "client_id", "client_secret_id",
		"auth_url", "token_url", "revoke_url", "scopes", "redirect_url", "extra_params",
	}).AddRow(
		"prov-1", "tenant-1", "Acme", "microsoft365", "client-id", "sec-1",
		"https://auth", "https://token", "", pq.Array([]string{"openid"}),
		"https://redirect", []byte(`{"prompt":"consent"}`),
	)
	mock.ExpectQuery("FROM oauth_providers").
		WithArgs("tenant-1", "prov-1").
		WillReturnRows(rows)

	cfg, err := repo.GetProviderConfig(context.Background(), "tenant-1", "prov-1")
	if err != nil {
		t.Fatalf("GetProviderConfig: %v", err)
	}
	if cfg.ProviderType != "microsoft365" {
		t.Errorf("provider_type = %q, want microsoft365", cfg.ProviderType)
	}
	if cfg.ExtraParams["prompt"] != "consent" {
		t.Errorf("extra_params not decoded: %v", cfg.ExtraParams)
	}
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != "openid" {
		t.Errorf("scopes not decoded as []string: %v", cfg.Scopes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRepoGetProviderConfig_NotFoundReturnsTypedError(t *testing.T) {
	repo, mock, cleanup := newRepoMock(t)
	defer cleanup()

	mock.ExpectQuery("FROM oauth_providers").
		WithArgs("tenant-1", "missing").
		WillReturnError(sql.ErrNoRows)

	_, err := repo.GetProviderConfig(context.Background(), "tenant-1", "missing")
	if !errors.Is(err, oauth.ErrProviderNotFound) {
		t.Errorf("want ErrProviderNotFound, got %v", err)
	}
}

func TestRepoScanExpiring_RespectsPartialIndexPredicate(t *testing.T) {
	repo, mock, cleanup := newRepoMock(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"id"}).AddRow("g-1").AddRow("g-2")
	mock.ExpectQuery("FROM oauth_grants").
		WithArgs("300", 50).
		WillReturnRows(rows)

	ids, err := repo.ScanExpiring(context.Background(), 5*time.Minute, 50)
	if err != nil {
		t.Fatalf("ScanExpiring: %v", err)
	}
	if len(ids) != 2 || ids[0] != "g-1" || ids[1] != "g-2" {
		t.Errorf("unexpected scan result: %v", ids)
	}
}

func TestRepoMarkRevoked_OnlyTouchesNonRevokedRows(t *testing.T) {
	repo, mock, cleanup := newRepoMock(t)
	defer cleanup()

	mock.ExpectExec("UPDATE oauth_grants SET revoked_at").
		WithArgs("tenant-1", "g-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.MarkRevoked(context.Background(), "tenant-1", "g-1"); err != nil {
		t.Errorf("MarkRevoked: %v", err)
	}
}

func TestRepoMarkRefreshFailure_StoresReason(t *testing.T) {
	repo, mock, cleanup := newRepoMock(t)
	defer cleanup()

	mock.ExpectExec("UPDATE oauth_grants SET refresh_failed_at").
		WithArgs("tenant-1", "g-1", "refresh_token_expired").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.MarkRefreshFailure(context.Background(), "tenant-1", "g-1", "refresh_token_expired"); err != nil {
		t.Errorf("MarkRefreshFailure: %v", err)
	}
}

func TestRepoDeleteOAuthProvider_RejectsWhenActiveGrants(t *testing.T) {
	repo, mock, cleanup := newRepoMock(t)
	defer cleanup()

	mock.ExpectQuery("FROM oauth_grants WHERE tenant_id").
		WithArgs("tenant-1", "prov-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	err := repo.DeleteOAuthProvider(context.Background(), "tenant-1", "prov-1")
	if err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestRepoDeleteOAuthProvider_NotFoundReturnsTypedError(t *testing.T) {
	repo, mock, cleanup := newRepoMock(t)
	defer cleanup()

	mock.ExpectQuery("FROM oauth_grants WHERE tenant_id").
		WithArgs("tenant-1", "missing").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("DELETE FROM oauth_providers").
		WithArgs("tenant-1", "missing").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.DeleteOAuthProvider(context.Background(), "tenant-1", "missing")
	if !errors.Is(err, oauth.ErrProviderNotFound) {
		t.Errorf("want ErrProviderNotFound, got %v", err)
	}
}

func TestRepoListOAuthProviders_EmptyResult(t *testing.T) {
	repo, mock, cleanup := newRepoMock(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{
		"id", "tenant_id", "name", "provider_type", "client_id", "client_secret_id",
		"auth_url", "token_url", "revoke_url", "scopes", "redirect_url", "extra_params",
	})
	mock.ExpectQuery("FROM oauth_providers").
		WithArgs("tenant-1").
		WillReturnRows(rows)

	got, err := repo.ListOAuthProviders(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListOAuthProviders: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %d", len(got))
	}
}
