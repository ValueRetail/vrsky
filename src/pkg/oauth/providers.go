package oauth

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// Provider is a static profile of an OAuth 2.0 provider — the well-known URLs
// and default scopes, plus optional per-provider hooks. Profiles are seeded
// in DefaultRegistry() and may be looked up at runtime by ProviderType.
type Provider struct {
	Type            string
	AuthURL         string
	TokenURL        string
	RevokeURL       string
	Scopes          []string
	ExtraParams     map[string]string
	SupportsRefresh bool

	// BuildAuthURL is non-nil only for providers whose authorize URL needs
	// per-grant templating (Shopify uses the shop subdomain). When nil, the
	// generic builder in Client.StartAuth is used.
	BuildAuthURL func(p Provider, cfg *ProviderConfig, opts StartOptions, state, challenge string) (string, error)
}

// ProviderRegistry holds the in-process catalogue of provider profiles. It is
// safe for concurrent use.
type ProviderRegistry struct {
	mu     sync.RWMutex
	byType map[string]Provider
}

// NewProviderRegistry returns an empty registry. Most callers want
// DefaultRegistry() instead.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{byType: map[string]Provider{}}
}

// Register adds or replaces a profile in the registry.
func (r *ProviderRegistry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byType[p.Type] = p
}

// Get returns the profile for a provider type. The bool is false if the type
// is not registered. Provider type "custom" is intentionally absent — admins
// using "custom" supply auth_url / token_url / scopes themselves on the
// ProviderConfig and Client falls back to a generic standards-only profile.
func (r *ProviderRegistry) Get(providerType string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byType[providerType]
	return p, ok
}

// DefaultRegistry returns a ProviderRegistry seeded with the profiles VRSky
// ships with. In PR #1 only microsoft365 is fully populated — Salesforce,
// Google, HubSpot, Shopify are stubs that PR #2 fills in.
func DefaultRegistry() *ProviderRegistry {
	r := NewProviderRegistry()
	r.Register(microsoft365)
	// PR #2 will register salesforce, google, hubspot, shopify.
	return r
}

// microsoft365 is the "v2.0" Microsoft identity platform endpoint (covers
// Microsoft 365, Entra ID / Azure AD, and personal Microsoft accounts via
// the "common" tenant). Refresh tokens are issued when the offline_access
// scope is requested.
var microsoft365 = Provider{
	Type:            "microsoft365",
	AuthURL:         "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
	TokenURL:        "https://login.microsoftonline.com/common/oauth2/v2.0/token",
	RevokeURL:       "", // MS does not expose a programmatic revoke endpoint — UI directs admins to the consent screen.
	Scopes:          []string{"https://graph.microsoft.com/.default", "offline_access"},
	ExtraParams:     map[string]string{},
	SupportsRefresh: true,
}

// genericProfile is the fallback used for provider_type="custom" rows. URLs
// and scopes come from the ProviderConfig itself.
var genericProfile = Provider{
	Type:            "custom",
	SupportsRefresh: true,
}

// profileFor returns the registered profile for cfg, or the generic profile
// if cfg.ProviderType is unknown. The returned profile's URL/scope fields
// are overlaid with whatever the config specifies so admins can override.
func profileFor(r *ProviderRegistry, cfg *ProviderConfig) Provider {
	p, ok := r.Get(cfg.ProviderType)
	if !ok {
		p = genericProfile
	}
	// Per-tenant overrides win over the profile defaults.
	if cfg.AuthURL != "" {
		p.AuthURL = cfg.AuthURL
	}
	if cfg.TokenURL != "" {
		p.TokenURL = cfg.TokenURL
	}
	if cfg.RevokeURL != "" {
		p.RevokeURL = cfg.RevokeURL
	}
	if len(cfg.Scopes) > 0 {
		p.Scopes = cfg.Scopes
	}
	// Merge ExtraParams: profile defaults first, then per-tenant overrides.
	merged := make(map[string]string, len(p.ExtraParams)+len(cfg.ExtraParams))
	for k, v := range p.ExtraParams {
		merged[k] = v
	}
	for k, v := range cfg.ExtraParams {
		merged[k] = v
	}
	p.ExtraParams = merged
	return p
}

// buildGenericAuthURL constructs the standard auth-code+PKCE URL. Used when
// the profile does not define BuildAuthURL.
func buildGenericAuthURL(p Provider, cfg *ProviderConfig, opts StartOptions, state, challenge string) (string, error) {
	u, err := url.Parse(p.AuthURL)
	if err != nil {
		return "", fmt.Errorf("parse auth url: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURL)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if len(p.Scopes) > 0 {
		q.Set("scope", strings.Join(p.Scopes, " "))
	}
	for k, v := range p.ExtraParams {
		q.Set(k, v)
	}
	// StartOptions.ExtraParams override the profile defaults (Shopify will
	// not use this — it overrides BuildAuthURL entirely — but other
	// providers can be parameterised at start time).
	for k, v := range opts.ExtraParams {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
