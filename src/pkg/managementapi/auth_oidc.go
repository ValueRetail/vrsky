package managementapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/ValueRetail/vrsky/pkg/auth"
	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// OIDC sign-in (Phase 1C / #68).
//
// Flow:
//
//	1. GET  /api/v1/auth/oidc/{slug}/login
//	   - resolve tenant + OIDC config
//	   - build provider (cached by issuer)
//	   - generate state + PKCE verifier + nonce, stash in short-lived
//	     HttpOnly cookies
//	   - 302 to provider's authorization URL
//
//	2. GET  /api/v1/auth/oidc/callback?code=…&state=…
//	   - read state cookie, compare
//	   - exchange code + PKCE verifier for tokens
//	   - validate ID token (signature, issuer, audience, nonce)
//	   - extract email + sub
//	   - check allowed_domains (if set)
//	   - find user by oidc_subject; if not found auto-provision
//	   - mint session
//	   - set vrsky_session cookie + redirect to /
//
//	3. GET  /api/v1/auth/oidc/{slug}/available
//	   - returns {available: true, label: "..."} or 404
//	   - the UI uses this to decide whether to show the SSO button

const (
	oidcStateCookie    = "vrsky_oidc_state"
	oidcVerifierCookie = "vrsky_oidc_verifier"
	oidcNonceCookie    = "vrsky_oidc_nonce"
	oidcSlugCookie     = "vrsky_oidc_slug"
	sessionCookieName  = "vrsky_session"
	oidcCookieTTL      = 10 * time.Minute
)

// providerCache memoises the network call go-oidc makes to fetch
// /.well-known/openid-configuration. One entry per issuer URL.
type providerCache struct {
	mu    sync.Mutex
	cache map[string]*oidc.Provider
}

var providers = &providerCache{cache: map[string]*oidc.Provider{}}

func (p *providerCache) get(ctx context.Context, issuer string) (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prov, ok := p.cache[issuer]; ok {
		return prov, nil
	}
	prov, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	p.cache[issuer] = prov
	return prov, nil
}

// ===== Handlers =====

// HandleOIDCAvailable: GET /api/v1/auth/oidc/{slug}/available
//
// The UI uses this on the login page to decide whether to render an SSO
// button alongside the email/password form. The slug comes from the URL
// because at this point the user has no tenant header.
func (h *Handler) HandleOIDCAvailable(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	cfg, err := h.repo.GetOIDCConfigByTenantSlug(r.Context(), slug)
	if err != nil || cfg == nil {
		_ = writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	label := cfg.ProviderLabel
	if label == "" {
		label = "Single sign-on"
	}
	_ = writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"label":     label,
	})
}

// HandleOIDCLogin: GET /api/v1/auth/oidc/{slug}/login
func (h *Handler) HandleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")

	cfg, err := h.repo.GetOIDCConfigByTenantSlug(ctx, slug)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "OIDCNotConfigured",
			"this workspace does not have SSO configured", nil)
		return
	}

	prov, err := providers.get(ctx, cfg.IssuerURL)
	if err != nil {
		h.logAuthEvent(ctx, r, nil, "", "oidc_login", "failed",
			stringPtr("provider discovery failed: "+err.Error()))
		_ = writeError(w, http.StatusBadGateway, "ProviderError",
			"failed to reach identity provider", nil)
		return
	}

	clientSecret, err := h.resolveOIDCSecret(ctx, cfg)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ConfigError",
			"unable to read OIDC client secret", nil)
		return
	}

	state := randURLSafe(32)
	verifier := randURLSafe(64)
	nonce := randURLSafe(24)

	// PKCE: S256 challenge derived from the verifier.
	challenge := pkceChallenge(verifier)

	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: clientSecret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
	}

	setShortCookie(w, oidcStateCookie, state)
	setShortCookie(w, oidcVerifierCookie, verifier)
	setShortCookie(w, oidcNonceCookie, nonce)
	setShortCookie(w, oidcSlugCookie, slug)

	authURL := oauth2Cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oidc.Nonce(nonce),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleOIDCCallback: GET /api/v1/auth/oidc/callback
func (h *Handler) HandleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	// 1. Provider may report an error directly (user denied, config off).
	if errMsg := q.Get("error"); errMsg != "" {
		h.logAuthEvent(ctx, r, nil, "", "oidc_login", "failed",
			stringPtr("idp returned error: "+errMsg))
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized",
			"sign-in cancelled or rejected: "+errMsg, nil)
		return
	}

	// 2. Validate state cookie matches the query param (CSRF defense).
	stateCookie, _ := r.Cookie(oidcStateCookie)
	if stateCookie == nil || stateCookie.Value == "" || stateCookie.Value != q.Get("state") {
		h.logAuthEvent(ctx, r, nil, "", "oidc_login", "failed", stringPtr("state mismatch"))
		_ = writeError(w, http.StatusBadRequest, "InvalidState",
			"state mismatch; possible CSRF or expired login attempt", nil)
		return
	}
	verifierCookie, _ := r.Cookie(oidcVerifierCookie)
	nonceCookie, _ := r.Cookie(oidcNonceCookie)
	slugCookie, _ := r.Cookie(oidcSlugCookie)
	if verifierCookie == nil || slugCookie == nil || nonceCookie == nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest",
			"login session missing; please retry", nil)
		return
	}

	// 3. Resolve tenant + config.
	cfg, err := h.repo.GetOIDCConfigByTenantSlug(ctx, slugCookie.Value)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "OIDCNotConfigured", "tenant config gone", nil)
		return
	}
	clientSecret, err := h.resolveOIDCSecret(ctx, cfg)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ConfigError",
			"unable to read OIDC client secret", nil)
		return
	}
	prov, err := providers.get(ctx, cfg.IssuerURL)
	if err != nil {
		_ = writeError(w, http.StatusBadGateway, "ProviderError",
			"provider discovery failed", nil)
		return
	}

	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: clientSecret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
	}

	// 4. Exchange the code for tokens, supplying the PKCE verifier.
	token, err := oauth2Cfg.Exchange(ctx, q.Get("code"),
		oauth2.SetAuthURLParam("code_verifier", verifierCookie.Value),
	)
	if err != nil {
		h.logAuthEvent(ctx, r, nil, "", "oidc_login", "failed",
			stringPtr("token exchange failed: "+err.Error()))
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized",
			"token exchange failed", nil)
		return
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok || rawID == "" {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized",
			"id_token missing in provider response", nil)
		return
	}
	verifier := prov.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	idToken, err := verifier.Verify(ctx, rawID)
	if err != nil {
		h.logAuthEvent(ctx, r, nil, "", "oidc_login", "failed",
			stringPtr("id_token verification: "+err.Error()))
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized",
			"id_token verification failed", nil)
		return
	}
	if idToken.Nonce != nonceCookie.Value {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "nonce mismatch", nil)
		return
	}

	// 5. Extract claims we care about.
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Email == "" {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized",
			"id_token missing email claim", nil)
		return
	}
	email := strings.ToLower(claims.Email)
	subject := idToken.Subject

	// 6. allowed_domains enforcement.
	if !domainAllowed(email, cfg.AllowedDomains) {
		h.logAuthEvent(ctx, r, nil, email, "oidc_login", "denied",
			stringPtr("domain not in allowed_domains"))
		_ = writeError(w, http.StatusForbidden, "DomainNotAllowed",
			"your email domain is not permitted for this workspace", nil)
		return
	}

	// 7. Find or auto-provision the user.
	provider := cfg.IssuerURL // good-enough unique scope per IdP
	user, err := h.findOrProvisionOIDCUser(ctx, provider, subject, email, claims.Name, cfg)
	if err != nil {
		h.logAuthEvent(ctx, r, nil, email, "oidc_login", "failed",
			stringPtr("user provision: "+err.Error()))
		_ = writeError(w, http.StatusInternalServerError, "ProvisionFailed",
			"unable to create or look up user", nil)
		return
	}

	// 8. Mint a session.
	rawToken, hashedToken, err := auth.GenerateSessionToken()
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError",
			"failed to create session", nil)
		return
	}
	now := time.Now().UTC()
	session := &Session{
		ID:           uuid.New().String(),
		UserID:       user.ID,
		TokenHash:    hashedToken,
		IPAddress:    stringPtr(getClientIP(r)),
		UserAgent:    stringPtr(r.UserAgent()),
		CreatedAt:    now,
		ExpiresAt:    auth.CalculateSessionExpiry(),
		LastActivity: now,
		IsActive:     true,
	}
	if err := h.repo.CreateSession(ctx, session); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError",
			"failed to persist session", nil)
		return
	}
	_ = h.repo.UpdateUserLastLogin(ctx, user.ID)

	// 9. Clear the short-lived OIDC cookies; set the long session cookie.
	clearCookie(w, oidcStateCookie)
	clearCookie(w, oidcVerifierCookie)
	clearCookie(w, oidcNonceCookie)
	clearCookie(w, oidcSlugCookie)
	setSessionCookie(w, rawToken, session.ExpiresAt)

	h.logAuthEvent(ctx, r, &user.ID, email, "oidc_login", "success", nil)

	// Land the user on the dashboard. The UI may parse this into a state
	// machine via a /post-login route in the future.
	http.Redirect(w, r, "/", http.StatusFound)
}

// findOrProvisionOIDCUser implements the user-side of the auto-provision
// flow described in #68: look up by oidc_subject; fall back to email
// match (to link a pre-existing password account to SSO); otherwise
// create a new user with the tenant's default_role.
func (h *Handler) findOrProvisionOIDCUser(ctx context.Context, provider, subject, email, fullName string, cfg *OIDCConfig) (*User, error) {
	if u, err := h.repo.GetUserByOIDCSubject(ctx, provider, subject); err != nil {
		return nil, err
	} else if u != nil {
		return u, nil
	}

	// Existing user with the same email? Link them.
	if existing, err := h.repo.GetUserByEmail(ctx, email); err == nil && existing != nil {
		if err := h.repo.LinkUserOIDC(ctx, existing.ID, provider, subject); err != nil {
			return nil, err
		}
		existing.OIDCProvider = provider
		existing.OIDCSubject = subject
		return existing, nil
	}

	// First sign-in: create a fresh user. No password — they will always
	// be steered back through SSO.
	user := &User{
		ID:            uuid.New().String(),
		Email:         email,
		PasswordHash:  "",
		FullName:      fullName,
		Status:        UserStatusActive,
		EmailVerified: true, // the IdP already verified the address
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := h.repo.CreateUser(ctx, user); err != nil {
		return nil, err
	}
	if err := h.repo.LinkUserOIDC(ctx, user.ID, provider, subject); err != nil {
		return nil, err
	}
	user.OIDCProvider = provider
	user.OIDCSubject = subject
	return user, nil
}

func (h *Handler) resolveOIDCSecret(ctx context.Context, cfg *OIDCConfig) (string, error) {
	if cfg.ClientSecretID == "" {
		return "", errors.New("client_secret_id missing")
	}
	ct, err := h.repo.GetSecretCiphertext(ctx, cfg.TenantID, cfg.ClientSecretID)
	if err != nil {
		return "", fmt.Errorf("lookup secret: %w", err)
	}
	key, err := crypto.Key()
	if err != nil {
		return "", err
	}
	plain, err := crypto.Decrypt(ct, key)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return plain, nil
}

// ===== Helpers =====

// domainAllowed returns true when allowed is empty (no restriction) OR the
// email's domain matches one of the allowed entries (case-insensitive).
func domainAllowed(email string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(a), domain) {
			return true
		}
	}
	return false
}

// randURLSafe returns a URL-safe random string of the requested byte length.
func randURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// pkceChallenge derives the S256 code_challenge from a verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// cookieSecure controls the Secure attribute on auth/OAuth cookies. It must be
// false for local http development — Safari (unlike Chrome) refuses to send
// Secure cookies over http://localhost, which breaks the cookie-based OAuth/OIDC
// callback flow. Defaults to true (production over HTTPS); set COOKIE_SECURE=false
// for an http dev stack.
var cookieSecure = os.Getenv("COOKIE_SECURE") != "false"

// setShortCookie writes a 10-minute HttpOnly cookie used during the OIDC
// round-trip (state/PKCE/nonce/slug).
func setShortCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(oidcCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// setSessionCookie persists the session token after a successful login.
// Lax SameSite is required because the redirect back from the IdP is a
// top-level GET from another origin; Strict would lose the cookie.
func setSessionCookie(w http.ResponseWriter, rawToken string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    rawToken,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// urlEscape is unused inline above but kept around for future query-string
// composition (e.g. error-redirect targets).
var _ = url.QueryEscape
