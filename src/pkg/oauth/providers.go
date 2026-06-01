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

// DefaultRegistry returns a ProviderRegistry seeded with the five provider
// profiles VRSky ships with. Admins may still create provider_type="custom"
// rows with their own URLs/scopes.
func DefaultRegistry() *ProviderRegistry {
	r := NewProviderRegistry()
	r.Register(microsoft365)
	r.Register(salesforce)
	r.Register(google)
	r.Register(hubspot)
	r.Register(shopify)
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

// salesforce uses the production login host by default. Sandbox orgs use
// test.salesforce.com — the admin selects this via extra_params.environment
// = "sandbox", which the handler's applyProfileDefaults swaps in. Refresh +
// offline_access scopes are required to receive a refresh token.
var salesforce = Provider{
	Type:            "salesforce",
	AuthURL:         "https://login.salesforce.com/services/oauth2/authorize",
	TokenURL:        "https://login.salesforce.com/services/oauth2/token",
	RevokeURL:       "https://login.salesforce.com/services/oauth2/revoke",
	Scopes:          []string{"api", "refresh_token", "offline_access"},
	ExtraParams:     map[string]string{},
	SupportsRefresh: true,
}

// SalesforceSandboxAuthURL / SalesforceSandboxTokenURL / SalesforceSandboxRevokeURL
// are the test.salesforce.com equivalents the handler substitutes when an
// admin marks a provider as a sandbox.
const (
	SalesforceSandboxAuthURL   = "https://test.salesforce.com/services/oauth2/authorize"
	SalesforceSandboxTokenURL  = "https://test.salesforce.com/services/oauth2/token"
	SalesforceSandboxRevokeURL = "https://test.salesforce.com/services/oauth2/revoke"
)

// google requires access_type=offline + prompt=consent to be issued a refresh
// token; without them Google returns only a short-lived access token.
var google = Provider{
	Type:      "google",
	AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	RevokeURL: "https://oauth2.googleapis.com/revoke",
	Scopes:    []string{"openid", "email", "https://www.googleapis.com/auth/userinfo.profile"},
	ExtraParams: map[string]string{
		"access_type": "offline",
		"prompt":      "consent",
	},
	SupportsRefresh: true,
}

// hubspot uses a standard auth-code flow. It has no RFC-7009 revoke endpoint
// that fits our POST helper (revocation is DELETE /oauth/v1/refresh-tokens/{token}),
// so RevokeURL is empty — revoke marks the grant locally only. Refresh tokens
// expire 30 days after last use (surfaced as a note in the UI).
var hubspot = Provider{
	Type:            "hubspot",
	AuthURL:         "https://app.hubspot.com/oauth/authorize",
	TokenURL:        "https://api.hubapi.com/oauth/v1/token",
	RevokeURL:       "",
	Scopes:          []string{"crm.objects.contacts.read", "crm.objects.contacts.write"},
	ExtraParams:     map[string]string{},
	SupportsRefresh: true,
}

// shopify scopes its endpoints to a per-store subdomain. The {shop}
// placeholder is filled at runtime from StartOptions.ExtraParams["shop"]
// (see applyURLTemplate). Shopify admin API access tokens are long-lived and
// are NOT refreshed — SupportsRefresh is false, so the refresher skips them.
var shopify = Provider{
	Type:            "shopify",
	AuthURL:         "https://{shop}.myshopify.com/admin/oauth/authorize",
	TokenURL:        "https://{shop}.myshopify.com/admin/oauth/access_token",
	RevokeURL:       "",
	Scopes:          []string{"read_products", "read_orders"},
	ExtraParams:     map[string]string{},
	SupportsRefresh: false,
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

// applyURLTemplate substitutes {key} placeholders in a URL with values from
// params. Used for providers whose endpoints embed a per-grant value — e.g.
// Shopify's auth_url and token_url contain {shop}, filled from the shop the
// admin enters on the Connect screen (carried in StartOptions.ExtraParams).
// A no-op when the URL has no placeholders.
func applyURLTemplate(raw string, params map[string]string) string {
	if !strings.Contains(raw, "{") || len(params) == 0 {
		return raw
	}
	for k, v := range params {
		raw = strings.ReplaceAll(raw, "{"+k+"}", v)
	}
	return raw
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
