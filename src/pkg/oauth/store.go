package oauth

import (
	"context"
	"time"
)

// ProviderConfig is the per-tenant persistent configuration of one OAuth
// provider — one row of the oauth_providers table loaded into memory. The
// ClientSecret field is intentionally absent here; callers resolve it via
// Store.ResolveClientSecret when they need to make a token-endpoint call,
// so the plaintext stays close to its use-site.
type ProviderConfig struct {
	ID             string
	TenantID       string
	Name           string
	ProviderType   string
	ClientID       string
	ClientSecretID string // points at secrets.id
	AuthURL        string
	TokenURL       string
	RevokeURL      string
	Scopes         []string
	RedirectURL    string
	ExtraParams    map[string]string
}

// Grant is the in-memory view of one oauth_grants row. AccessToken /
// RefreshToken are populated only by Store.GetGrant — list/meta calls leave
// them empty so that listing many grants does not require many secrets
// lookups.
type Grant struct {
	ID             string
	TenantID       string
	ProviderID     string
	ProviderType   string
	ProviderName   string
	ConnectionID   *string
	UserIdentifier string
	ScopesGranted  []string

	// Plaintext tokens. Populated only when explicitly loaded.
	AccessToken  string
	RefreshToken string

	ExpiresAt       *time.Time
	LastRefreshedAt *time.Time
	RevokedAt       *time.Time

	// Surfacing the most recent refresh failure: the refresher records these
	// when MarkRefreshFailure is called; UI uses them to flag "Reconnect
	// required" when refresh_token_expired.
	RefreshFailedAt      *time.Time
	RefreshFailureReason string
}

// IsRevoked reports whether the grant has been revoked.
func (g *Grant) IsRevoked() bool { return g.RevokedAt != nil }

// NeedsRefresh reports whether the grant should be refreshed proactively
// because the access token will expire within `skew`. A grant with no
// expires_at (provider didn't tell us) is never refreshed proactively.
func (g *Grant) NeedsRefresh(now time.Time, skew time.Duration) bool {
	if g.ExpiresAt == nil {
		return false
	}
	return !now.Add(skew).Before(*g.ExpiresAt)
}

// Store is the persistence boundary for pkg/oauth. The Postgres + secrets
// implementation lives in pkg/managementapi/repo_oauth.go; pkg/oauth itself
// has zero DB or secrets imports.
//
// All methods are tenant-scoped. Implementations must verify that grants /
// providers belong to the supplied tenant — pkg/oauth assumes this.
type Store interface {
	// GetProviderConfig loads one provider config by ID for the tenant.
	// Returns ErrProviderNotFound if absent.
	GetProviderConfig(ctx context.Context, tenantID, providerID string) (*ProviderConfig, error)

	// ResolveClientSecret returns the plaintext client_secret for a provider
	// (decrypting from the secrets table).
	ResolveClientSecret(ctx context.Context, cfg *ProviderConfig) (string, error)

	// CreateGrant inserts a new grant row plus two secret rows (access +
	// refresh). refreshTok may be empty for providers that do not refresh.
	CreateGrant(ctx context.Context, g *Grant, accessTok, refreshTok string) error

	// UpdateTokens rewrites the access/refresh tokens for an existing grant
	// (in place — same secret rows are updated to preserve secret IDs and
	// audit history). refreshTok empty means leave the existing refresh
	// token unchanged.
	UpdateTokens(ctx context.Context, grantID, accessTok, refreshTok string, expiresAt *time.Time) error

	// GetGrant loads a grant by ID, populating AccessToken + RefreshToken.
	GetGrant(ctx context.Context, tenantID, grantID string) (*Grant, error)

	// GetGrantMeta is the same as GetGrant but does not decrypt tokens. Used
	// by list views where loading every token would be expensive.
	GetGrantMeta(ctx context.Context, tenantID, grantID string) (*Grant, error)

	// ListGrants returns all grants for a tenant. Tokens are not populated.
	ListGrants(ctx context.Context, tenantID string) ([]*Grant, error)

	// MarkRevoked sets revoked_at = now() for a grant. tenantID is included
	// so the UPDATE is tenant-scoped at the SQL layer (defense-in-depth on
	// top of the GetGrant tenant check the caller already performs).
	MarkRevoked(ctx context.Context, tenantID, grantID string) error

	// MarkRefreshFailure records a refresh failure (reason will surface in
	// the UI as "Reconnect required"). Cleared by the next successful
	// UpdateTokens. tenantID scopes the UPDATE at the SQL layer.
	MarkRefreshFailure(ctx context.Context, tenantID, grantID, reason string) error

	// ScanExpiring returns grant IDs whose access token will expire within
	// `within` and that still have a refresh token. limit caps the batch
	// size the refresher pulls per tick.
	ScanExpiring(ctx context.Context, within time.Duration, limit int) ([]string, error)
}
