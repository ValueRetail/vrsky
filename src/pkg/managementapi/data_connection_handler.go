package managementapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// CreateConnectionRequest handles POST /api/v1/tenants/{tenant_id}/connection-requests
func (h *Handler) CreateConnectionRequest(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	var payload CreateConnectionRequestPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "invalid request body", nil)
		return
	}

	// If API key provided, resolve to tenant ID
	if payload.TargetAPIKey != "" && payload.TargetTenantID == "" {
		keyHash := auth.HashToken(payload.TargetAPIKey)
		target, err := h.repo.GetTenantByAPIKeyHash(r.Context(), keyHash)
		if err != nil || target == nil {
			_ = writeError(w, http.StatusNotFound, "NotFound", "no tenant found for this API key", nil)
			return
		}
		payload.TargetTenantID = target.ID
	}

	if payload.TargetTenantID == "" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "target_tenant_id or target_api_key is required", nil)
		return
	}
	if payload.TargetTenantID == tenant.ID {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "cannot request connection to yourself", nil)
		return
	}
	if payload.PermissionType != "send" && payload.PermissionType != "receive" && payload.PermissionType != "both" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "permission_type must be send, receive, or both", nil)
		return
	}

	// Verify target tenant exists and is active
	target, err := h.repo.GetTenantByID(r.Context(), payload.TargetTenantID)
	if err != nil || target == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "target tenant not found", nil)
		return
	}
	if target.Status != "active" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "target tenant is not active", nil)
		return
	}

	req := &DataConnectionRequest{
		RequesterTenantID: tenant.ID,
		TargetTenantID:    payload.TargetTenantID,
		PermissionType:    payload.PermissionType,
		Status:            "pending",
		Message:           payload.Message,
	}

	if err := h.repo.CreateConnectionRequest(r.Context(), req); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to create connection request", nil)
		return
	}

	_ = writeJSON(w, http.StatusCreated, req)
}

// ListIncomingConnectionRequests handles GET /api/v1/tenants/{tenant_id}/connection-requests/incoming
func (h *Handler) ListIncomingConnectionRequests(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	requests, err := h.repo.ListIncomingConnectionRequests(r.Context(), tenant.ID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to list connection requests", nil)
		return
	}
	if requests == nil {
		requests = []*DataConnectionRequest{}
	}

	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"requests": requests})
}

// ListOutgoingConnectionRequests handles GET /api/v1/tenants/{tenant_id}/connection-requests/outgoing
func (h *Handler) ListOutgoingConnectionRequests(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	requests, err := h.repo.ListOutgoingConnectionRequests(r.Context(), tenant.ID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to list connection requests", nil)
		return
	}
	if requests == nil {
		requests = []*DataConnectionRequest{}
	}

	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"requests": requests})
}

// ApproveConnectionRequest handles POST /api/v1/tenants/{tenant_id}/connection-requests/{request_id}/approve
func (h *Handler) ApproveConnectionRequest(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	requestID := r.PathValue("request_id")
	if requestID == "" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "request_id is required", nil)
		return
	}

	// Verify request exists and targets this tenant
	connReq, err := h.repo.GetConnectionRequest(r.Context(), requestID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "connection request not found", nil)
		return
	}
	if connReq.TargetTenantID != tenant.ID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "this request is not addressed to your tenant", nil)
		return
	}
	if connReq.Status != "pending" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "request is not pending", nil)
		return
	}

	var payload ApproveConnectionRequestPayload
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "invalid request body", nil)
			return
		}
	}

	// Merge auto-denied unsafe patterns into denied fields
	denied := mergeUnsafeDeniedFields(payload.DeniedFields)

	// The approver may only share its OWN connections. Validate each supplied
	// id belongs to this tenant before storing it: GetConnection is not
	// tenant-scoped, and these ids are later resolved (name + payload) for the
	// requester, so an unchecked id from a third tenant would be disclosed.
	for _, scID := range payload.SharedConnectionIDs {
		shared, gerr := h.repo.GetConnection(r.Context(), scID)
		if gerr != nil || shared == nil || shared.TenantID != tenant.ID {
			_ = writeError(w, http.StatusBadRequest, "BadRequest", "shared connection ids must belong to your tenant", nil)
			return
		}
	}

	conn, err := h.repo.ApproveConnectionRequest(r.Context(), requestID, payload.AllowedFields, denied, payload.SharedConnectionIDs)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to approve connection request", nil)
		return
	}

	_ = writeJSON(w, http.StatusOK, conn)
}

// DenyConnectionRequest handles POST /api/v1/tenants/{tenant_id}/connection-requests/{request_id}/deny
func (h *Handler) DenyConnectionRequest(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	requestID := r.PathValue("request_id")
	if requestID == "" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "request_id is required", nil)
		return
	}

	// Verify request targets this tenant
	connReq, err := h.repo.GetConnectionRequest(r.Context(), requestID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "connection request not found", nil)
		return
	}
	if connReq.TargetTenantID != tenant.ID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "this request is not addressed to your tenant", nil)
		return
	}

	if err := h.repo.DenyConnectionRequest(r.Context(), requestID); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to deny connection request", nil)
		return
	}

	_ = writeJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "connection request denied"})
}

// ListDataConnections handles GET /api/v1/tenants/{tenant_id}/data-connections
func (h *Handler) ListDataConnections(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	connections, err := h.repo.ListDataConnections(r.Context(), tenant.ID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to list data connections", nil)
		return
	}
	if connections == nil {
		connections = []*TenantDataConnection{}
	}

	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"connections": connections})
}

// GetDataConnection handles GET /api/v1/tenants/{tenant_id}/data-connections/{connection_id}
func (h *Handler) GetDataConnection(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	connectionID := r.PathValue("connection_id")
	if connectionID == "" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "connection_id is required", nil)
		return
	}

	conn, err := h.repo.GetDataConnectionByID(r.Context(), connectionID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "data connection not found", nil)
		return
	}

	// Verify tenant is part of this connection
	if conn.RequesterTenantID != tenant.ID && conn.TargetTenantID != tenant.ID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	_ = writeJSON(w, http.StatusOK, conn)
}

// RevokeDataConnection handles POST /api/v1/tenants/{tenant_id}/data-connections/{connection_id}/revoke
func (h *Handler) RevokeDataConnection(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	connectionID := r.PathValue("connection_id")
	if connectionID == "" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "connection_id is required", nil)
		return
	}

	conn, err := h.repo.GetDataConnectionByID(r.Context(), connectionID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "data connection not found", nil)
		return
	}

	// Only involved tenants can revoke
	if conn.RequesterTenantID != tenant.ID && conn.TargetTenantID != tenant.ID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	if err := h.repo.RevokeDataConnection(r.Context(), connectionID); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to revoke data connection", nil)
		return
	}

	// Auto-pause pipeline flows using this data connection
	// The target tenant's flows are the ones consuming data
	_, _ = h.repo.PauseConnectionsByDataConnection(r.Context(), conn.TargetTenantID, connectionID)

	// Remove rate limiter entry
	if h.rateLimiter != nil {
		h.rateLimiter.Remove(connectionID)
	}

	_ = writeJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "data connection revoked"})
}

// GetSharedConnections handles GET /api/v1/tenants/{tenant_id}/data-connections/{connection_id}/shared-connections
// Returns the pipeline connections that the target tenant has shared with the requester.
func (h *Handler) GetSharedConnections(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	connectionID := r.PathValue("connection_id")
	if connectionID == "" {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "connection_id is required", nil)
		return
	}

	// Get the data connection
	dataConn, err := h.repo.GetDataConnectionByID(r.Context(), connectionID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "data connection not found", nil)
		return
	}

	// Verify tenant is part of this connection
	if dataConn.RequesterTenantID != tenant.ID && dataConn.TargetTenantID != tenant.ID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	// Return the shared connection IDs along with their names
	type SharedConnection struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	var result []SharedConnection
	for _, connID := range dataConn.SharedConnectionIDs {
		conn, err := h.repo.GetConnection(r.Context(), connID)
		if err != nil {
			continue
		}
		result = append(result, SharedConnection{ID: conn.ID, Name: conn.Name})
	}
	if result == nil {
		result = []SharedConnection{}
	}

	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"shared_connections": result})
}

// GetDataAccessLog handles GET /api/v1/tenants/{tenant_id}/data-access-log
func (h *Handler) GetDataAccessLog(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	limit := 20
	if l := r.URL.Query().Get("page_size"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	filters := &ListFilters{
		Limit:  limit,
		Offset: (page - 1) * limit,
	}

	entries, total, err := h.repo.ListDataAccessLog(r.Context(), tenant.ID, filters)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "ServerError", "failed to list audit log", nil)
		return
	}
	if entries == nil {
		entries = []*DataAccessLogEntry{}
	}

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	_ = writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"page_info": map[string]interface{}{
			"page":        page,
			"page_size":   limit,
			"total":       total,
			"total_pages": totalPages,
		},
	})
}

// mergeUnsafeDeniedFields appends the standard unsafe field patterns to the explicit denied list.
func mergeUnsafeDeniedFields(explicit []string) []string {
	seen := make(map[string]bool, len(explicit))
	for _, f := range explicit {
		seen[toLower(f)] = true
	}
	result := make([]string, len(explicit))
	copy(result, explicit)
	for _, p := range unsafeFieldPatterns {
		if !seen[p] {
			result = append(result, p)
			seen[p] = true
		}
	}
	return result
}
