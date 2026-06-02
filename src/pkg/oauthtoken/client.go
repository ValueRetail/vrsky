// Package oauthtoken is the worker-side client for the management-api OAuth
// token endpoint (GET /api/v1/oauth/grants/{id}/token). Workers that deliver
// to / poll OAuth-protected APIs use it to obtain a fresh access token for a
// grant without holding the encryption key or talking to the database — all
// refresh coordination stays in management-api (single Client + singleflight).
//
// Tokens are cached in-process until just before they expire; a 401 from the
// downstream API should trigger ForceToken (which asks management-api to
// refresh) followed by exactly one retry.
package oauthtoken

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// expirySkew is how long before a token's stated expiry we treat it as stale
// and re-fetch, so a request never goes out with a token about to lapse.
const expirySkew = 30 * time.Second

type cacheEntry struct {
	token     string
	expiresAt *time.Time // nil = unknown; treated as always-fetch
}

// Client fetches and caches OAuth access tokens from management-api.
type Client struct {
	baseURL      string
	serviceToken string
	http         *http.Client
	now          func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry // keyed by grantID
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client (tests inject httptest's).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithClock overrides the time source (tests drive expiry deterministically).
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// New builds a token client. baseURL is the management-api root (e.g.
// http://management-api:3000); serviceToken is the shared secret presented as
// X-Service-Token. If either is empty, Configured() returns false and the
// worker should treat OAuth auth as unavailable.
func New(baseURL, serviceToken string, opts ...Option) *Client {
	c := &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		serviceToken: serviceToken,
		http:         &http.Client{Timeout: 10 * time.Second},
		now:          time.Now,
		cache:        map[string]cacheEntry{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Configured reports whether the client has both a base URL and a service
// token — i.e. whether OAuth token resolution is usable in this deployment.
func (c *Client) Configured() bool {
	return c.baseURL != "" && c.serviceToken != ""
}

// Token returns a cached access token for the grant if it is still fresh,
// otherwise fetches one from management-api (which refreshes transparently
// only if the token is near expiry).
func (c *Client) Token(ctx context.Context, tenantID, grantID string) (string, error) {
	c.mu.Lock()
	entry, ok := c.cache[grantID]
	c.mu.Unlock()
	if ok && c.fresh(entry) {
		return entry.token, nil
	}
	return c.fetch(ctx, tenantID, grantID, false)
}

// ForceToken bypasses the cache and asks management-api to refresh the grant
// before returning a token. Used after a 401 from the downstream API, where
// the current token is bad even if it isn't near its stated expiry.
func (c *Client) ForceToken(ctx context.Context, tenantID, grantID string) (string, error) {
	return c.fetch(ctx, tenantID, grantID, true)
}

// Invalidate drops a grant's cached token (e.g. on revoke).
func (c *Client) Invalidate(grantID string) {
	c.mu.Lock()
	delete(c.cache, grantID)
	c.mu.Unlock()
}

func (c *Client) fresh(e cacheEntry) bool {
	if e.token == "" {
		return false
	}
	if e.expiresAt == nil {
		return false // unknown expiry — always re-fetch to be safe
	}
	return c.now().Add(expirySkew).Before(*e.expiresAt)
}

func (c *Client) fetch(ctx context.Context, tenantID, grantID string, force bool) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("oauthtoken: client not configured (missing base URL or service token)")
	}
	u := fmt.Sprintf("%s/api/v1/oauth/grants/%s/token", c.baseURL, url.PathEscape(grantID))
	if force {
		u += "?refresh=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Service-Token", c.serviceToken)
	req.Header.Set("X-Tenant-ID", tenantID)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauthtoken: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauthtoken: token endpoint returned %d", resp.StatusCode)
	}

	var body struct {
		AccessToken string     `json:"access_token"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("oauthtoken: decode response: %w", err)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("oauthtoken: empty access token in response")
	}

	c.mu.Lock()
	c.cache[grantID] = cacheEntry{token: body.AccessToken, expiresAt: body.ExpiresAt}
	c.mu.Unlock()

	return body.AccessToken, nil
}
