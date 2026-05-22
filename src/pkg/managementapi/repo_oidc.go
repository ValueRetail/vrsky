package managementapi

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
)

// ===== Types =====

// OIDCConfig is the per-tenant configuration for SSO via an OIDC IdP
// (Google, Microsoft Entra, Okta, Keycloak, …). The client secret is
// stored encrypted in the `secrets` table and referenced by UUID.
type OIDCConfig struct {
	TenantID         string    `json:"tenant_id"`
	IssuerURL        string    `json:"issuer_url"`
	ClientID         string    `json:"client_id"`
	ClientSecretID   string    `json:"client_secret_id"`
	RedirectURL      string    `json:"redirect_url"`
	Scopes           []string  `json:"scopes"`
	AllowedDomains   []string  `json:"allowed_domains,omitempty"`
	DefaultRole      string    `json:"default_role"`
	ProviderLabel    string    `json:"provider_label,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ErrOIDCConfigNotFound is returned when a tenant has no OIDC config.
var ErrOIDCConfigNotFound = errors.New("oidc config not found for tenant")

// ===== PostgresRepository methods =====

// GetOIDCConfigByTenantID returns the OIDC config for a tenant or
// ErrOIDCConfigNotFound when the tenant has not configured SSO.
func (r *PostgresRepository) GetOIDCConfigByTenantID(ctx context.Context, tenantID string) (*OIDCConfig, error) {
	var (
		c           OIDCConfig
		allowed     pq.StringArray
		label       sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT tenant_id, issuer_url, client_id, client_secret_id, redirect_url,
		       scopes, allowed_domains, default_role, provider_label,
		       created_at, updated_at
		FROM oidc_config WHERE tenant_id = $1
	`, tenantID).Scan(
		&c.TenantID, &c.IssuerURL, &c.ClientID, &c.ClientSecretID, &c.RedirectURL,
		(*pq.StringArray)(&c.Scopes), &allowed, &c.DefaultRole, &label,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOIDCConfigNotFound
	}
	if err != nil {
		return nil, err
	}
	c.AllowedDomains = []string(allowed)
	c.ProviderLabel = label.String
	return &c, nil
}

// GetOIDCConfigByTenantSlug is the variant used at /auth/oidc/{slug}/login
// — the slug is what URLs carry, not the UUID.
func (r *PostgresRepository) GetOIDCConfigByTenantSlug(ctx context.Context, slug string) (*OIDCConfig, error) {
	var tenantID string
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM tenants WHERE slug = $1`, slug,
	).Scan(&tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.GetOIDCConfigByTenantID(ctx, tenantID)
}

// UpsertOIDCConfig stores or replaces the OIDC config for a tenant.
func (r *PostgresRepository) UpsertOIDCConfig(ctx context.Context, c *OIDCConfig) error {
	var label sql.NullString
	if c.ProviderLabel != "" {
		label = sql.NullString{String: c.ProviderLabel, Valid: true}
	}
	return r.db.QueryRowContext(ctx, `
		INSERT INTO oidc_config (
			tenant_id, issuer_url, client_id, client_secret_id, redirect_url,
			scopes, allowed_domains, default_role, provider_label
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id) DO UPDATE SET
			issuer_url        = EXCLUDED.issuer_url,
			client_id         = EXCLUDED.client_id,
			client_secret_id  = EXCLUDED.client_secret_id,
			redirect_url      = EXCLUDED.redirect_url,
			scopes            = EXCLUDED.scopes,
			allowed_domains   = EXCLUDED.allowed_domains,
			default_role      = EXCLUDED.default_role,
			provider_label    = EXCLUDED.provider_label,
			updated_at        = NOW()
		RETURNING created_at, updated_at
	`, c.TenantID, c.IssuerURL, c.ClientID, c.ClientSecretID, c.RedirectURL,
		pq.StringArray(c.Scopes), pq.StringArray(c.AllowedDomains),
		c.DefaultRole, label,
	).Scan(&c.CreatedAt, &c.UpdatedAt)
}

// DeleteOIDCConfig removes the OIDC config for a tenant.
func (r *PostgresRepository) DeleteOIDCConfig(ctx context.Context, tenantID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM oidc_config WHERE tenant_id = $1`, tenantID,
	)
	return err
}

// GetUserByOIDCSubject finds a user previously linked to an OIDC identity.
// Returns nil + nil when no match (callers will auto-provision).
func (r *PostgresRepository) GetUserByOIDCSubject(ctx context.Context, provider, subject string) (*User, error) {
	u := &User{}
	var passwordHash sql.NullString
	var fullName sql.NullString
	var lastLogin, emailVerifiedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT id, email, password_hash, full_name, status, email_verified, email_verified_at,
		       last_login_at, created_at, updated_at
		FROM users
		WHERE oidc_provider = $1 AND oidc_subject = $2 AND status != 'deleted'
	`, provider, subject).Scan(
		&u.ID, &u.Email, &passwordHash, &fullName, &u.Status,
		&u.EmailVerified, &emailVerifiedAt,
		&lastLogin, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if passwordHash.Valid {
		u.PasswordHash = passwordHash.String
	}
	u.FullName = fullName.String
	if emailVerifiedAt.Valid {
		t := emailVerifiedAt.Time
		u.EmailVerifiedAt = &t
	}
	if lastLogin.Valid {
		t := lastLogin.Time
		u.LastLoginAt = &t
	}
	return u, nil
}

// LinkUserOIDC writes the oidc_provider + oidc_subject onto an existing
// user row. Used after auto-provisioning or first link of an existing
// password-based account to SSO.
func (r *PostgresRepository) LinkUserOIDC(ctx context.Context, userID, provider, subject string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE users SET oidc_provider = $2, oidc_subject = $3, updated_at = NOW()
		WHERE id = $1
	`, userID, provider, subject)
	return err
}
