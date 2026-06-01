package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
)

// StartOptions carries optional inputs to a single authorization flow that
// the static Provider profile cannot supply on its own — most importantly the
// connection the grant will be attached to, and provider-specific extras
// (e.g. Shopify's shop subdomain).
type StartOptions struct {
	ConnectionID *string
	ExtraParams  map[string]string
}

// Client is the entrypoint to pkg/oauth. It is safe for concurrent use; per-
// grant operations are deduplicated via an internal singleflight group so
// concurrent token reads of an expiring grant never trigger more than one
// provider call.
type Client struct {
	store    Store
	registry *ProviderRegistry
	http     *http.Client
	now      func() time.Time
	skew     time.Duration
	sf       singleflight.Group
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithHTTPClient overrides the http.Client used for provider-side calls
// (token endpoint, revoke endpoint). The default is a 15-second-timeout
// http.Client. Tests use this to inject httptest servers' clients.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.http = c }
}

// WithClock overrides the time source. Tests use this to drive the
// refresh-on-expiry logic deterministically.
func WithClock(now func() time.Time) Option {
	return func(cl *Client) { cl.now = now }
}

// WithRefreshSkew sets how far ahead of expires_at to refresh proactively.
// Default 60 seconds.
func WithRefreshSkew(s time.Duration) Option {
	return func(cl *Client) { cl.skew = s }
}

// New constructs a Client. registry may be DefaultRegistry() or a custom
// one for testing.
func New(store Store, registry *ProviderRegistry, opts ...Option) *Client {
	c := &Client{
		store:    store,
		registry: registry,
		http:     &http.Client{Timeout: 15 * time.Second},
		now:      time.Now,
		skew:     60 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// StartAuth begins an OAuth 2.0 auth-code + PKCE flow. It returns the
// authorize URL the browser should be redirected to, plus the state and
// code_verifier values the caller must persist (typically as short-lived
// HttpOnly cookies) and supply back to Complete. The caller is responsible
// for ensuring the state value reaches Complete unchanged — this package
// only generates and validates it.
func (c *Client) StartAuth(ctx context.Context, tenantID, providerID string, opts StartOptions) (authURL, state, verifier string, err error) {
	cfg, err := c.store.GetProviderConfig(ctx, tenantID, providerID)
	if err != nil {
		return "", "", "", err
	}
	prof := profileFor(c.registry, cfg)

	state, err = randURLSafe(24)
	if err != nil {
		return "", "", "", fmt.Errorf("generate state: %w", err)
	}
	verifier, err = randURLSafe(48)
	if err != nil {
		return "", "", "", fmt.Errorf("generate verifier: %w", err)
	}
	challenge := pkceChallenge(verifier)

	build := prof.BuildAuthURL
	if build == nil {
		build = buildGenericAuthURL
	}
	authURL, err = build(prof, cfg, opts, state, challenge)
	if err != nil {
		return "", "", "", err
	}
	return authURL, state, verifier, nil
}

// Complete exchanges an authorization code for tokens, persists the grant
// (with tokens stored encrypted via Store.CreateGrant), and returns the
// resulting Grant. The expectedState argument is the state value returned
// by StartAuth; Complete checks it against actualState and returns
// ErrStateMismatch if they differ.
func (c *Client) Complete(ctx context.Context, tenantID, providerID, code, codeVerifier, expectedState, actualState string, opts StartOptions) (*Grant, error) {
	if expectedState == "" || actualState == "" || expectedState != actualState {
		return nil, ErrStateMismatch
	}
	cfg, err := c.store.GetProviderConfig(ctx, tenantID, providerID)
	if err != nil {
		return nil, err
	}
	prof := profileFor(c.registry, cfg)
	clientSecret, err := c.store.ResolveClientSecret(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve client secret: %w", err)
	}

	oCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: clientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       prof.Scopes,
		Endpoint:     oauth2.Endpoint{AuthURL: prof.AuthURL, TokenURL: prof.TokenURL},
	}

	tok, err := oCfg.Exchange(c.httpCtx(ctx), code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return nil, classifyProviderError(err)
	}

	var expiresAt *time.Time
	if !tok.Expiry.IsZero() {
		t := tok.Expiry
		expiresAt = &t
	}
	refreshTok := tok.RefreshToken
	if !prof.SupportsRefresh {
		refreshTok = ""
	}

	g := &Grant{
		TenantID:      tenantID,
		ProviderID:    cfg.ID,
		ProviderType:  cfg.ProviderType,
		ProviderName:  cfg.Name,
		ConnectionID:  opts.ConnectionID,
		ScopesGranted: scopesFromToken(tok, prof.Scopes),
		AccessToken:   tok.AccessToken,
		RefreshToken:  refreshTok,
		ExpiresAt:     expiresAt,
	}
	if err := c.store.CreateGrant(ctx, g, tok.AccessToken, refreshTok); err != nil {
		return nil, fmt.Errorf("persist grant: %w", err)
	}
	return g, nil
}

// Token returns a fresh access token for a grant. If the access token is
// expired (or expiring within the configured skew), it triggers a refresh
// first. Concurrent calls for the same grant dedupe via singleflight so the
// provider sees at most one refresh.
func (c *Client) Token(ctx context.Context, tenantID, grantID string) (string, error) {
	g, err := c.store.GetGrant(ctx, tenantID, grantID)
	if err != nil {
		return "", err
	}
	if g.IsRevoked() {
		return "", ErrGrantRevoked
	}
	if !g.NeedsRefresh(c.now(), c.skew) {
		return g.AccessToken, nil
	}
	refreshed, err := c.Refresh(ctx, tenantID, grantID)
	if err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// Refresh forces an exchange of the grant's refresh token for a new access
// token. Used by the background refresher and by the on-401 retry path.
// Concurrent calls for the same grant dedupe via singleflight.
func (c *Client) Refresh(ctx context.Context, tenantID, grantID string) (*Grant, error) {
	v, err, _ := c.sf.Do(grantID, func() (interface{}, error) {
		return c.refreshLocked(ctx, tenantID, grantID)
	})
	if err != nil {
		return nil, err
	}
	return v.(*Grant), nil
}

func (c *Client) refreshLocked(ctx context.Context, tenantID, grantID string) (*Grant, error) {
	g, err := c.store.GetGrant(ctx, tenantID, grantID)
	if err != nil {
		return nil, err
	}
	if g.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}
	if g.IsRevoked() {
		return nil, ErrGrantRevoked
	}

	cfg, err := c.store.GetProviderConfig(ctx, tenantID, g.ProviderID)
	if err != nil {
		return nil, err
	}
	prof := profileFor(c.registry, cfg)
	clientSecret, err := c.store.ResolveClientSecret(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve client secret: %w", err)
	}

	oCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: clientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: prof.AuthURL, TokenURL: prof.TokenURL},
	}
	ts := oCfg.TokenSource(c.httpCtx(ctx), &oauth2.Token{RefreshToken: g.RefreshToken})
	newTok, err := ts.Token()
	if err != nil {
		return nil, classifyProviderError(err)
	}

	var expiresAt *time.Time
	if !newTok.Expiry.IsZero() {
		t := newTok.Expiry
		expiresAt = &t
	}
	// Providers may rotate refresh tokens (Google, Microsoft do; HubSpot
	// sometimes does). If the response omitted one, keep the existing.
	refreshTok := newTok.RefreshToken
	if refreshTok == "" {
		refreshTok = g.RefreshToken
	}
	if err := c.store.UpdateTokens(ctx, grantID, newTok.AccessToken, refreshTok, expiresAt); err != nil {
		return nil, fmt.Errorf("persist refreshed tokens: %w", err)
	}

	g.AccessToken = newTok.AccessToken
	g.RefreshToken = refreshTok
	g.ExpiresAt = expiresAt
	now := c.now()
	g.LastRefreshedAt = &now
	return g, nil
}

// Revoke marks a grant revoked locally and (best-effort) calls the
// provider's revocation endpoint. A provider-side failure is logged via the
// returned error but does NOT prevent the local revoke — once an operator
// asks us to forget a grant, we forget it.
func (c *Client) Revoke(ctx context.Context, tenantID, grantID string) error {
	g, err := c.store.GetGrant(ctx, tenantID, grantID)
	if err != nil {
		return err
	}
	if g.IsRevoked() {
		return nil
	}
	cfg, err := c.store.GetProviderConfig(ctx, tenantID, g.ProviderID)
	if err != nil {
		// Provider config gone — still mark the grant revoked locally so
		// downstream lookups stop returning live tokens.
		_ = c.store.MarkRevoked(ctx, tenantID, grantID)
		return err
	}
	prof := profileFor(c.registry, cfg)

	var providerErr error
	if prof.RevokeURL != "" {
		// Prefer revoking the refresh token (kills all derived access tokens);
		// fall back to the access token if no refresh token is available.
		tokenToRevoke := g.RefreshToken
		hint := "refresh_token"
		if tokenToRevoke == "" {
			tokenToRevoke = g.AccessToken
			hint = "access_token"
		}
		if tokenToRevoke != "" {
			providerErr = c.postRevoke(ctx, prof.RevokeURL, cfg.ClientID, tokenToRevoke, hint)
		}
	}

	if err := c.store.MarkRevoked(ctx, tenantID, grantID); err != nil {
		return fmt.Errorf("mark revoked: %w", err)
	}
	return providerErr
}

func (c *Client) postRevoke(ctx context.Context, revokeURL, clientID, token, hint string) error {
	form := url.Values{}
	form.Set("token", token)
	form.Set("token_type_hint", hint)
	form.Set("client_id", clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: build revoke request: %v", ErrProviderError, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProviderError, err)
	}
	defer resp.Body.Close()
	// RFC 7009: a successful revocation returns 200; many providers also
	// return 200 for "already revoked / unknown token". 401 / 403 indicate
	// the request itself was rejected — surface that.
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%w: revoke returned %d", ErrProviderError, resp.StatusCode)
	}
	return nil
}

// httpCtx returns a context that carries c.http as the http.Client used by
// the oauth2 package (it looks for one in the context). This lets tests
// substitute an httptest server's client without our touching globals.
func (c *Client) httpCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, c.http)
}

// classifyProviderError maps a *oauth2.RetrieveError into one of our typed
// sentinels so callers can distinguish "user must reconnect" from
// "transient — retry later". Non-RetrieveError values are wrapped as
// ErrProviderError.
func classifyProviderError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		// RFC 6749 §5.2: invalid_grant means the refresh token is no longer
		// accepted — the user needs to re-authorise. invalid_client and
		// invalid_request are configuration / programmer errors, not
		// transient.
		body := strings.ToLower(string(re.Body))
		if strings.Contains(body, "invalid_grant") {
			return fmt.Errorf("%w: %s", ErrRefreshExpired, re.Error())
		}
		return fmt.Errorf("%w: %s", ErrProviderError, re.Error())
	}
	return fmt.Errorf("%w: %v", ErrProviderError, err)
}

// scopesFromToken extracts the scope list the provider actually issued. The
// oauth2 library stashes this in the Extra("scope") field for providers
// that include it in the token response (Microsoft, Google do; HubSpot
// embeds it elsewhere). Falls back to the requested scopes.
func scopesFromToken(tok *oauth2.Token, requested []string) []string {
	if v, ok := tok.Extra("scope").(string); ok && v != "" {
		return strings.Fields(v)
	}
	return requested
}

// randURLSafe returns a cryptographically random, URL-safe string of n
// random bytes (length on the wire is ~1.33×n).
func randURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge returns the S256 code challenge for an OAuth 2.0 PKCE
// verifier (RFC 7636 §4.2).
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
