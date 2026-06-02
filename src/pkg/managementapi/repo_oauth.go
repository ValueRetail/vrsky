package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/oauth"
)

// Small json wrappers so the helper code reads cleanly and we can swap the
// implementation in one place if needed.
var (
	jsonMarshal   = json.Marshal
	jsonUnmarshal = json.Unmarshal
)

// Compile-time check that *PostgresRepository satisfies the oauth.Store
// interface. If a future signature change breaks this, the package won't
// build.
var _ oauth.Store = (*PostgresRepository)(nil)

// =====================================================================
// oauth.Store implementation
// =====================================================================

// GetProviderConfig loads one provider config by ID for the tenant.
func (r *PostgresRepository) GetProviderConfig(ctx context.Context, tenantID, providerID string) (*oauth.ProviderConfig, error) {
	const q = `
		SELECT id, tenant_id, name, provider_type, client_id, client_secret_id,
		       auth_url, token_url, COALESCE(revoke_url, ''), scopes, redirect_url, extra_params
		FROM oauth_providers
		WHERE tenant_id = $1 AND id = $2`
	cfg := &oauth.ProviderConfig{}
	var extra []byte
	err := r.db.QueryRowContext(ctx, q, tenantID, providerID).Scan(
		&cfg.ID, &cfg.TenantID, &cfg.Name, &cfg.ProviderType,
		&cfg.ClientID, &cfg.ClientSecretID,
		&cfg.AuthURL, &cfg.TokenURL, &cfg.RevokeURL,
		pq.Array(&cfg.Scopes), &cfg.RedirectURL, &extra,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, oauth.ErrProviderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load oauth_providers: %w", err)
	}
	cfg.ExtraParams, err = decodeStringMap(extra)
	if err != nil {
		return nil, fmt.Errorf("decode extra_params: %w", err)
	}
	return cfg, nil
}

// ResolveClientSecret decrypts the client_secret referenced by the provider
// config.
func (r *PostgresRepository) ResolveClientSecret(ctx context.Context, cfg *oauth.ProviderConfig) (string, error) {
	ct, err := r.GetSecretCiphertext(ctx, cfg.TenantID, cfg.ClientSecretID)
	if err != nil {
		return "", err
	}
	key, err := crypto.Key()
	if err != nil {
		return "", err
	}
	return crypto.Decrypt(ct, key)
}

// CreateGrant inserts a grant row plus its two secret rows (access + refresh).
// refreshTok empty → refresh_token_secret_id stays NULL (provider has no refresh).
// All writes happen in a single transaction so a partial failure leaves no
// orphan secrets.
func (r *PostgresRepository) CreateGrant(ctx context.Context, g *oauth.Grant, accessTok, refreshTok string) error {
	key, err := crypto.Key()
	if err != nil {
		return err
	}
	accessCt, err := crypto.Encrypt(accessTok, key)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	var refreshCt string
	if refreshTok != "" {
		refreshCt, err = crypto.Encrypt(refreshTok, key)
		if err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Secret names just need to be tenant-unique. Including grant intent in
	// the name makes operator inspection easier.
	accessSec, err := createSecretTx(ctx, tx, g.TenantID, "oauth-access:"+g.ProviderName, accessCt)
	if err != nil {
		return fmt.Errorf("persist access secret: %w", err)
	}
	var refreshSecID sql.NullString
	if refreshCt != "" {
		s, err := createSecretTx(ctx, tx, g.TenantID, "oauth-refresh:"+g.ProviderName, refreshCt)
		if err != nil {
			return fmt.Errorf("persist refresh secret: %w", err)
		}
		refreshSecID.String = s
		refreshSecID.Valid = true
	}

	const q = `
		INSERT INTO oauth_grants (
		    tenant_id, provider_id, provider_type, provider_name,
		    connection_id, user_identifier, scopes_granted,
		    access_token_secret_id, refresh_token_secret_id, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, created_at, updated_at`
	var connID sql.NullString
	if g.ConnectionID != nil {
		connID.String = *g.ConnectionID
		connID.Valid = true
	}
	var userIdent sql.NullString
	if g.UserIdentifier != "" {
		userIdent.String = g.UserIdentifier
		userIdent.Valid = true
	}
	var createdAt, updatedAt time.Time
	err = tx.QueryRowContext(ctx, q,
		g.TenantID, g.ProviderID, g.ProviderType, g.ProviderName,
		connID, userIdent, pq.Array(g.ScopesGranted),
		accessSec, refreshSecID, g.ExpiresAt,
	).Scan(&g.ID, &createdAt, &updatedAt)
	if err != nil {
		return fmt.Errorf("insert oauth_grants: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// UpdateTokens rewrites the access and (optionally) refresh tokens for an
// existing grant. Updates the existing secret rows in place so secret IDs
// stay stable.
func (r *PostgresRepository) UpdateTokens(ctx context.Context, grantID, accessTok, refreshTok string, expiresAt *time.Time) error {
	key, err := crypto.Key()
	if err != nil {
		return err
	}
	accessCt, err := crypto.Encrypt(accessTok, key)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	var refreshCt string
	if refreshTok != "" {
		refreshCt, err = crypto.Encrypt(refreshTok, key)
		if err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
	}

	// Need the tenant + secret IDs to update the secrets in place.
	var tenantID string
	var accessSecID string
	var refreshSecID sql.NullString
	const qLookup = `
		SELECT tenant_id, access_token_secret_id, refresh_token_secret_id
		FROM oauth_grants
		WHERE id = $1`
	if err := r.db.QueryRowContext(ctx, qLookup, grantID).Scan(&tenantID, &accessSecID, &refreshSecID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return oauth.ErrGrantNotFound
		}
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := updateSecretCiphertextTx(ctx, tx, tenantID, accessSecID, accessCt); err != nil {
		return fmt.Errorf("update access secret: %w", err)
	}
	if refreshCt != "" && refreshSecID.Valid {
		if err := updateSecretCiphertextTx(ctx, tx, tenantID, refreshSecID.String, refreshCt); err != nil {
			return fmt.Errorf("update refresh secret: %w", err)
		}
	}

	const qGrant = `
		UPDATE oauth_grants
		SET expires_at = $2,
		    last_refreshed_at = NOW(),
		    refresh_failed_at = NULL,
		    refresh_failure_reason = NULL,
		    updated_at = NOW()
		WHERE id = $1`
	if _, err := tx.ExecContext(ctx, qGrant, grantID, expiresAt); err != nil {
		return fmt.Errorf("update oauth_grants: %w", err)
	}
	return tx.Commit()
}

// GetGrant loads a grant with decrypted tokens.
func (r *PostgresRepository) GetGrant(ctx context.Context, tenantID, grantID string) (*oauth.Grant, error) {
	g, accessSecID, refreshSecID, err := r.loadGrantRow(ctx, tenantID, grantID)
	if err != nil {
		return nil, err
	}
	key, err := crypto.Key()
	if err != nil {
		return nil, err
	}
	accessCt, err := r.GetSecretCiphertext(ctx, tenantID, accessSecID)
	if err != nil {
		return nil, fmt.Errorf("load access ciphertext: %w", err)
	}
	g.AccessToken, err = crypto.Decrypt(accessCt, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	if refreshSecID.Valid {
		ct, err := r.GetSecretCiphertext(ctx, tenantID, refreshSecID.String)
		if err != nil {
			return nil, fmt.Errorf("load refresh ciphertext: %w", err)
		}
		g.RefreshToken, err = crypto.Decrypt(ct, key)
		if err != nil {
			return nil, fmt.Errorf("decrypt refresh token: %w", err)
		}
	}
	return g, nil
}

// GetGrantMeta returns a grant without populating tokens.
func (r *PostgresRepository) GetGrantMeta(ctx context.Context, tenantID, grantID string) (*oauth.Grant, error) {
	g, _, _, err := r.loadGrantRow(ctx, tenantID, grantID)
	return g, err
}

// ListGrants returns all of a tenant's grants (without tokens).
func (r *PostgresRepository) ListGrants(ctx context.Context, tenantID string) ([]*oauth.Grant, error) {
	const q = `
		SELECT id, tenant_id, provider_id, provider_type, provider_name,
		       connection_id, COALESCE(user_identifier, ''), scopes_granted,
		       expires_at, last_refreshed_at, revoked_at
		FROM oauth_grants
		WHERE tenant_id = $1
		ORDER BY created_at DESC`
	rows, err := r.db.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list oauth_grants: %w", err)
	}
	defer rows.Close()
	var out []*oauth.Grant
	for rows.Next() {
		g := &oauth.Grant{}
		var connID, provID sql.NullString
		if err := rows.Scan(
			&g.ID, &g.TenantID, &provID, &g.ProviderType, &g.ProviderName,
			&connID, &g.UserIdentifier, pq.Array(&g.ScopesGranted),
			&g.ExpiresAt, &g.LastRefreshedAt, &g.RevokedAt,
		); err != nil {
			return nil, err
		}
		g.ProviderID = provID.String // empty when the provider config was deleted
		if connID.Valid {
			c := connID.String
			g.ConnectionID = &c
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MarkRevoked sets revoked_at = NOW() on the grant, scoped by tenant.
func (r *PostgresRepository) MarkRevoked(ctx context.Context, tenantID, grantID string) error {
	const q = `UPDATE oauth_grants SET revoked_at = NOW(), updated_at = NOW() WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, tenantID, grantID)
	return err
}

// MarkRefreshFailure records the most recent refresh failure on a grant,
// scoped by tenant.
func (r *PostgresRepository) MarkRefreshFailure(ctx context.Context, tenantID, grantID, reason string) error {
	const q = `UPDATE oauth_grants SET refresh_failed_at = NOW(), refresh_failure_reason = $3, updated_at = NOW() WHERE tenant_id = $1 AND id = $2`
	_, err := r.db.ExecContext(ctx, q, tenantID, grantID, reason)
	return err
}

// ScanExpiring returns the IDs of grants whose access token expires within
// `within` and that are still refreshable. The partial index
// idx_oauth_grants_expiring keeps this an index-only scan.
func (r *PostgresRepository) ScanExpiring(ctx context.Context, within time.Duration, limit int) ([]string, error) {
	const q = `
		SELECT id FROM oauth_grants
		WHERE revoked_at IS NULL
		  AND expires_at IS NOT NULL
		  AND refresh_token_secret_id IS NOT NULL
		  AND expires_at < NOW() + ($1::text || ' seconds')::interval
		ORDER BY expires_at ASC
		LIMIT $2` // lint:tenant-ok — global scan across all tenants is intentional for the background refresher.
	rows, err := r.db.QueryContext(ctx, q, fmt.Sprintf("%d", int(within.Seconds())), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// loadGrantRow is the shared SELECT shared by GetGrant / GetGrantMeta. It
// returns the grant minus tokens, plus the secret IDs so GetGrant can fetch
// and decrypt them.
func (r *PostgresRepository) loadGrantRow(ctx context.Context, tenantID, grantID string) (*oauth.Grant, string, sql.NullString, error) {
	const q = `
		SELECT id, tenant_id, provider_id, provider_type, provider_name,
		       connection_id, COALESCE(user_identifier, ''), scopes_granted,
		       access_token_secret_id, refresh_token_secret_id,
		       expires_at, last_refreshed_at, revoked_at
		FROM oauth_grants
		WHERE tenant_id = $1 AND id = $2`
	g := &oauth.Grant{}
	var connID, provID sql.NullString
	var accessSecID string
	var refreshSecID sql.NullString
	err := r.db.QueryRowContext(ctx, q, tenantID, grantID).Scan(
		&g.ID, &g.TenantID, &provID, &g.ProviderType, &g.ProviderName,
		&connID, &g.UserIdentifier, pq.Array(&g.ScopesGranted),
		&accessSecID, &refreshSecID,
		&g.ExpiresAt, &g.LastRefreshedAt, &g.RevokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", sql.NullString{}, oauth.ErrGrantNotFound
	}
	if err != nil {
		return nil, "", sql.NullString{}, fmt.Errorf("load oauth_grants: %w", err)
	}
	g.ProviderID = provID.String // empty when the provider config was deleted
	if connID.Valid {
		c := connID.String
		g.ConnectionID = &c
	}
	return g, accessSecID, refreshSecID, nil
}

// =====================================================================
// Admin CRUD on oauth_providers (used by the admin handler, not Store)
// =====================================================================

// CreateOAuthProvider inserts a provider config and stores the client secret.
// The cfg.ClientSecretID field is populated on success.
func (r *PostgresRepository) CreateOAuthProvider(ctx context.Context, cfg *oauth.ProviderConfig, clientSecret string) error {
	key, err := crypto.Key()
	if err != nil {
		return err
	}
	ct, err := crypto.Encrypt(clientSecret, key)
	if err != nil {
		return fmt.Errorf("encrypt client secret: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	secID, err := createSecretTx(ctx, tx, cfg.TenantID, "oauth-clientsecret:"+cfg.Name, ct)
	if err != nil {
		return fmt.Errorf("persist client secret: %w", err)
	}
	cfg.ClientSecretID = secID

	extra, err := encodeStringMap(cfg.ExtraParams)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO oauth_providers (
		    tenant_id, name, provider_type, client_id, client_secret_id,
		    auth_url, token_url, revoke_url, scopes, redirect_url, extra_params
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id`
	var revokeURL sql.NullString
	if cfg.RevokeURL != "" {
		revokeURL.String = cfg.RevokeURL
		revokeURL.Valid = true
	}
	if err := tx.QueryRowContext(ctx, q,
		cfg.TenantID, cfg.Name, cfg.ProviderType, cfg.ClientID, cfg.ClientSecretID,
		cfg.AuthURL, cfg.TokenURL, revokeURL,
		pq.Array(cfg.Scopes), cfg.RedirectURL, extra,
	).Scan(&cfg.ID); err != nil {
		return fmt.Errorf("insert oauth_providers: %w", err)
	}
	return tx.Commit()
}

// UpdateOAuthProvider updates the mutable fields of a provider. If
// newClientSecret is non-empty, the referenced secret row is rewritten in
// place (its ID is preserved).
func (r *PostgresRepository) UpdateOAuthProvider(ctx context.Context, cfg *oauth.ProviderConfig, newClientSecret string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if newClientSecret != "" {
		key, err := crypto.Key()
		if err != nil {
			return err
		}
		ct, err := crypto.Encrypt(newClientSecret, key)
		if err != nil {
			return fmt.Errorf("encrypt client secret: %w", err)
		}
		if err := updateSecretCiphertextTx(ctx, tx, cfg.TenantID, cfg.ClientSecretID, ct); err != nil {
			return fmt.Errorf("update client secret: %w", err)
		}
	}

	extra, err := encodeStringMap(cfg.ExtraParams)
	if err != nil {
		return err
	}
	var revokeURL sql.NullString
	if cfg.RevokeURL != "" {
		revokeURL.String = cfg.RevokeURL
		revokeURL.Valid = true
	}
	const q = `
		UPDATE oauth_providers
		SET name = $3, provider_type = $4, client_id = $5,
		    auth_url = $6, token_url = $7, revoke_url = $8,
		    scopes = $9, redirect_url = $10, extra_params = $11,
		    updated_at = NOW()
		WHERE tenant_id = $1 AND id = $2`
	res, err := tx.ExecContext(ctx, q,
		cfg.TenantID, cfg.ID, cfg.Name, cfg.ProviderType, cfg.ClientID,
		cfg.AuthURL, cfg.TokenURL, revokeURL,
		pq.Array(cfg.Scopes), cfg.RedirectURL, extra,
	)
	if err != nil {
		return fmt.Errorf("update oauth_providers: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return oauth.ErrProviderNotFound
	}
	return tx.Commit()
}

// DeleteOAuthProvider removes a provider config. Returns an error if any
// non-revoked grants still reference it.
func (r *PostgresRepository) DeleteOAuthProvider(ctx context.Context, tenantID, providerID string) error {
	var liveCount int
	const qCheck = `SELECT COUNT(*) FROM oauth_grants WHERE tenant_id = $1 AND provider_id = $2 AND revoked_at IS NULL`
	if err := r.db.QueryRowContext(ctx, qCheck, tenantID, providerID).Scan(&liveCount); err != nil {
		return err
	}
	if liveCount > 0 {
		return fmt.Errorf("cannot delete provider with %d active grant(s); revoke them first", liveCount)
	}
	const q = `DELETE FROM oauth_providers WHERE tenant_id = $1 AND id = $2`
	res, err := r.db.ExecContext(ctx, q, tenantID, providerID)
	if err != nil {
		return fmt.Errorf("delete oauth_providers: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return oauth.ErrProviderNotFound
	}
	return nil
}

// ListOAuthProviders returns all provider configs for a tenant.
func (r *PostgresRepository) ListOAuthProviders(ctx context.Context, tenantID string) ([]*oauth.ProviderConfig, error) {
	const q = `
		SELECT id, tenant_id, name, provider_type, client_id, client_secret_id,
		       auth_url, token_url, COALESCE(revoke_url, ''), scopes, redirect_url, extra_params
		FROM oauth_providers
		WHERE tenant_id = $1
		ORDER BY name ASC`
	rows, err := r.db.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list oauth_providers: %w", err)
	}
	defer rows.Close()
	var out []*oauth.ProviderConfig
	for rows.Next() {
		cfg := &oauth.ProviderConfig{}
		var extra []byte
		if err := rows.Scan(
			&cfg.ID, &cfg.TenantID, &cfg.Name, &cfg.ProviderType,
			&cfg.ClientID, &cfg.ClientSecretID,
			&cfg.AuthURL, &cfg.TokenURL, &cfg.RevokeURL,
			pq.Array(&cfg.Scopes), &cfg.RedirectURL, &extra,
		); err != nil {
			return nil, err
		}
		cfg.ExtraParams, err = decodeStringMap(extra)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// =====================================================================
// Small helpers shared by the methods above
// =====================================================================

// encodeStringMap marshals a map[string]string to JSON for storage in a
// JSONB column. A nil map encodes as "{}" so the column NOT NULL constraint
// is honoured.
func encodeStringMap(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return jsonMarshal(m)
}

// decodeStringMap unmarshals a JSONB column into a map[string]string. Empty
// input yields an empty map, never nil, so callers can write the result
// directly into a struct without nil checks.
func decodeStringMap(raw []byte) (map[string]string, error) {
	m := map[string]string{}
	if len(raw) == 0 {
		return m, nil
	}
	if err := jsonUnmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// createSecretTx inserts one secret row inside a transaction and returns the
// new secret's ID. Mirrors *PostgresRepository.CreateSecret but takes a Tx.
func createSecretTx(ctx context.Context, tx *sql.Tx, tenantID, name, ciphertext string) (string, error) {
	const q = `
		INSERT INTO secrets (tenant_id, name, ciphertext)
		VALUES ($1, $2, $3)
		RETURNING id`
	var id string
	if err := tx.QueryRowContext(ctx, q, tenantID, name, ciphertext).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// updateSecretCiphertextTx rewrites the ciphertext (and bumps rotated_at /
// updated_at) on an existing secret inside a transaction.
func updateSecretCiphertextTx(ctx context.Context, tx *sql.Tx, tenantID, id, ciphertext string) error {
	const q = `
		UPDATE secrets
		SET ciphertext = $3, rotated_at = NOW(), updated_at = NOW()
		WHERE tenant_id = $1 AND id = $2`
	res, err := tx.ExecContext(ctx, q, tenantID, id, ciphertext)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("secret %s not found for tenant %s", id, tenantID)
	}
	return nil
}
