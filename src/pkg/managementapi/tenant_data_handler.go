package managementapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// HandleTenantDataIngestion handles POST /api/v1/tenant/{tenant_id}/data
// Authenticated via TenantAPIKeyMiddleware (API key, not session).
func (h *Handler) HandleTenantDataIngestion(w http.ResponseWriter, r *http.Request) {
	requestingTenant := GetRequestingTenantFromContext(r.Context())
	if requestingTenant == nil {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "authentication required", nil)
		return
	}

	targetTenantID := r.PathValue("tenant_id")
	if targetTenantID == "" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "tenant_id is required", nil)
		return
	}

	// Look up an active data connection between requester and target
	conn, err := h.repo.GetActiveDataConnection(r.Context(), requestingTenant.ID, targetTenantID)
	if err != nil {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "no active data connection with this tenant", nil)
		return
	}

	// Check permission type allows sending
	if conn.PermissionType != "send" && conn.PermissionType != "both" {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "your connection does not have send permission", nil)
		return
	}

	// Rate limiting
	if h.rateLimiter != nil && !h.rateLimiter.Allow(conn.ID, conn.RateLimitPerHour) {
		_ = writeError(w, http.StatusTooManyRequests, "RateLimited", "rate limit exceeded", nil)
		return
	}

	// Read request body (limit to 10MB)
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "failed to read request body", nil)
		return
	}

	if len(body) == 0 {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "request body is empty", nil)
		return
	}

	// Apply field filtering
	filteredData := filterFields(json.RawMessage(body), conn.AllowedFields, conn.DeniedFields)

	// Publish to NATS if publisher is available
	if h.publisher != nil {
		subject := "tenant." + conn.ID + ".data"
		_ = h.publisher.PublishRaw(r.Context(), subject, filteredData)
	}

	// Extract client IP
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.Split(fwd, ",")[0]
	}
	// Strip port from IP
	if idx := strings.LastIndex(ip, ":"); idx > 0 {
		// Only strip if it's not an IPv6 address without brackets
		if strings.Contains(ip, ".") || strings.HasPrefix(ip, "[") {
			ip = ip[:idx]
		}
	}
	ip = strings.Trim(ip, "[]")

	// Extract accessed field names for audit
	var fieldNames []string
	var parsed map[string]json.RawMessage
	if json.Unmarshal(filteredData, &parsed) == nil {
		for k := range parsed {
			fieldNames = append(fieldNames, k)
		}
	}

	// Log access asynchronously. Detach from the request context: once this
	// handler returns, net/http cancels r.Context(), which would race the
	// insert and frequently abort it with "context canceled" — silently losing
	// the cross-tenant data-access audit record. WithoutCancel keeps the
	// values (e.g. tracing) but drops the cancellation.
	auditCtx := context.WithoutCancel(r.Context())
	go func() {
		_ = h.repo.CreateDataAccessLog(auditCtx, &DataAccessLogEntry{
			ConnectionID:      conn.ID,
			RequesterTenantID: requestingTenant.ID,
			TargetTenantID:    targetTenantID,
			FieldsAccessed:    fieldNames,
			BytesReceived:     len(body),
			StatusCode:        http.StatusAccepted,
			IPAddress:         ip,
		})
	}()

	_ = writeJSON(w, http.StatusAccepted, MessageResponse{Success: true, Message: "data accepted"})
}
