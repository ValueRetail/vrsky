package managementapi

import (
	"database/sql"
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
	db                *sql.DB // Direct DB access for raw queries (e.g. sample-data)

	// K8s integration for graph-based pipelines (Phase 2)
	orchestratorFactory OrchestratorFactory

	// Tenant NATS provisioning (Phase 2)
	tenantProvisioner *TenantProvisioner
	tenantSSEHub      *TenantSSEHub

	// Data sharing rate limiter (Phase 3)
	rateLimiter *ConnectionRateLimiter
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

// SetDB sets the direct database connection for raw queries
func (h *Handler) SetDB(db *sql.DB) {
	h.db = db
}

// SetPublisher sets the NATS publisher for command publishing
func (h *Handler) SetPublisher(publisher *NATSPublisher) {
	h.publisher = publisher
}

// SetOrchestratorFactory sets the factory for creating pipeline orchestrators.
// This enables K8s deployment for graph-based connections (Phase 2).
func (h *Handler) SetOrchestratorFactory(factory OrchestratorFactory) {
	h.orchestratorFactory = factory
}

// SetTenantProvisioner sets the background provisioner for tenant NATS instances.
func (h *Handler) SetTenantProvisioner(provisioner *TenantProvisioner) {
	h.tenantProvisioner = provisioner
}

// SetTenantSSEHub sets the SSE hub for tenant provisioning status streaming.
func (h *Handler) SetTenantSSEHub(hub *TenantSSEHub) {
	h.tenantSSEHub = hub
}

// SetRateLimiter sets the rate limiter for tenant data ingestion.
func (h *Handler) SetRateLimiter(rl *ConnectionRateLimiter) {
	h.rateLimiter = rl
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

	// Validate configuration
	// If using graph-based model (nodes present), validate DAG topology
	if len(conn.Nodes) > 0 {
		if err := h.validator.ValidateDAG(conn); err != nil {
			if dagErr, ok := err.(*DAGValidationError); ok {
				_ = writeError(w, http.StatusBadRequest, "DAGValidationError", "pipeline validation failed", map[string]interface{}{
					"errors": dagErr.Errors,
				})
			} else {
				_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
			}
			return
		}
	} else {
		// Legacy linear model validation (DEPRECATED)
		if err := h.validator.ValidateConnection(conn); err != nil {
			if cfgErr, ok := err.(*ConfigError); ok {
				_ = writeError(w, http.StatusBadRequest, "ValidationError", cfgErr.Error(), map[string]interface{}{
					"field":     cfgErr.Field,
					"component": cfgErr.Component,
				})
			} else {
				_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
			}
			return
		}
	}

	// Save to database
	if err := h.repo.CreateConnection(ctx, conn); err != nil {
		if conflictErr, ok := err.(*ConflictError); ok {
			_ = writeError(w, http.StatusConflict, "Conflict", conflictErr.Error(), nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to create connection", nil)
		}
		return
	}

	// Create connection created event
	eventData, _ := json.Marshal(map[string]string{
		"status":      conn.Status,
		"description": conn.Description,
	})
	event := NewConnectionEvent(conn.ID, conn.TenantID, "created", eventData)
	_ = h.repo.CreateConnectionEvent(ctx, event)

	// Return created response
	w.Header().Set("Location", fmt.Sprintf("/api/v1/connections/%s", conn.ID))
	_ = writeJSON(w, http.StatusCreated, SuccessResponse{Data: conn})
}

// GetConnection handles GET /api/v1/connections/{id}
func (h *Handler) GetConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/connections/")

	// Get connection
	conn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: conn})
}

// ListConnections handles GET /api/v1/connections
func (h *Handler) ListConnections(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
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
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to list connections", nil)
		return
	}

	if connections == nil {
		connections = []*Connection{} // Return empty array instead of null
	}

	_ = writeJSON(w, http.StatusOK, ListResponse{
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
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/connections/")

	// Get existing connection
	existingConn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if existingConn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	// Prevent updating running connections
	if existingConn.Status == "running" {
		_ = writeError(w, http.StatusBadRequest, "InvalidState", "cannot update a running connection, please stop it first", nil)
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

	// Parse request body
	var updateReq UpdateConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&updateReq); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", fmt.Sprintf("failed to parse request: %v", err), nil)
		return
	}

	// Apply updates
	if updateReq.Name != nil && strings.TrimSpace(*updateReq.Name) != "" {
		existingConn.Name = *updateReq.Name
	}

	if updateReq.Description != nil {
		existingConn.Description = *updateReq.Description
	}

	// Apply graph-based model updates (Phase 1)
	// If Nodes are provided, use the new model; otherwise fall back to legacy
	if len(updateReq.Nodes) > 0 {
		existingConn.Nodes = updateReq.Nodes
		existingConn.Edges = updateReq.Edges
	} else {
		// DEPRECATED: Legacy linear model updates
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
	}

	// Validate updated configuration
	// If using graph-based model (nodes present), validate DAG topology
	if len(existingConn.Nodes) > 0 {
		if err := h.validator.ValidateDAG(existingConn); err != nil {
			if dagErr, ok := err.(*DAGValidationError); ok {
				_ = writeError(w, http.StatusBadRequest, "DAGValidationError", "pipeline validation failed", map[string]interface{}{
					"errors": dagErr.Errors,
				})
			} else {
				_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
			}
			return
		}
	} else {
		// Legacy linear model validation (DEPRECATED)
		if err := h.validator.ValidateConnection(existingConn); err != nil {
			if cfgErr, ok := err.(*ConfigError); ok {
				_ = writeError(w, http.StatusBadRequest, "ValidationError", cfgErr.Error(), map[string]interface{}{
					"field":     cfgErr.Field,
					"component": cfgErr.Component,
				})
			} else {
				_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
			}
			return
		}
	}

	// Update in database
	if err := h.repo.UpdateConnection(ctx, existingConn); err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update connection", nil)
		}
		return
	}

	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: existingConn})
}

// DeleteConnection handles DELETE /api/v1/connections/{id}
func (h *Handler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/connections/")

	// Get connection to verify ownership and status
	conn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	// Auto-stop running connections before deleting
	if conn.Status == "running" && h.publisher != nil {
		_ = h.publisher.PublishConnectionStop(ctx, id, tenantID)
		// Update status in DB
		_ = h.repo.UpdateConnectionStatus(ctx, id, "stopped", nil)
	}

	// Delete connection (cascade delete events)
	if err := h.repo.DeleteConnection(ctx, id); err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to delete connection", nil)
		}
		return
	}

	// Create connection deleted event
	eventData, _ := json.Marshal(map[string]string{
		"deletedAt": time.Now().UTC().String(),
	})
	event := NewConnectionEvent(id, tenantID, "deleted", eventData)
	_ = h.repo.CreateConnectionEvent(ctx, event)

	w.WriteHeader(http.StatusNoContent)
}

// StartConnection handles POST /api/v1/connections/{id}/start
func (h *Handler) StartConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID from URL path
	connID := r.PathValue("id")
	if strings.TrimSpace(connID) == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Get the connection
	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to retrieve connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Check if connection is already running
	if conn.Status == "running" {
		_ = writeError(w, http.StatusBadRequest, "InvalidState", "connection is already running", nil)
		return
	}

	// For graph-based connections (Phase 2): Deploy to Kubernetes via orchestrator
	if len(conn.Nodes) > 0 && h.orchestratorFactory != nil {
		orch := h.orchestratorFactory(conn)
		if err := orch.StartPipeline(ctx, conn); err != nil {
			// Deployment failed - don't update status to running
			_ = writeError(w, http.StatusInternalServerError, "OrchestratorError",
				fmt.Sprintf("failed to deploy pipeline: %v", err), nil)
			return
		}
	}

	// Update connection status to Running
	conn.Status = "running"
	conn.StartedAt = pointerTo(time.Now().UTC())

	if err := h.repo.UpdateConnection(ctx, conn); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update connection status", nil)
		return
	}

	// Create connection started event
	eventData, _ := json.Marshal(map[string]interface{}{
		"status":    conn.Status,
		"startedAt": conn.StartedAt,
	})
	event := NewConnectionEvent(connID, tenantID, "started", eventData)
	_ = h.repo.CreateConnectionEvent(ctx, event)

	// Publish start command to NATS if publisher is available
	if h.publisher != nil {
		_ = h.publisher.PublishConnectionStart(ctx, connID, tenantID)
	}

	_ = writeJSON(w, http.StatusOK, SuccessResponse{
		Data: conn,
	})
}

// StopConnection handles POST /api/v1/connections/{id}/stop
func (h *Handler) StopConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID from URL path
	connID := r.PathValue("id")
	if strings.TrimSpace(connID) == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Get the connection
	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to retrieve connection", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Check if connection is already stopped
	if conn.Status == "stopped" {
		_ = writeError(w, http.StatusBadRequest, "InvalidState", "connection is already stopped", nil)
		return
	}

	// For graph-based connections (Phase 2): Remove K8s deployments via orchestrator
	// Note: Even if K8s cleanup fails, we still update the connection status to stopped.
	// Failed K8s resources can be cleaned up manually or by a garbage collector.
	var orchestratorErr error
	if len(conn.Nodes) > 0 && h.orchestratorFactory != nil {
		orch := h.orchestratorFactory(conn)
		orchestratorErr = orch.StopPipeline(ctx, conn)
	}

	// Update connection status to Stopped
	conn.Status = "stopped"
	conn.StoppedAt = pointerTo(time.Now().UTC())

	if err := h.repo.UpdateConnection(ctx, conn); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update connection status", nil)
		return
	}

	// Create connection stopped event
	eventPayload := map[string]interface{}{
		"status":    conn.Status,
		"stoppedAt": conn.StoppedAt,
	}
	if orchestratorErr != nil {
		eventPayload["orchestratorError"] = orchestratorErr.Error()
	}
	eventData, _ := json.Marshal(eventPayload)
	event := NewConnectionEvent(connID, tenantID, "stopped", eventData)
	_ = h.repo.CreateConnectionEvent(ctx, event)

	// Publish stop command to NATS if publisher is available
	if h.publisher != nil {
		_ = h.publisher.PublishConnectionStop(ctx, connID, tenantID)
	}

	_ = writeJSON(w, http.StatusOK, SuccessResponse{
		Data: conn,
	})
}

// GetSampleData returns the last received payload for a connection (used by filter data structure preview)
func (h *Handler) GetSampleData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID from path: /api/v1/connections/{id}/sample-data
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/connections/")
	id := strings.TrimSuffix(path, "/sample-data")

	// Try exact connection first
	var lastPayload []byte
	err = h.db.QueryRowContext(ctx,
		"SELECT last_payload FROM connections WHERE id = $1 AND tenant_id = $2 AND last_payload IS NOT NULL",
		id, tenantID).Scan(&lastPayload)

	// Fallback: find any connection in the same tenant with payload
	if err != nil || len(lastPayload) == 0 {
		err = h.db.QueryRowContext(ctx, `
			SELECT last_payload FROM connections
			WHERE tenant_id = $1 AND last_payload IS NOT NULL
			ORDER BY updated_at DESC LIMIT 1`,
			tenantID).Scan(&lastPayload)
	}

	if err != nil || len(lastPayload) == 0 {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": false, "error": "No data received yet. Deploy the pipeline and send data first.",
		})
		return
	}

	// last_payload is a serialized envelope — extract the payload field
	// Payload is []byte which json.Marshal encodes as base64
	var env struct {
		Payload []byte `json:"payload"`
	}
	if err := json.Unmarshal(lastPayload, &env); err != nil {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": false, "error": "Failed to parse stored payload",
		})
		return
	}

	// Try to parse the decoded payload as JSON
	var parsed interface{}
	if err := json.Unmarshal(env.Payload, &parsed); err != nil {
		// Payload isn't valid JSON — check source tenant connections as fallback
		// (tenant consumer may have corrupted data while source has original JSON)
		var sourceTenantID string
		_ = h.db.QueryRowContext(ctx, `
			SELECT n->'config'->'tenant'->>'source_tenant_id' FROM connections c,
			jsonb_array_elements(c.nodes) n
			WHERE c.id = $1 AND n->>'type' = 'consumer'
			AND n->'config'->'tenant'->>'source_tenant_id' IS NOT NULL
			LIMIT 1`, id).Scan(&sourceTenantID)

		if sourceTenantID != "" {
			var sourcePayload []byte
			_ = h.db.QueryRowContext(ctx, `
				SELECT last_payload FROM connections
				WHERE tenant_id = $1 AND last_payload IS NOT NULL
				ORDER BY updated_at DESC LIMIT 1`,
				sourceTenantID).Scan(&sourcePayload)
			if sourcePayload != nil {
				var srcEnv struct{ Payload []byte `json:"payload"` }
				if json.Unmarshal(sourcePayload, &srcEnv) == nil {
					if json.Unmarshal(srcEnv.Payload, &parsed) == nil {
						_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": parsed})
						return
					}
				}
			}
		}

		_ = writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "data": string(env.Payload),
		})
		return
	}

	_ = writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "data": parsed,
	})
}

// GetSourceSampleData returns the last_payload of a SOURCE tenant's connection,
// for previewing tenant-consumer data before deploying the local pipeline. The
// caller must have an approved data connection with the source tenant.
//
//	GET /api/v1/sample-data/source?source_tenant_id=X&source_connection_id=Y
func (h *Handler) GetSourceSampleData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	sourceTenantID := r.URL.Query().Get("source_tenant_id")
	sourceConnID := r.URL.Query().Get("source_connection_id")
	if sourceTenantID == "" {
		_ = writeError(w, http.StatusBadRequest, "MissingParam", "source_tenant_id required", nil)
		return
	}

	// Verify the caller has an approved data connection with this source tenant.
	// In tenant_data_connections, the requester is the consumer of the data and
	// the target is the source tenant whose data is being shared.
	var approved bool
	err = h.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM tenant_data_connections
			WHERE requester_tenant_id = $1 AND target_tenant_id = $2 AND status = 'active'
		)`, tenantID, sourceTenantID).Scan(&approved)
	if err != nil || !approved {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "no active data connection with that source tenant", nil)
		return
	}

	// Fetch last_payload from the source. Prefer the specific connection if
	// provided, otherwise pick the most recent one with payload.
	var lastPayload []byte
	if sourceConnID != "" {
		err = h.db.QueryRowContext(ctx,
			"SELECT last_payload FROM connections WHERE id = $1 AND tenant_id = $2 AND last_payload IS NOT NULL",
			sourceConnID, sourceTenantID).Scan(&lastPayload)
	}
	if err != nil || len(lastPayload) == 0 {
		err = h.db.QueryRowContext(ctx, `
			SELECT last_payload FROM connections
			WHERE tenant_id = $1 AND last_payload IS NOT NULL
			ORDER BY updated_at DESC LIMIT 1`,
			sourceTenantID).Scan(&lastPayload)
	}
	if err != nil || len(lastPayload) == 0 {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": false, "error": "No sample data available from source tenant yet.",
		})
		return
	}

	// last_payload is a serialized envelope — extract the payload field
	var env struct {
		Payload []byte `json:"payload"`
	}
	if err := json.Unmarshal(lastPayload, &env); err != nil {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": false, "error": "Failed to parse stored payload",
		})
		return
	}

	var parsed interface{}
	if err := json.Unmarshal(env.Payload, &parsed); err != nil {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "data": string(env.Payload),
		})
		return
	}
	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": parsed})
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

	// Sample data for filter preview
	mux.HandleFunc("GET /api/v1/connections/{id}/sample-data", h.GetSampleData)
	mux.HandleFunc("GET /api/v1/sample-data/source", h.GetSourceSampleData)

	// Test data generation
	mux.HandleFunc("POST /api/v1/connections/{id}/test-message", h.SendSingleTestMessage)
	mux.HandleFunc("POST /api/v1/connections/{id}/auto-generator/start", h.StartAutoGenerator)
	mux.HandleFunc("POST /api/v1/connections/{id}/auto-generator/stop", h.StopAutoGenerator)
	mux.HandleFunc("GET /api/v1/connections/{id}/auto-generator/status", h.GetAutoGeneratorStatus)

	// API Consumer routes
	h.RegisterAPIConsumerRoutes(mux)

	// Auth routes (these bypass TenantIDMiddleware)
	h.RegisterAuthRoutes(mux)
}

// RegisterAuthRoutes registers authentication routes
// These routes do NOT require X-Tenant-ID header (auth is global in Phase 1)
func (h *Handler) RegisterAuthRoutes(mux *http.ServeMux) {
	// Public auth routes (no authentication required)
	mux.HandleFunc("POST /api/v1/auth/register", h.RegisterUser)
	mux.HandleFunc("POST /api/v1/auth/login", h.LoginUser)
	mux.HandleFunc("GET /api/v1/auth/verify-email", h.VerifyEmail)
	mux.HandleFunc("POST /api/v1/auth/forgot-password", h.ForgotPassword)
	mux.HandleFunc("POST /api/v1/auth/reset-password", h.ResetPassword)

	// Protected auth routes (require valid session)
	sessionMW := SessionAuthMiddleware(h.repo)
	mux.HandleFunc("GET /api/v1/auth/me", sessionMW(http.HandlerFunc(h.GetMe)).ServeHTTP)
	mux.HandleFunc("POST /api/v1/auth/logout", sessionMW(http.HandlerFunc(h.LogoutUser)).ServeHTTP)
	mux.HandleFunc("POST /api/v1/auth/change-password", sessionMW(http.HandlerFunc(h.ChangePassword)).ServeHTTP)
	mux.HandleFunc("DELETE /api/v1/auth/me", sessionMW(http.HandlerFunc(h.DeleteAccount)).ServeHTTP)

	// Tenant routes (require valid session, no X-Tenant-ID header)
	mux.HandleFunc("POST /api/v1/tenants", sessionMW(http.HandlerFunc(h.CreateTenantHandler)).ServeHTTP)
	mux.HandleFunc("GET /api/v1/tenants", sessionMW(http.HandlerFunc(h.ListTenantsHandler)).ServeHTTP)

	// Tenant routes with membership validation
	tenantMW := TenantMemberMiddleware(h.repo)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}", sessionMW(tenantMW(http.HandlerFunc(h.GetTenantHandler))).ServeHTTP)
	mux.HandleFunc("DELETE /api/v1/tenants/{tenant_id}", sessionMW(tenantMW(RequireRole("owner")(http.HandlerFunc(h.DeleteTenantHandler)))).ServeHTTP)

	// Tenant provisioning status stream (Phase 2)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/status/stream", sessionMW(tenantMW(http.HandlerFunc(h.HandleTenantStatusSSE))).ServeHTTP)

	// Tenant API key management (Phase 2)
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/api-key/rotate", sessionMW(tenantMW(RequireRole("owner")(http.HandlerFunc(h.RotateTenantAPIKey)))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/api-key", sessionMW(tenantMW(RequireRole("admin")(http.HandlerFunc(h.GetTenantAPIKey)))).ServeHTTP)

	// ============================================
	// Data Sharing Routes (Phase 3)
	// ============================================

	// Connection requests
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/connection-requests", sessionMW(tenantMW(http.HandlerFunc(h.CreateConnectionRequest))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/connection-requests/incoming", sessionMW(tenantMW(RequireRole("admin")(http.HandlerFunc(h.ListIncomingConnectionRequests)))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/connection-requests/outgoing", sessionMW(tenantMW(http.HandlerFunc(h.ListOutgoingConnectionRequests))).ServeHTTP)
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/connection-requests/{request_id}/approve", sessionMW(tenantMW(RequireRole("owner")(http.HandlerFunc(h.ApproveConnectionRequest)))).ServeHTTP)
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/connection-requests/{request_id}/deny", sessionMW(tenantMW(RequireRole("owner")(http.HandlerFunc(h.DenyConnectionRequest)))).ServeHTTP)

	// Active data connections
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/data-connections", sessionMW(tenantMW(http.HandlerFunc(h.ListDataConnections))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/data-connections/{connection_id}", sessionMW(tenantMW(http.HandlerFunc(h.GetDataConnection))).ServeHTTP)
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/data-connections/{connection_id}/revoke", sessionMW(tenantMW(RequireRole("owner")(http.HandlerFunc(h.RevokeDataConnection)))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/data-connections/{connection_id}/shared-connections", sessionMW(tenantMW(http.HandlerFunc(h.GetSharedConnections))).ServeHTTP)

	// Audit log
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/data-access-log", sessionMW(tenantMW(RequireRole("admin")(http.HandlerFunc(h.GetDataAccessLog)))).ServeHTTP)

	// Tenant data ingestion endpoint (API key auth, not session auth)
	apiKeyMW := TenantAPIKeyMiddleware(h.repo)
	mux.HandleFunc("POST /api/v1/tenant/{tenant_id}/data", apiKeyMW(http.HandlerFunc(h.HandleTenantDataIngestion)).ServeHTTP)
}
