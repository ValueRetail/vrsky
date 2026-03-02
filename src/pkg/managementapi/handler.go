package managementapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler implements REST API handlers for connection management
type Handler struct {
	repo              Repository
	validator         *Validator
	publisher         *NATSPublisher
	clientRegistry    *ClientRegistry
	metricsCache      *MetricsCache
	generatorRegistry *TestGeneratorRegistry
}

// NewHandler creates a new handler
func NewHandler(repo Repository, validator *Validator) *Handler {
	return &Handler{
		repo:              repo,
		validator:         validator,
		publisher:         nil, // Will be set via SetPublisher if needed
		clientRegistry:    nil, // Will be set via InitializeWebSocketSupport if needed
		metricsCache:      nil, // Will be set via InitializeWebSocketSupport if needed
		generatorRegistry: NewTestGeneratorRegistry(),
	}
}

// SetPublisher sets the NATS publisher for command publishing
func (h *Handler) SetPublisher(publisher *NATSPublisher) {
	h.publisher = publisher
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string                 `json:"error"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
	Status  int                    `json:"status"`
}

// SuccessResponse represents a generic success response
type SuccessResponse struct {
	Data interface{} `json:"data"`
}

// ListResponse represents a paginated list response
type ListResponse struct {
	Data   []*Connection `json:"data"`
	Total  int64         `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// Helper to write JSON response
func writeJSON(w http.ResponseWriter, status int, data interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(data)
}

// Helper to write error response
func writeError(w http.ResponseWriter, status int, errType, message string, details map[string]interface{}) error {
	return writeJSON(w, status, ErrorResponse{
		Error:   errType,
		Message: message,
		Details: details,
		Status:  status,
	})
}

// CreateConnection handles POST /api/v1/connections
func (h *Handler) CreateConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Limit request body size (10MB)
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

	// Parse request body
	var req CreateConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err == io.EOF {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
		} else {
			_ = writeError(w, http.StatusBadRequest, "InvalidJSON", fmt.Sprintf("failed to parse request: %v", err), nil)
		}
		return
	}

	// Create connection
	conn := NewConnection(tenantID, req)

	// Validate configuration (fail fast)
	if err := h.validator.ValidateConnection(conn); err != nil {
		if cfgErr, ok := err.(*ConfigError); ok {
			writeError(w, http.StatusBadRequest, "ValidationError", cfgErr.Error(), map[string]interface{}{
				"field":     cfgErr.Field,
				"component": cfgErr.Component,
			})
		} else {
			writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
		}
		return
	}

	// Save to database
	if err := h.repo.CreateConnection(ctx, conn); err != nil {
		if conflictErr, ok := err.(*ConflictError); ok {
			writeError(w, http.StatusConflict, "Conflict", conflictErr.Error(), nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to create connection", nil)
		}
		return
	}

	// Create connection created event
	eventData, _ := json.Marshal(map[string]string{
		"status":      conn.Status,
		"description": conn.Description,
	})
	event := NewConnectionEvent(conn.ID, conn.TenantID, "created", eventData)
	if err := h.repo.CreateConnectionEvent(ctx, event); err != nil {
		// Log error but don't fail the request - connection is already created
	}

	// Return created response
	w.Header().Set("Location", fmt.Sprintf("/api/v1/connections/%s", conn.ID))
	writeJSON(w, http.StatusCreated, SuccessResponse{Data: conn})
}

// GetConnection handles GET /api/v1/connections/{id}
func (h *Handler) GetConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/connections/")

	// Get connection
	conn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	writeJSON(w, http.StatusOK, SuccessResponse{Data: conn})
}

// ListConnections handles GET /api/v1/connections
func (h *Handler) ListConnections(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Parse query parameters
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	filters := &ListFilters{
		Status: r.URL.Query().Get("status"),
		Search: r.URL.Query().Get("search"),
		Limit:  limit,
		Offset: offset,
	}

	// Get connections
	connections, total, err := h.repo.ListConnections(ctx, tenantID, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to list connections", nil)
		return
	}

	if connections == nil {
		connections = []*Connection{} // Return empty array instead of null
	}

	writeJSON(w, http.StatusOK, ListResponse{
		Data:   connections,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// UpdateConnection handles PUT /api/v1/connections/{id}
func (h *Handler) UpdateConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/connections/")

	// Get existing connection
	existingConn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if existingConn.TenantID != tenantID {
		writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	// Prevent updating running connections
	if existingConn.Status == "running" {
		writeError(w, http.StatusBadRequest, "InvalidState", "cannot update a running connection, please stop it first", nil)
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

	// Parse request body
	var updateReq UpdateConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&updateReq); err != nil {
		writeError(w, http.StatusBadRequest, "InvalidJSON", fmt.Sprintf("failed to parse request: %v", err), nil)
		return
	}

	// Apply updates
	if updateReq.Name != nil && strings.TrimSpace(*updateReq.Name) != "" {
		existingConn.Name = *updateReq.Name
	}

	if updateReq.Description != nil {
		existingConn.Description = *updateReq.Description
	}

	if updateReq.SourceConfig != nil {
		existingConn.SourceConfig = *updateReq.SourceConfig
	}

	if updateReq.ConverterConfig != nil {
		existingConn.ConverterConfig = *updateReq.ConverterConfig
	}

	if updateReq.FilterConfig != nil {
		existingConn.FilterConfig = *updateReq.FilterConfig
	}

	if updateReq.DestinationConfig != nil {
		existingConn.DestinationConfig = *updateReq.DestinationConfig
	}

	// Validate updated configuration
	if err := h.validator.ValidateConnection(existingConn); err != nil {
		if cfgErr, ok := err.(*ConfigError); ok {
			writeError(w, http.StatusBadRequest, "ValidationError", cfgErr.Error(), map[string]interface{}{
				"field":     cfgErr.Field,
				"component": cfgErr.Component,
			})
		} else {
			writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
		}
		return
	}

	// Update in database
	if err := h.repo.UpdateConnection(ctx, existingConn); err != nil {
		if _, ok := err.(*NotFoundError); ok {
			writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update connection", nil)
		}
		return
	}

	writeJSON(w, http.StatusOK, SuccessResponse{Data: existingConn})
}

// DeleteConnection handles DELETE /api/v1/connections/{id}
func (h *Handler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/connections/")

	// Get connection to verify ownership and status
	conn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	// Prevent deleting running connections
	if conn.Status == "running" {
		writeError(w, http.StatusBadRequest, "InvalidState", "cannot delete a running connection, please stop it first", nil)
		return
	}

	// Delete connection (cascade delete events)
	if err := h.repo.DeleteConnection(ctx, id); err != nil {
		if _, ok := err.(*NotFoundError); ok {
			writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to delete connection", nil)
		}
		return
	}

	// Create connection deleted event
	eventData, _ := json.Marshal(map[string]string{
		"deletedAt": time.Now().UTC().String(),
	})
	event := NewConnectionEvent(id, tenantID, "deleted", eventData)
	if err := h.repo.CreateConnectionEvent(ctx, event); err != nil {
		// Log error but don't fail the request - connection is already deleted
	}

	w.WriteHeader(http.StatusNoContent)
}

// StartConnection handles POST /api/v1/connections/{id}/start
func (h *Handler) StartConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID from URL path
	connID := r.PathValue("id")
	if strings.TrimSpace(connID) == "" {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Get the connection
	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to retrieve connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Check if connection is already running
	if conn.Status == "running" {
		writeError(w, http.StatusBadRequest, "InvalidState", "connection is already running", nil)
		return
	}

	// Update connection status to Running
	conn.Status = "running"
	conn.StartedAt = pointerTo(time.Now().UTC())

	if err := h.repo.UpdateConnection(ctx, conn); err != nil {
		writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update connection status", nil)
		return
	}

	// Create connection started event
	eventData, _ := json.Marshal(map[string]interface{}{
		"status":    conn.Status,
		"startedAt": conn.StartedAt,
	})
	event := NewConnectionEvent(connID, tenantID, "started", eventData)
	if err := h.repo.CreateConnectionEvent(ctx, event); err != nil {
		// Log error but don't fail the request - status is already updated
	}

	// Publish start command to NATS if publisher is available
	if h.publisher != nil {
		if err := h.publisher.PublishConnectionStart(ctx, connID, tenantID); err != nil {
			// Log error but don't fail the request - status is already updated
			// The connection is marked as running in the database
		}
	}

	writeJSON(w, http.StatusOK, SuccessResponse{
		Data: conn,
	})
}

// StopConnection handles POST /api/v1/connections/{id}/stop
func (h *Handler) StopConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID from URL path
	connID := r.PathValue("id")
	if strings.TrimSpace(connID) == "" {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Get the connection
	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to retrieve connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Check if connection is already stopped
	if conn.Status == "stopped" {
		writeError(w, http.StatusBadRequest, "InvalidState", "connection is already stopped", nil)
		return
	}

	// Update connection status to Stopped
	conn.Status = "stopped"
	conn.StoppedAt = pointerTo(time.Now().UTC())

	if err := h.repo.UpdateConnection(ctx, conn); err != nil {
		writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update connection status", nil)
		return
	}

	// Create connection stopped event
	eventData, _ := json.Marshal(map[string]interface{}{
		"status":    conn.Status,
		"stoppedAt": conn.StoppedAt,
	})
	event := NewConnectionEvent(connID, tenantID, "stopped", eventData)
	if err := h.repo.CreateConnectionEvent(ctx, event); err != nil {
		// Log error but don't fail the request - status is already updated
	}

	// Publish stop command to NATS if publisher is available
	if h.publisher != nil {
		if err := h.publisher.PublishConnectionStop(ctx, connID, tenantID); err != nil {
			// Log error but don't fail the request - status is already updated
			// The connection is marked as stopped in the database
		}
	}

	writeJSON(w, http.StatusOK, SuccessResponse{
		Data: conn,
	})
}

// pointerTo is a helper to create a pointer to a value
func pointerTo[T any](v T) *T {
	return &v
}

// RegisterRoutes registers all REST handlers with the mux
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// CRUD operations
	mux.HandleFunc("POST /api/v1/connections", h.CreateConnection)
	mux.HandleFunc("GET /api/v1/connections", h.ListConnections)
	mux.HandleFunc("GET /api/v1/connections/{id}", h.GetConnection)
	mux.HandleFunc("PUT /api/v1/connections/{id}", h.UpdateConnection)
	mux.HandleFunc("DELETE /api/v1/connections/{id}", h.DeleteConnection)

	// Control operations
	mux.HandleFunc("POST /api/v1/connections/{id}/start", h.StartConnection)
	mux.HandleFunc("POST /api/v1/connections/{id}/stop", h.StopConnection)

	// Metrics streaming via Server-Sent Events
	mux.HandleFunc("GET /api/v1/connections/{id}/metrics/stream", h.HandleMetricsSSE)
	mux.HandleFunc("GET /api/v1/connections/{id}/metrics/ws", h.HandleMetricsWebSocket)

	// Test data generation
	mux.HandleFunc("POST /api/v1/connections/{id}/test-message", h.SendSingleTestMessage)
	mux.HandleFunc("POST /api/v1/connections/{id}/auto-generator/start", h.StartAutoGenerator)
	mux.HandleFunc("POST /api/v1/connections/{id}/auto-generator/stop", h.StopAutoGenerator)
	mux.HandleFunc("GET /api/v1/connections/{id}/auto-generator/status", h.GetAutoGeneratorStatus)
}
