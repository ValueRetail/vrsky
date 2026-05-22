package managementapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Secrets handlers (Phase 1A — issue #66).
//
// Plaintext values move across the API boundary in POST/PUT request bodies
// only. Responses always strip the value. The Management API encrypts via
// pkg/crypto and persists ciphertext through PostgresRepository.

// CreateSecretRequest is the body for POST /api/v1/secrets.
type CreateSecretRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// UpdateSecretRequest is the body for PUT /api/v1/secrets/{id}.
// Both fields are optional; missing fields are left unchanged.
type UpdateSecretRequest struct {
	Name  *string `json:"name,omitempty"`
	Value *string `json:"value,omitempty"`
}

func (h *Handler) handleCreateSecret(w http.ResponseWriter, r *http.Request, tenantID string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1*1024*1024)
	var req CreateSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
		} else {
			_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		}
		return
	}
	if req.Name == "" || req.Value == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "name and value are required", nil)
		return
	}
	keyHex, err := crypto.Key()
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ConfigError", "encryption key not configured", nil)
		return
	}
	ct, err := crypto.Encrypt(req.Value, keyHex)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "EncryptError", "failed to encrypt secret", nil)
		return
	}
	s, err := h.repo.CreateSecret(r.Context(), tenantID, req.Name, ct)
	if err != nil {
		// Treat unique-violation as 409.
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "secrets_tenant_id_name_key") {
			_ = writeError(w, http.StatusConflict, "Conflict", "a secret with this name already exists", nil)
			return
		}
		log.Printf("secrets: CreateSecret failed tenant=%s name=%s: %v", tenantID, req.Name, err)
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	log.Printf("secret.create tenant=%s secret=%s", tenantID, s.ID)
	w.Header().Set("Location", "/api/v1/secrets/"+s.ID)
	_ = writeJSON(w, http.StatusCreated, SuccessResponse{Data: s})
}

func (h *Handler) handleListSecrets(w http.ResponseWriter, r *http.Request, tenantID string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	secrets, err := h.repo.ListSecrets(r.Context(), tenantID, limit, offset)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to list secrets", nil)
		return
	}
	if secrets == nil {
		secrets = []*Secret{}
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: secrets})
}

func (h *Handler) handleGetSecret(w http.ResponseWriter, r *http.Request, tenantID, id string) {
	s, err := h.repo.GetSecret(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "secret not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get secret", nil)
		return
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: s})
}

func (h *Handler) handleUpdateSecret(w http.ResponseWriter, r *http.Request, tenantID, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1*1024*1024)
	var req UpdateSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	var newName, newCipher string
	if req.Name != nil {
		newName = *req.Name
	}
	if req.Value != nil && *req.Value != "" {
		keyHex, err := crypto.Key()
		if err != nil {
			_ = writeError(w, http.StatusInternalServerError, "ConfigError", "encryption key not configured", nil)
			return
		}
		ct, err := crypto.Encrypt(*req.Value, keyHex)
		if err != nil {
			_ = writeError(w, http.StatusInternalServerError, "EncryptError", "failed to encrypt secret", nil)
			return
		}
		newCipher = ct
	}
	s, err := h.repo.UpdateSecret(r.Context(), tenantID, id, newName, newCipher)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "secret not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update secret", nil)
		return
	}
	log.Printf("secret.update tenant=%s secret=%s rotated=%v", tenantID, s.ID, newCipher != "")
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: s})
}

func (h *Handler) handleRotateSecret(w http.ResponseWriter, r *http.Request, tenantID, id string) {
	// Re-wraps the current ciphertext with the active master key. The plaintext
	// is decrypted in memory only.
	keyHex, err := crypto.Key()
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ConfigError", "encryption key not configured", nil)
		return
	}
	ct, err := h.repo.GetSecretCiphertext(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "secret not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to read secret", nil)
		return
	}
	plain, err := crypto.Decrypt(ct, keyHex)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DecryptError", "failed to decrypt for rotation", nil)
		return
	}
	newCT, err := crypto.Encrypt(plain, keyHex)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "EncryptError", "failed to re-encrypt", nil)
		return
	}
	s, err := h.repo.UpdateSecret(r.Context(), tenantID, id, "", newCT)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to persist rotated secret", nil)
		return
	}
	log.Printf("secret.rotate tenant=%s secret=%s", tenantID, s.ID)
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: s})
}

func (h *Handler) handleDeleteSecret(w http.ResponseWriter, r *http.Request, tenantID, id string) {
	refs, err := h.repo.DeleteSecret(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "secret not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to delete secret", nil)
		return
	}
	if len(refs) > 0 {
		_ = writeError(w, http.StatusConflict, "InUse", "secret is referenced by connections", map[string]interface{}{
			"connection_ids": refs,
		})
		return
	}
	log.Printf("secret.delete tenant=%s secret=%s", tenantID, id)
	w.WriteHeader(http.StatusNoContent)
}

// SecretsCollection routes POST/GET /api/v1/secrets.
func (h *Handler) SecretsCollection(w http.ResponseWriter, r *http.Request) {
	tenantID, err := GetTenantIDFromContext(r.Context())
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.handleCreateSecret(w, r, tenantID)
	case http.MethodGet:
		h.handleListSecrets(w, r, tenantID)
	default:
		_ = writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "use GET or POST", nil)
	}
}

// SecretsItem routes GET/PUT/DELETE /api/v1/secrets/{id} and the trailing
// /rotate sub-path.
func (h *Handler) SecretsItem(w http.ResponseWriter, r *http.Request) {
	tenantID, err := GetTenantIDFromContext(r.Context())
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/secrets/")
	parts := strings.Split(path, "/")
	id := parts[0]
	if id == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "secret id is required", nil)
		return
	}
	if len(parts) == 2 && parts[1] == "rotate" {
		if r.Method != http.MethodPost {
			_ = writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "use POST", nil)
			return
		}
		h.handleRotateSecret(w, r, tenantID, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handleGetSecret(w, r, tenantID, id)
	case http.MethodPut:
		h.handleUpdateSecret(w, r, tenantID, id)
	case http.MethodDelete:
		h.handleDeleteSecret(w, r, tenantID, id)
	default:
		_ = writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "use GET, PUT or DELETE", nil)
	}
}
