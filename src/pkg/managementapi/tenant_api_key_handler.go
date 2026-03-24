package managementapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// RotateTenantAPIKey handles POST /api/v1/tenants/{tenant_id}/api-key/rotate
// Generates a new API key, stores its hash, and returns the raw key once.
func (h *Handler) RotateTenantAPIKey(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	// Generate raw key: vrsky_{slug}_{32 random hex chars}
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to generate key", nil)
		return
	}
	rawKey := fmt.Sprintf("vrsky_%s_%s", tenant.Slug, hex.EncodeToString(randomBytes))

	// Hash for storage
	keyHash := auth.HashToken(rawKey)

	apiKey, err := h.repo.UpsertTenantAPIKey(r.Context(), tenant.ID, keyHash)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to save API key", nil)
		return
	}

	_ = writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":         apiKey.ID,
		"tenant_id":  apiKey.TenantID,
		"raw_key":    rawKey, // Shown only once
		"created_at": apiKey.CreatedAt,
		"is_active":  apiKey.IsActive,
	})
}

// GetTenantAPIKey handles GET /api/v1/tenants/{tenant_id}/api-key
// Returns key metadata without the raw key.
func (h *Handler) GetTenantAPIKey(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	apiKey, err := h.repo.GetTenantAPIKey(r.Context(), tenant.ID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "no API key found for this workspace", nil)
		return
	}

	_ = writeJSON(w, http.StatusOK, apiKey)
}
