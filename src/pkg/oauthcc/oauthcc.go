// Package oauthcc is a minimal OAuth 2.0 client-credentials (service-to-service)
// token source with in-memory caching. It's used by connectors to machine-to-
// machine APIs — e.g. Microsoft Dynamics 365 Business Central (Entra ID) and
// Visma.net (Visma Connect) — which authenticate unattended integrations with
// the client_credentials grant rather than a user-delegated grant.
package oauthcc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client fetches and caches a bearer token for one (tokenURL, clientID, scope).
// It is safe for concurrent use.
type Client struct {
	tokenURL     string
	clientID     string
	clientSecret string
	scope        string

	httpClient *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// New returns a token source. scope may be empty for providers that don't need
// it. tokenURL is the provider's token endpoint (e.g. Entra ID
// https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token).
func New(tokenURL, clientID, clientSecret, scope string) *Client {
	return &Client{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		scope:        scope,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// WithHTTPClient overrides the HTTP client (tests point it at a stub server).
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	if h != nil {
		c.httpClient = h
	}
	return c
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"` // seconds
	TokenType   string `json:"token_type"`
}

// Token returns a valid bearer token, fetching a new one when the cache is empty
// or within 60s of expiry.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expiry.Add(-60*time.Second)) {
		return c.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	if c.scope != "" {
		form.Set("scope", c.scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token endpoint %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("token endpoint returned no access_token")
	}

	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute // conservative default when expires_in is absent
	}
	c.token = tr.AccessToken
	c.expiry = time.Now().Add(ttl)
	return c.token, nil
}
