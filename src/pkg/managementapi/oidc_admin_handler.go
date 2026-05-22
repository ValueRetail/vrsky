package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Admin CRUD for a tenant's OIDC config (#68).
//
//	GET    /api/v1/tenants/{id}/oidc      read metadata (no secret)
//	PUT    /api/v1/tenants/{id}/oidc      upsert
//	DELETE /api/v1/tenants/{id}/oidc      remove
//
// All three are wired behind SessionAuthMiddleware + TenantMemberMiddleware
// + RequireRole("admin") in handler.go::RegisterAuthRoutes.

// oidcUpsertRequest mirrors OIDCConfig but the secret is supplied as
// plaintext on PUT; the handler encrypts it via #66's secrets table
// before storing the resulting UUID reference.
type oidcUpsertRequest struct {
	IssuerURL      string   `json:"issuer_url"`
	ClientID       string   `json:"client_id"`
	ClientSecret   string   `json:"client_secret"`
	RedirectURL    string   `json:"redirect_url"`
	Scopes         []string `json:"scopes"`
	AllowedDomains []string `json:"allowed_domains"`
	DefaultRole    string   `json:"default_role"`
	ProviderLabel  string   `json:"provider_label"`
}

// HandleOIDCConfigRead: GET /api/v1/tenants/{id}/oidc
func (h *Handler) HandleOIDCConfigRead(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	cfg, err := h.repo.GetOIDCConfigByTenantID(r.Context(), tenantID)
	if err != nil {
		if errors.Is(err, ErrOIDCConfigNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "OIDC not configured", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to load OIDC config", nil)
		return
	}
	// Strip the secret ID from the response so admins can't trivially harvest
	// it from the page. They can still rotate via PUT.
	cfg.ClientSecretID = ""
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: cfg})
}

// HandleOIDCConfigUpsert: PUT /api/v1/tenants/{id}/oidc
func (h *Handler) HandleOIDCConfigUpsert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")

	r.Body = http.MaxBytesReader(w, r.Body, 1*1024*1024)
	var req oidcUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body required", nil)
			return
		}
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	if err := validateOIDCUpsert(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
		return
	}

	// 1. Look up the existing config (if any) so we can reuse the same
	//    secret slot when the caller didn't supply a new value. This makes
	//    "edit allowed_domains" a no-secret operation.
	var clientSecretID string
	existing, _ := h.repo.GetOIDCConfigByTenantID(ctx, tenantID)
	if existing != nil {
		clientSecretID = existing.ClientSecretID
	}

	if req.ClientSecret != "" {
		secretName := "oidc-client-secret"
		secret, err := h.createOrUpdateOIDCSecret(ctx, tenantID, clientSecretID, secretName, req.ClientSecret)
		if err != nil {
			_ = writeError(w, http.StatusInternalServerError, "SecretError", "failed to store client secret", nil)
			return
		}
		clientSecretID = secret.ID
	}
	if clientSecretID == "" {
		_ = writeError(w, http.StatusBadRequest, "ValidationError",
			"client_secret is required on first configuration", nil)
		return
	}

	cfg := &OIDCConfig{
		TenantID:       tenantID,
		IssuerURL:      req.IssuerURL,
		ClientID:       req.ClientID,
		ClientSecretID: clientSecretID,
		RedirectURL:    req.RedirectURL,
		Scopes:         req.Scopes,
		AllowedDomains: req.AllowedDomains,
		DefaultRole:    req.DefaultRole,
		ProviderLabel:  req.ProviderLabel,
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = "viewer"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "profile"}
	}
	if err := h.repo.UpsertOIDCConfig(ctx, cfg); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to save OIDC config", nil)
		return
	}
	// Drop the secret ID before returning, same reasoning as the read path.
	cfg.ClientSecretID = ""
	SetAuditDetail(ctx, "issuer_url", cfg.IssuerURL)
	SetAuditDetail(ctx, "client_id", cfg.ClientID)
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: cfg})
}

// HandleOIDCConfigDelete: DELETE /api/v1/tenants/{id}/oidc
func (h *Handler) HandleOIDCConfigDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")
	if err := h.repo.DeleteOIDCConfig(ctx, tenantID); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to remove OIDC config", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createOrUpdateOIDCSecret encrypts the plaintext client secret via the
// secrets table (#66) and returns the resulting Secret row. When the
// tenant already has an OIDC secret of the same name, UpdateSecret is
// used to rewrap it in place.
func (h *Handler) createOrUpdateOIDCSecret(ctx context.Context, tenantID, existingID, name, plaintext string) (*Secret, error) {
	key, err := crypto.Key()
	if err != nil {
		return nil, err
	}
	ct, err := crypto.Encrypt(plaintext, key)
	if err != nil {
		return nil, err
	}
	if existingID != "" {
		return h.repo.UpdateSecret(ctx, tenantID, existingID, "", ct)
	}
	return h.repo.CreateSecret(ctx, tenantID, name, ct)
}

// validateOIDCUpsert centralises the field-level checks. Keeping this in a
// pure function makes it trivial to unit-test without spinning up HTTP.
func validateOIDCUpsert(req *oidcUpsertRequest) error {
	if req.IssuerURL == "" {
		return errors.New("issuer_url is required")
	}
	if !strings.HasPrefix(req.IssuerURL, "https://") {
		return errors.New("issuer_url must use https://")
	}
	if req.ClientID == "" {
		return errors.New("client_id is required")
	}
	if req.RedirectURL == "" {
		return errors.New("redirect_url is required")
	}
	if !(strings.HasPrefix(req.RedirectURL, "https://") || strings.HasPrefix(req.RedirectURL, "http://localhost")) {
		return errors.New("redirect_url must use https:// (or http://localhost for development)")
	}
	if len(req.Scopes) > 0 {
		hasOpenID := false
		for _, s := range req.Scopes {
			if strings.EqualFold(strings.TrimSpace(s), "openid") {
				hasOpenID = true
				break
			}
		}
		if !hasOpenID {
			return errors.New("scopes must include \"openid\"")
		}
	}
	for _, d := range req.AllowedDomains {
		if strings.ContainsAny(d, "@/ ") {
			return errors.New("allowed_domains entries must be bare hostnames (e.g. \"acme.com\")")
		}
	}
	return nil
}
