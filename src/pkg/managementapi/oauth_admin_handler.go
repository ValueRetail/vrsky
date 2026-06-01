package managementapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/oauth"
)

// OAuth 2.0 admin endpoints: per-tenant provider configuration CRUD.
// Layout mirrors the OIDC admin handler (oidc_admin_handler.go) — section
// comments, tenant-from-context extraction, audit calls via SetAuditAction
// + SetAuditDetail, error responses via writeError.

// oauthProviderRequest is the JSON shape of a create / update request.
type oauthProviderRequest struct {
	Name         string            `json:"name"`
	ProviderType string            `json:"provider_type"`
	ClientID     string            `json:"client_id"`
	ClientSecret string            `json:"client_secret,omitempty"` // PUT may omit
	AuthURL      string            `json:"auth_url,omitempty"`
	TokenURL     string            `json:"token_url,omitempty"`
	RevokeURL    string            `json:"revoke_url,omitempty"`
	Scopes       []string          `json:"scopes,omitempty"`
	RedirectURL  string            `json:"redirect_url"`
	ExtraParams  map[string]string `json:"extra_params,omitempty"`
}

// oauthProviderResponse is the safe (secret-less) shape returned to clients.
type oauthProviderResponse struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	ProviderType string            `json:"provider_type"`
	ClientID     string            `json:"client_id"`
	AuthURL      string            `json:"auth_url"`
	TokenURL     string            `json:"token_url"`
	RevokeURL    string            `json:"revoke_url,omitempty"`
	Scopes       []string          `json:"scopes"`
	RedirectURL  string            `json:"redirect_url"`
	ExtraParams  map[string]string `json:"extra_params,omitempty"`
}

func toOAuthProviderResponse(cfg *oauth.ProviderConfig) oauthProviderResponse {
	return oauthProviderResponse{
		ID:           cfg.ID,
		Name:         cfg.Name,
		ProviderType: cfg.ProviderType,
		ClientID:     cfg.ClientID,
		AuthURL:      cfg.AuthURL,
		TokenURL:     cfg.TokenURL,
		RevokeURL:    cfg.RevokeURL,
		Scopes:       cfg.Scopes,
		RedirectURL:  cfg.RedirectURL,
		ExtraParams:  cfg.ExtraParams,
	}
}

// validateProviderRequest applies sanity checks. URL validation is loose
// (the OIDC validator's same shape) — we accept anything looking like an
// https URL and let the provider tell us if it's wrong at auth time.
func validateProviderRequest(req *oauthProviderRequest, requireSecret bool) error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(req.ProviderType) == "" {
		return errors.New("provider_type is required")
	}
	if strings.TrimSpace(req.ClientID) == "" {
		return errors.New("client_id is required")
	}
	if requireSecret && strings.TrimSpace(req.ClientSecret) == "" {
		return errors.New("client_secret is required")
	}
	if strings.TrimSpace(req.RedirectURL) == "" {
		return errors.New("redirect_url is required")
	}
	// auth_url + token_url are only required when the profile doesn't seed
	// them. Generic / custom providers require both.
	if req.ProviderType == "custom" {
		if !strings.HasPrefix(req.AuthURL, "https://") {
			return errors.New("auth_url must be https for custom providers")
		}
		if !strings.HasPrefix(req.TokenURL, "https://") {
			return errors.New("token_url must be https for custom providers")
		}
	}
	return nil
}

// applyProfileDefaults overlays the seeded profile's URLs + scopes onto a
// config that didn't supply them. This is what makes "create a Microsoft365
// provider — just give me client_id and redirect_url" work.
func applyProfileDefaults(cfg *oauth.ProviderConfig) {
	prof, ok := oauth.DefaultRegistry().Get(cfg.ProviderType)
	if !ok {
		return
	}
	if cfg.AuthURL == "" {
		cfg.AuthURL = prof.AuthURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = prof.TokenURL
	}
	if cfg.RevokeURL == "" {
		cfg.RevokeURL = prof.RevokeURL
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = append([]string(nil), prof.Scopes...)
	}
	// Salesforce sandbox orgs authenticate against test.salesforce.com. The
	// admin signals this with extra_params.environment=sandbox; swap the
	// host on all three URLs unless they were explicitly overridden.
	if cfg.ProviderType == "salesforce" && cfg.ExtraParams["environment"] == "sandbox" {
		if cfg.AuthURL == "" || cfg.AuthURL == prof.AuthURL {
			cfg.AuthURL = oauth.SalesforceSandboxAuthURL
		}
		if cfg.TokenURL == "" || cfg.TokenURL == prof.TokenURL {
			cfg.TokenURL = oauth.SalesforceSandboxTokenURL
		}
		if cfg.RevokeURL == "" || cfg.RevokeURL == prof.RevokeURL {
			cfg.RevokeURL = oauth.SalesforceSandboxRevokeURL
		}
	}
}

// CreateOAuthProvider handles POST /api/v1/oauth/providers.
func (h *Handler) CreateOAuthProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	var req oauthProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	if err := validateProviderRequest(&req, true); err != nil {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
		return
	}
	cfg := &oauth.ProviderConfig{
		TenantID:     tenantID,
		Name:         req.Name,
		ProviderType: req.ProviderType,
		ClientID:     req.ClientID,
		AuthURL:      req.AuthURL,
		TokenURL:     req.TokenURL,
		RevokeURL:    req.RevokeURL,
		Scopes:       req.Scopes,
		RedirectURL:  req.RedirectURL,
		ExtraParams:  req.ExtraParams,
	}
	applyProfileDefaults(cfg)

	if err := h.repo.CreateOAuthProvider(ctx, cfg, req.ClientSecret); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to create provider", nil)
		return
	}
	SetAuditAction(ctx, "oauth.provider.create")
	SetAuditResource(ctx, "oauth_provider", cfg.ID)
	SetAuditDetail(ctx, "provider_type", cfg.ProviderType)
	SetAuditDetail(ctx, "name", cfg.Name)
	_ = writeJSON(w, http.StatusCreated, toOAuthProviderResponse(cfg))
}

// ListOAuthProvidersHandler handles GET /api/v1/oauth/providers.
func (h *Handler) ListOAuthProvidersHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	cfgs, err := h.repo.ListOAuthProviders(ctx, tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to list providers", nil)
		return
	}
	out := make([]oauthProviderResponse, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, toOAuthProviderResponse(c))
	}
	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"providers": out})
}

// GetOAuthProvider handles GET /api/v1/oauth/providers/{id}.
func (h *Handler) GetOAuthProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	id := r.PathValue("id")
	cfg, err := h.repo.GetProviderConfig(ctx, tenantID, id)
	if errors.Is(err, oauth.ErrProviderNotFound) {
		_ = writeError(w, http.StatusNotFound, "NotFound", "provider not found", nil)
		return
	}
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to load provider", nil)
		return
	}
	_ = writeJSON(w, http.StatusOK, toOAuthProviderResponse(cfg))
}

// UpdateOAuthProvider handles PUT /api/v1/oauth/providers/{id}.
func (h *Handler) UpdateOAuthProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	id := r.PathValue("id")
	existing, err := h.repo.GetProviderConfig(ctx, tenantID, id)
	if errors.Is(err, oauth.ErrProviderNotFound) {
		_ = writeError(w, http.StatusNotFound, "NotFound", "provider not found", nil)
		return
	}
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to load provider", nil)
		return
	}

	var req oauthProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	// On PUT, client_secret is OPTIONAL — empty means "keep what we have".
	if err := validateProviderRequest(&req, false); err != nil {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
		return
	}

	existing.Name = req.Name
	existing.ProviderType = req.ProviderType
	existing.ClientID = req.ClientID
	existing.AuthURL = req.AuthURL
	existing.TokenURL = req.TokenURL
	existing.RevokeURL = req.RevokeURL
	existing.Scopes = req.Scopes
	existing.RedirectURL = req.RedirectURL
	existing.ExtraParams = req.ExtraParams
	applyProfileDefaults(existing)

	if err := h.repo.UpdateOAuthProvider(ctx, existing, req.ClientSecret); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to update provider", nil)
		return
	}
	SetAuditAction(ctx, "oauth.provider.update")
	SetAuditResource(ctx, "oauth_provider", existing.ID)
	SetAuditDetail(ctx, "provider_type", existing.ProviderType)
	SetAuditDetail(ctx, "name", existing.Name)
	SetAuditDetail(ctx, "secret_rotated", req.ClientSecret != "")
	_ = writeJSON(w, http.StatusOK, toOAuthProviderResponse(existing))
}

// DeleteOAuthProvider handles DELETE /api/v1/oauth/providers/{id}.
func (h *Handler) DeleteOAuthProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	id := r.PathValue("id")
	if err := h.repo.DeleteOAuthProvider(ctx, tenantID, id); err != nil {
		if errors.Is(err, oauth.ErrProviderNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "provider not found", nil)
			return
		}
		// The "still has active grants" error is the user's fault — surface it.
		_ = writeError(w, http.StatusConflict, "Conflict", err.Error(), nil)
		return
	}
	SetAuditAction(ctx, "oauth.provider.delete")
	SetAuditResource(ctx, "oauth_provider", id)
	w.WriteHeader(http.StatusNoContent)
}
