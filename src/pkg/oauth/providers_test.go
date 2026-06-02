package oauth

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// DefaultRegistry must carry all five shipped profiles.
func TestDefaultRegistry_HasAllProviders(t *testing.T) {
	reg := DefaultRegistry()
	for _, typ := range []string{"microsoft365", "salesforce", "google", "hubspot", "shopify"} {
		if _, ok := reg.Get(typ); !ok {
			t.Errorf("DefaultRegistry missing profile %q", typ)
		}
	}
}

// startURLFor builds the authorize URL for a given provider profile + config
// without standing up an HTTP server (StartAuth only touches the store for
// the provider config, then builds the URL locally).
func startURLFor(t *testing.T, cfg *ProviderConfig, opts StartOptions) *url.URL {
	t.Helper()
	store := newInMemStore()
	store.providers[cfg.ID] = cfg
	c := New(store, DefaultRegistry())
	raw, _, _, err := c.StartAuth(context.Background(), cfg.TenantID, cfg.ID, opts)
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	return u
}

func baseCfg(id, providerType string) *ProviderConfig {
	return &ProviderConfig{
		ID:           id,
		TenantID:     "tenant-1",
		Name:         providerType + " test",
		ProviderType: providerType,
		ClientID:     "client-x",
		RedirectURL:  "https://app.example.com/oauth/callback",
	}
}

// Google's authorize URL must carry access_type=offline + prompt=consent,
// or Google won't issue a refresh token.
func TestGoogle_AuthorizeURLForcesOfflineConsent(t *testing.T) {
	u := startURLFor(t, baseCfg("g1", "google"), StartOptions{})
	q := u.Query()
	if q.Get("access_type") != "offline" {
		t.Errorf("google access_type=%q, want offline", q.Get("access_type"))
	}
	if q.Get("prompt") != "consent" {
		t.Errorf("google prompt=%q, want consent", q.Get("prompt"))
	}
	if u.Host != "accounts.google.com" {
		t.Errorf("google host=%q", u.Host)
	}
}

// Shopify's authorize URL must template the shop subdomain from ExtraParams.
func TestShopify_AuthorizeURLTemplatesShop(t *testing.T) {
	u := startURLFor(t, baseCfg("s1", "shopify"), StartOptions{
		ExtraParams: map[string]string{"shop": "acme"},
	})
	if u.Host != "acme.myshopify.com" {
		t.Errorf("shopify host=%q, want acme.myshopify.com", u.Host)
	}
	if !strings.HasPrefix(u.Path, "/admin/oauth/authorize") {
		t.Errorf("shopify path=%q", u.Path)
	}
}

// Salesforce production uses login.salesforce.com.
func TestSalesforce_ProductionHost(t *testing.T) {
	u := startURLFor(t, baseCfg("sf1", "salesforce"), StartOptions{})
	if u.Host != "login.salesforce.com" {
		t.Errorf("salesforce prod host=%q, want login.salesforce.com", u.Host)
	}
}

// Microsoft authorize URL carries the offline_access scope so a refresh token
// is issued.
func TestMicrosoft_RequestsOfflineAccessScope(t *testing.T) {
	u := startURLFor(t, baseCfg("ms1", "microsoft365"), StartOptions{})
	if !strings.Contains(u.Query().Get("scope"), "offline_access") {
		t.Errorf("microsoft scope=%q missing offline_access", u.Query().Get("scope"))
	}
}

// applyURLTemplate is a no-op when there are no placeholders or no params.
func TestApplyURLTemplate(t *testing.T) {
	cases := []struct {
		raw    string
		params map[string]string
		want   string
	}{
		{"https://x.com/a", nil, "https://x.com/a"},
		{"https://{shop}.myshopify.com", map[string]string{"shop": "acme"}, "https://acme.myshopify.com"},
		{"https://{shop}.x/{shop}", map[string]string{"shop": "a"}, "https://a.x/a"},
		{"https://x.com", map[string]string{"shop": "a"}, "https://x.com"},
		{"https://{missing}.x", map[string]string{"shop": "a"}, "https://{missing}.x"},
	}
	for _, tc := range cases {
		if got := applyURLTemplate(tc.raw, tc.params); got != tc.want {
			t.Errorf("applyURLTemplate(%q, %v) = %q, want %q", tc.raw, tc.params, got, tc.want)
		}
	}
}

// profileFor overlays per-tenant config overrides on top of the profile.
func TestProfileFor_ConfigOverridesProfile(t *testing.T) {
	reg := DefaultRegistry()
	cfg := baseCfg("o1", "google")
	cfg.Scopes = []string{"custom.scope"}
	cfg.AuthURL = "https://override.example/auth"
	p := profileFor(reg, cfg)
	if p.AuthURL != "https://override.example/auth" {
		t.Errorf("config AuthURL override ignored: %q", p.AuthURL)
	}
	if len(p.Scopes) != 1 || p.Scopes[0] != "custom.scope" {
		t.Errorf("config Scopes override ignored: %v", p.Scopes)
	}
	// ExtraParams from the profile (access_type/prompt) should still merge in.
	if p.ExtraParams["access_type"] != "offline" {
		t.Errorf("profile ExtraParams lost on override: %v", p.ExtraParams)
	}
}

// A non-refresh provider (Shopify) must NOT persist a refresh token even if
// the token endpoint returns one — the refresher skips grants with no refresh
// token, and storing a useless one would be misleading.
func TestComplete_NonRefreshProviderDropsRefreshToken(t *testing.T) {
	fp := newFakeProvider(t, "client-id", "client-secret")
	store := newInMemStore()
	store.clientSecret = fp.expectedSecret

	reg := NewProviderRegistry()
	reg.Register(Provider{
		Type:            "shoplike",
		AuthURL:         fp.srv.URL + "/authorize",
		TokenURL:        fp.srv.URL + "/token",
		Scopes:          []string{"read"},
		SupportsRefresh: false, // like Shopify
	})
	cfg := &ProviderConfig{
		ID: "p1", TenantID: "tenant-1", Name: "Shop", ProviderType: "shoplike",
		ClientID: "client-id", AuthURL: fp.srv.URL + "/authorize",
		TokenURL: fp.srv.URL + "/token", RedirectURL: "https://app/cb",
	}
	store.providers["p1"] = cfg

	c := New(store, reg)
	_, state, verifier, err := c.StartAuth(context.Background(), "tenant-1", "p1", StartOptions{})
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	g, err := c.Complete(context.Background(), "tenant-1", "p1", "auth-code-123", verifier, state, state, StartOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if g.RefreshToken != "" {
		t.Errorf("non-refresh provider stored a refresh token: %q", g.RefreshToken)
	}
	// And the stored secret should be empty too.
	if store.tokens[g.ID].refreshTok != "" {
		t.Errorf("non-refresh provider persisted refresh token to store")
	}
}

// An unknown provider type falls back to the generic profile (custom configs).
func TestProfileFor_UnknownTypeUsesGeneric(t *testing.T) {
	reg := DefaultRegistry()
	cfg := baseCfg("c1", "custom")
	cfg.AuthURL = "https://my.idp/auth"
	cfg.TokenURL = "https://my.idp/token"
	p := profileFor(reg, cfg)
	if p.AuthURL != "https://my.idp/auth" || p.TokenURL != "https://my.idp/token" {
		t.Errorf("custom URLs not honoured: %+v", p)
	}
	if !p.SupportsRefresh {
		t.Errorf("generic profile should support refresh by default")
	}
}
