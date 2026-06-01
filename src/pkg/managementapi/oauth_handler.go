package managementapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/oauth"
)

// Cookie names used during a single auth-code round-trip. Mirrors the OIDC
// pattern in auth_oidc.go but with distinct names so an OAuth flow and an
// OIDC login can be in flight in adjacent browser tabs without clobbering
// each other.
const (
	oauthStateCookie       = "vrsky_oauth_state"
	oauthVerifierCookie    = "vrsky_oauth_verifier"
	oauthProviderIDCookie  = "vrsky_oauth_provider_id"
	oauthTenantIDCookie    = "vrsky_oauth_tenant_id"
	oauthConnectionCookie  = "vrsky_oauth_connection_id"
	oauthExtraParamsCookie = "vrsky_oauth_extra" // base64(JSON) of provider-specific extras (e.g. Shopify shop)
)

// startOAuthRequest is the optional body of POST /providers/{id}/start.
type startOAuthRequest struct {
	ConnectionID *string           `json:"connection_id,omitempty"`
	ExtraParams  map[string]string `json:"extra_params,omitempty"`
}

// startOAuthResponse carries the URL the UI should redirect the browser to.
type startOAuthResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

// StartOAuth handles POST /api/v1/oauth/providers/{id}/start.
//
// Sets HttpOnly cookies carrying state, verifier, provider ID, tenant ID
// and (optional) connection ID, then returns the authorize URL. The UI
// opens that URL in a popup; the provider eventually redirects back to
// /api/v1/oauth/callback (browser-driven), at which point the cookies are
// validated and the auth code exchanged for tokens.
func (h *Handler) StartOAuth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	if h.oauthClient == nil {
		_ = writeError(w, http.StatusServiceUnavailable, "OAuthUnavailable", "OAuth client is not configured on this server", nil)
		return
	}
	providerID := r.PathValue("id")

	var req startOAuthRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
			return
		}
	}

	authURL, state, verifier, err := h.oauthClient.StartAuth(ctx, tenantID, providerID, oauth.StartOptions{
		ConnectionID: req.ConnectionID,
		ExtraParams:  req.ExtraParams,
	})
	if errors.Is(err, oauth.ErrProviderNotFound) {
		_ = writeError(w, http.StatusNotFound, "NotFound", "provider not found", nil)
		return
	}
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to start auth", nil)
		return
	}

	setShortCookie(w, oauthStateCookie, state)
	setShortCookie(w, oauthVerifierCookie, verifier)
	setShortCookie(w, oauthProviderIDCookie, providerID)
	setShortCookie(w, oauthTenantIDCookie, tenantID)
	if req.ConnectionID != nil {
		setShortCookie(w, oauthConnectionCookie, *req.ConnectionID)
	} else {
		clearCookie(w, oauthConnectionCookie)
	}
	// Carry provider-specific extras (e.g. Shopify's shop subdomain) to the
	// callback so the token exchange can template the endpoint URL. Base64 so
	// the JSON survives cookie value restrictions.
	if len(req.ExtraParams) > 0 {
		b, _ := json.Marshal(req.ExtraParams)
		setShortCookie(w, oauthExtraParamsCookie, base64.RawURLEncoding.EncodeToString(b))
	} else {
		clearCookie(w, oauthExtraParamsCookie)
	}
	_ = writeJSON(w, http.StatusOK, startOAuthResponse{AuthorizeURL: authURL})
}

// HandleOAuthCallback handles GET /api/v1/oauth/callback. It is called by
// the user's browser (redirected by the provider) and is therefore public —
// authorisation is recovered from the short-lived cookies set by
// StartOAuth. On success it redirects the browser to /oauth/connected
// with ?grant_id=...; the popup UI listens for that and closes itself.
func (h *Handler) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.oauthClient == nil {
		http.Error(w, "OAuth client is not configured on this server", http.StatusServiceUnavailable)
		return
	}

	stateCk, _ := r.Cookie(oauthStateCookie)
	verifierCk, _ := r.Cookie(oauthVerifierCookie)
	providerCk, _ := r.Cookie(oauthProviderIDCookie)
	tenantCk, _ := r.Cookie(oauthTenantIDCookie)
	if stateCk == nil || verifierCk == nil || providerCk == nil || tenantCk == nil {
		http.Error(w, "missing or expired OAuth session cookies", http.StatusBadRequest)
		return
	}

	// Recover provider-specific extras (e.g. Shopify shop) before clearing.
	var extraParams map[string]string
	if extraCk, _ := r.Cookie(oauthExtraParamsCookie); extraCk != nil && extraCk.Value != "" {
		if b, decErr := base64.RawURLEncoding.DecodeString(extraCk.Value); decErr == nil {
			_ = json.Unmarshal(b, &extraParams)
		}
	}

	clearCookie(w, oauthStateCookie)
	clearCookie(w, oauthVerifierCookie)
	clearCookie(w, oauthProviderIDCookie)
	clearCookie(w, oauthTenantIDCookie)
	clearCookie(w, oauthConnectionCookie)
	clearCookie(w, oauthExtraParamsCookie)

	q := r.URL.Query()
	// If the provider returned an error, surface it.
	if errCode := q.Get("error"); errCode != "" {
		http.Error(w, "provider returned error: "+errCode, http.StatusBadRequest)
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state in callback", http.StatusBadRequest)
		return
	}

	var connectionID *string
	if connCk, _ := r.Cookie(oauthConnectionCookie); connCk != nil && connCk.Value != "" {
		c := connCk.Value
		connectionID = &c
	}

	grant, err := h.oauthClient.Complete(ctx, tenantCk.Value, providerCk.Value, code, verifierCk.Value, stateCk.Value, state, oauth.StartOptions{
		ConnectionID: connectionID,
		ExtraParams:  extraParams,
	})
	if errors.Is(err, oauth.ErrStateMismatch) {
		http.Error(w, "state mismatch — possible CSRF", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "failed to exchange code: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Audit. The callback is hit via a browser redirect from the IdP, so it
	// does NOT pass through AuditMiddleware (which writes audit_log for the
	// JSON API surface). Emit the grant entry directly to keep parity with
	// the revoke path's audit record (criterion #5: grant + revoke audited).
	h.logOAuthGrant(ctx, r, grant)

	// Redirect to the UI's "connected" page. The popup is expected to read
	// the grant_id and close itself.
	redirectURL := "/oauth/connected?grant_id=" + grant.ID
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// ListOAuthGrants handles GET /api/v1/oauth/grants.
func (h *Handler) ListOAuthGrants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	grants, err := h.repo.ListGrants(ctx, tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to list grants", nil)
		return
	}
	out := make([]oauthGrantResponse, 0, len(grants))
	for _, g := range grants {
		out = append(out, toOAuthGrantResponse(g))
	}
	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"grants": out})
}

// GetOAuthGrant handles GET /api/v1/oauth/grants/{id}.
func (h *Handler) GetOAuthGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	id := r.PathValue("id")
	g, err := h.repo.GetGrantMeta(ctx, tenantID, id)
	if errors.Is(err, oauth.ErrGrantNotFound) {
		_ = writeError(w, http.StatusNotFound, "NotFound", "grant not found", nil)
		return
	}
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to load grant", nil)
		return
	}
	_ = writeJSON(w, http.StatusOK, toOAuthGrantResponse(g))
}

// RevokeOAuthGrant handles POST /api/v1/oauth/grants/{id}/revoke.
func (h *Handler) RevokeOAuthGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	if h.oauthClient == nil {
		_ = writeError(w, http.StatusServiceUnavailable, "OAuthUnavailable", "OAuth client is not configured on this server", nil)
		return
	}
	id := r.PathValue("id")
	err = h.oauthClient.Revoke(ctx, tenantID, id)
	// The Client.Revoke contract: local revoke succeeded; the returned
	// error (if any) only reports the provider-side call. Treat both
	// outcomes as 200 OK but include the provider-error in the response
	// payload so the UI can surface it.
	providerWarning := ""
	if err != nil && !errors.Is(err, oauth.ErrGrantNotFound) {
		providerWarning = err.Error()
	}
	if errors.Is(err, oauth.ErrGrantNotFound) {
		_ = writeError(w, http.StatusNotFound, "NotFound", "grant not found", nil)
		return
	}
	SetAuditAction(ctx, "oauth.revoke")
	SetAuditResource(ctx, "oauth_grant", id)
	resp := map[string]interface{}{"revoked": true}
	if providerWarning != "" {
		resp["provider_warning"] = providerWarning
	}
	_ = writeJSON(w, http.StatusOK, resp)
}

// oauthGrantResponse is the safe (token-less) shape returned to clients.
type oauthGrantResponse struct {
	ID                   string     `json:"id"`
	ProviderID           string     `json:"provider_id"`
	ProviderType         string     `json:"provider_type"`
	ProviderName         string     `json:"provider_name"`
	ConnectionID         *string    `json:"connection_id,omitempty"`
	UserIdentifier       string     `json:"user_identifier,omitempty"`
	ScopesGranted        []string   `json:"scopes_granted"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	LastRefreshedAt      *time.Time `json:"last_refreshed_at,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
	RefreshFailedAt      *time.Time `json:"refresh_failed_at,omitempty"`
	RefreshFailureReason string     `json:"refresh_failure_reason,omitempty"`
	NeedsReconnect       bool       `json:"needs_reconnect"`
}

func toOAuthGrantResponse(g *oauth.Grant) oauthGrantResponse {
	return oauthGrantResponse{
		ID:              g.ID,
		ProviderID:      g.ProviderID,
		ProviderType:    g.ProviderType,
		ProviderName:    g.ProviderName,
		ConnectionID:    g.ConnectionID,
		UserIdentifier:  g.UserIdentifier,
		ScopesGranted:   g.ScopesGranted,
		ExpiresAt:       g.ExpiresAt,
		LastRefreshedAt: g.LastRefreshedAt,
		RevokedAt:       g.RevokedAt,
		// NeedsReconnect = the refresher hit ErrRefreshExpired and gave up.
		NeedsReconnect: g.RefreshToken == "" && g.RevokedAt == nil && g.ExpiresAt != nil && g.ExpiresAt.Before(time.Now()),
	}
}

// logOAuthGrant writes an audit_log entry for a successful authorization.
// The browser-redirect callback bypasses AuditMiddleware, so we emit the
// entry directly here — the same audit_log table the revoke path writes via
// SetAuditAction. Best-effort: the grant has already been persisted, so an
// audit write failure is logged but does not fail the callback.
func (h *Handler) logOAuthGrant(ctx context.Context, r *http.Request, g *oauth.Grant) {
	entry := &AuditEntry{
		TenantID:     g.TenantID,
		ActorKind:    "user",
		Action:       "oauth.grant",
		ResourceType: "oauth_grant",
		ResourceID:   g.ID,
		Method:       r.Method,
		Path:         r.URL.Path,
		StatusCode:   http.StatusFound,
		IPAddress:    getClientIP(r),
		UserAgent:    r.UserAgent(),
		Details: map[string]interface{}{
			"provider_type":   g.ProviderType,
			"provider_name":   g.ProviderName,
			"user_identifier": g.UserIdentifier,
			"scopes":          strings.Join(g.ScopesGranted, ","),
		},
	}
	if err := h.repo.CreateAuditEntry(ctx, entry); err != nil {
		slog.Warn("failed to write oauth.grant audit entry",
			"grant_id", g.ID, "tenant_id", g.TenantID, "error", err)
	}
}
