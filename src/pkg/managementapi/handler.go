package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/oauth"
	"github.com/ValueRetail/vrsky/pkg/promquery"
)

// Handler implements REST API handlers for connection management
type Handler struct {
	repo              Repository
	validator         *Validator
	publisher         *NATSPublisher
	clientRegistry    *ClientRegistry
	metricsCache      *MetricsCache
	generatorRegistry *TestGeneratorRegistry
	db                *sql.DB               // Direct DB access for raw queries (e.g. sample-data)
	js                nats.JetStreamContext // JetStream context for DLQ endpoints (#70)
	quotas            *QuotaTracker         // In-process token buckets for per-tenant rate limits (#74)

	// K8s integration for graph-based pipelines (Phase 2)
	orchestratorFactory OrchestratorFactory

	// Tenant NATS provisioning (Phase 2)
	tenantProvisioner *TenantProvisioner
	tenantSSEHub      *TenantSSEHub

	// Data sharing rate limiter (Phase 3)
	rateLimiter *ConnectionRateLimiter

	// OAuth 2.0 framework (Phase 2A — #75). Optional: nil disables the
	// /api/v1/oauth/* routes' state-machine endpoints (start / callback /
	// revoke); provider CRUD still works since it goes through the repo.
	oauthClient    *oauth.Client
	oauthRefresher *OAuthRefresher

	// gatewaySync, when set, regenerates the gateway (Traefik) per-tenant
	// rate-limit config after a plan change so the new limit takes effect at the
	// edge (#90). Wired by cmd/management-api when TRAEFIK_DYNAMIC_DIR is set;
	// nil (the default) makes plan changes a pure DB update.
	gatewaySync func(context.Context) error

	// prom, when set, backs the public status page (#95) with Prometheus `up`
	// probe data. nil (no PROMETHEUS_URL) makes every component report "unknown".
	prom *promquery.Client
}

// SetGatewaySync wires the gateway rate-limit config refresher (#90).
func (h *Handler) SetGatewaySync(fn func(context.Context) error) { h.gatewaySync = fn }

// SetOAuthClient wires the generic OAuth 2.0 client (built on h.repo).
func (h *Handler) SetOAuthClient(c *oauth.Client) { h.oauthClient = c }

// SetOAuthRefresher wires the background refresher so the on-401 retry path
// (PR #3) can Enqueue refresh jobs.
func (h *Handler) SetOAuthRefresher(r *OAuthRefresher) { h.oauthRefresher = r }

// NewHandler creates a new handler
func NewHandler(repo Repository, validator *Validator) *Handler {
	return &Handler{
		repo:              repo,
		validator:         validator,
		publisher:         nil, // Will be set via SetPublisher if needed
		clientRegistry:    nil, // Will be set via InitializeWebSocketSupport if needed
		metricsCache:      nil, // Will be set via InitializeWebSocketSupport if needed
		generatorRegistry: NewTestGeneratorRegistry(),
		quotas:            NewQuotaTracker(),
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

	// Per-tenant quota check (#74): refuse with 429 if adding this
	// integration would put the tenant over their plan limit. Skipped
	// silently if quotas can't be loaded — failing closed here would
	// punish users for a DB hiccup.
	if q, err := h.repo.GetTenantQuotas(ctx, tenantID); err == nil {
		if qerr := h.quotas.CheckIntegrationCount(ctx, h.repo, tenantID, q); qerr != nil {
			w.Header().Set("Retry-After", "0")
			_ = writeError(w, http.StatusTooManyRequests, "QuotaExceeded",
				"integration count quota reached", map[string]interface{}{
					"max_integrations": q.MaxIntegrations,
				})
			return
		}
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

	// Count the deploy for per-tenant usage metering (#92). The usage rollup
	// snapshots increase() of this counter from Prometheus into usage_daily.
	connectionDeploys.WithLabelValues(tenantID).Inc()

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

	// Stop is idempotent: an already-stopped connection succeeds with 200
	// so the UI's "stop → update → start" redeploy flow doesn't log a
	// spurious 4xx on every redeploy.
	if conn.Status == "stopped" {
		_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: conn})
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
		// lint:tenant-ok — resolving source-tenant ID by connection PK; outer handler verified caller tenant.
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
				var srcEnv struct {
					Payload []byte `json:"payload"`
				}
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
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to verify data connection", nil)
		return
	}
	if !approved {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "no active data connection with that source tenant", nil)
		return
	}

	// Fetch last_payload from the source. Prefer the specific connection if
	// provided, otherwise pick the most recent one with payload. A real DB
	// error is surfaced as 500; a missing row (sql.ErrNoRows) falls back to the
	// most-recent lookup and ultimately to an "ok=false / no data" response, so
	// transient outages aren't disguised as "no sample data yet".
	var lastPayload []byte
	if sourceConnID != "" {
		err = h.db.QueryRowContext(ctx,
			"SELECT last_payload FROM connections WHERE id = $1 AND tenant_id = $2 AND last_payload IS NOT NULL",
			sourceConnID, sourceTenantID).Scan(&lastPayload)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to load sample data", nil)
			return
		}
	}
	if len(lastPayload) == 0 {
		err = h.db.QueryRowContext(ctx, `
			SELECT last_payload FROM connections
			WHERE tenant_id = $1 AND last_payload IS NOT NULL
			ORDER BY updated_at DESC LIMIT 1`,
			sourceTenantID).Scan(&lastPayload)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to load sample data", nil)
			return
		}
	}
	if len(lastPayload) == 0 {
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

// RegisterRoutes registers all REST handlers with the mux.
//
// Role guards (Phase 1D / #69):
//
//	create / update / start / stop / secret-write / dlq-retry|discard → editor
//	delete connection / delete secret                                 → admin
//	list / get / metrics / sample-data / dlq-list                     → viewer (membership-only)
//
// Reads still require tenant membership but no role above viewer.
// Mutations go through RequireTenantRoleFromHeader which authenticates
// the caller (session OR API key) and resolves their role in the tenant
// supplied via the X-Tenant-ID header.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	editor := RequireTenantRoleFromHeader(h.repo, "editor")
	adminMW := RequireTenantRoleFromHeader(h.repo, "admin")
	// Read routes gate at viewer (the lowest membership role). Without this,
	// connection GET routes scoped only by the X-Tenant-ID header let any
	// authenticated user read another tenant's connections, sample data,
	// metrics, and DLQ payloads simply by changing the header. viewer ≤ editor,
	// so it does not restrict anyone who could already mutate.
	viewer := RequireTenantRoleFromHeader(h.repo, "viewer")

	// API documentation (#94): the generated OpenAPI spec + Swagger UI. Public
	// (no tenant header — exempted in TenantIDMiddleware).
	mux.HandleFunc("GET /openapi.json", h.ServeOpenAPISpec)
	mux.HandleFunc("GET /docs", h.ServeSwaggerUI)

	// Public status page (#95): HTML + JSON, driven by Prometheus probe data.
	// Public (exempted in TenantIDMiddleware).
	mux.HandleFunc("GET /status", h.ServeStatusPage)
	mux.HandleFunc("GET /status.json", h.ServeStatusJSON)

	// CRUD operations
	mux.Handle("POST /api/v1/connections", editor(http.HandlerFunc(h.CreateConnection)))
	mux.Handle("GET /api/v1/connections", viewer(http.HandlerFunc(h.ListConnections)))
	mux.Handle("GET /api/v1/connections/{id}", viewer(http.HandlerFunc(h.GetConnection)))
	mux.Handle("PUT /api/v1/connections/{id}", editor(http.HandlerFunc(h.UpdateConnection)))
	mux.Handle("DELETE /api/v1/connections/{id}", adminMW(http.HandlerFunc(h.DeleteConnection)))

	// Control operations
	mux.Handle("POST /api/v1/connections/{id}/start", editor(http.HandlerFunc(h.StartConnection)))
	mux.Handle("POST /api/v1/connections/{id}/stop", editor(http.HandlerFunc(h.StopConnection)))

	// Test a draft connector config without persisting it (#82).
	mux.Handle("POST /api/v1/connections/test", editor(http.HandlerFunc(h.TestConnection)))

	// Metrics streaming via Server-Sent Events
	mux.Handle("GET /api/v1/connections/{id}/metrics", viewer(http.HandlerFunc(h.HandleConnectionMetrics)))
	mux.Handle("GET /api/v1/connections/{id}/metrics/stream", viewer(http.HandlerFunc(h.HandleMetricsSSE)))
	mux.Handle("GET /api/v1/connections/{id}/metrics/ws", viewer(http.HandlerFunc(h.HandleMetricsWebSocket)))

	// Sample data for filter preview
	mux.Handle("GET /api/v1/connections/{id}/sample-data", viewer(http.HandlerFunc(h.GetSampleData)))
	mux.Handle("GET /api/v1/sample-data/source", viewer(http.HandlerFunc(h.GetSourceSampleData)))

	// Test data generation — editor (sends real data through a pipeline)
	mux.Handle("POST /api/v1/connections/{id}/test-message", editor(http.HandlerFunc(h.SendSingleTestMessage)))
	mux.Handle("POST /api/v1/connections/{id}/auto-generator/start", editor(http.HandlerFunc(h.StartAutoGenerator)))
	mux.Handle("POST /api/v1/connections/{id}/auto-generator/stop", editor(http.HandlerFunc(h.StopAutoGenerator)))
	mux.Handle("GET /api/v1/connections/{id}/auto-generator/status", viewer(http.HandlerFunc(h.GetAutoGeneratorStatus)))

	// API Consumer routes
	h.RegisterAPIConsumerRoutes(mux)

	// Secrets (Phase 1A — #66). Reads = viewer; writes/rotate = editor;
	// delete = admin. Branching happens inside SecretsCollection /
	// SecretsItem; we apply editor here as the minimum so reads still
	// pass through (editor ≥ viewer). The handler enforces the
	// admin-for-delete rule inline.
	mux.Handle("/api/v1/secrets", roleByMethod(h.repo, http.HandlerFunc(h.SecretsCollection), map[string]string{
		"POST": "editor",
		"GET":  "viewer",
	}))
	mux.Handle("/api/v1/secrets/", roleByMethod(h.repo, http.HandlerFunc(h.SecretsItem), map[string]string{
		"GET":    "viewer",
		"PUT":    "editor",
		"POST":   "editor", // /rotate
		"DELETE": "admin",
	}))

	// Dead-letter queue (Phase 1E — #70). Reads = viewer, retry/discard = editor.
	mux.Handle("GET /api/v1/connections/{id}/dlq", viewer(http.HandlerFunc(h.DLQRouter)))
	mux.Handle("GET /api/v1/connections/{id}/dlq/{seq}", viewer(http.HandlerFunc(h.DLQRouter)))
	mux.Handle("POST /api/v1/connections/{id}/dlq/{seq}/retry", editor(http.HandlerFunc(h.DLQRouter)))
	mux.Handle("POST /api/v1/connections/{id}/dlq/{seq}/discard", editor(http.HandlerFunc(h.DLQRouter)))

	// Audit log (Phase 1G — #72). Read-only — writes happen via middleware.
	// viewer-gated: the handler scopes by the X-Tenant-ID header, which is
	// untrusted on its own, so the role check enforces tenant isolation.
	mux.Handle("GET /api/v1/audit", viewer(http.HandlerFunc(h.ListAudit)))

	// OAuth 2.0 framework (Phase 2A — #75). Provider CRUD is admin; viewing
	// providers / grants is viewer; the auth start is editor; the callback
	// is public + cookie-gated (it's hit via browser redirect from the IdP).
	mux.Handle("GET /api/v1/oauth/providers", viewer(http.HandlerFunc(h.ListOAuthProvidersHandler)))
	mux.Handle("POST /api/v1/oauth/providers", adminMW(http.HandlerFunc(h.CreateOAuthProvider)))
	mux.Handle("GET /api/v1/oauth/providers/{id}", viewer(http.HandlerFunc(h.GetOAuthProvider)))
	mux.Handle("PUT /api/v1/oauth/providers/{id}", adminMW(http.HandlerFunc(h.UpdateOAuthProvider)))
	mux.Handle("DELETE /api/v1/oauth/providers/{id}", adminMW(http.HandlerFunc(h.DeleteOAuthProvider)))
	mux.Handle("POST /api/v1/oauth/providers/{id}/start", editor(http.HandlerFunc(h.StartOAuth)))
	mux.HandleFunc("GET /api/v1/oauth/callback", h.HandleOAuthCallback)
	mux.Handle("GET /api/v1/oauth/grants", viewer(http.HandlerFunc(h.ListOAuthGrants)))
	mux.Handle("GET /api/v1/oauth/grants/{id}", viewer(http.HandlerFunc(h.GetOAuthGrant)))
	mux.Handle("POST /api/v1/oauth/grants/{id}/revoke", editor(http.HandlerFunc(h.RevokeOAuthGrant)))
	// Service-only token endpoint for workers — authenticated by the shared
	// X-Service-Token secret inside the handler, not the user-session
	// middleware (workers have no session). Registered plain for that reason.
	mux.HandleFunc("GET /api/v1/oauth/grants/{id}/token", h.TokenForGrant)

	// Notification targets (Phase 3A — #84). Writes are admin; listing is
	// viewer; the test send is admin (it uses the stored secret).
	mux.Handle("GET /api/v1/notifications/targets", viewer(http.HandlerFunc(h.ListNotificationTargets)))
	mux.Handle("POST /api/v1/notifications/targets", adminMW(http.HandlerFunc(h.CreateNotificationTarget)))
	mux.Handle("PUT /api/v1/notifications/targets/{id}", adminMW(http.HandlerFunc(h.UpdateNotificationTarget)))
	mux.Handle("DELETE /api/v1/notifications/targets/{id}", adminMW(http.HandlerFunc(h.DeleteNotificationTarget)))
	mux.Handle("POST /api/v1/notifications/targets/{id}/test", adminMW(http.HandlerFunc(h.TestNotificationTarget)))
	// Alertmanager's webhook receiver — authenticated by the shared
	// ALERTS_WEBHOOK_TOKEN bearer inside the handler (Alertmanager has no
	// session or tenant header). Exempted from TenantIDMiddleware in cors.go.
	mux.HandleFunc("POST /api/v1/alerts/webhook", h.AlertsWebhook)

	// Auth routes (these bypass TenantIDMiddleware)
	h.RegisterAuthRoutes(mux)
}

// roleByMethod dispatches role requirements based on the HTTP method —
// useful for endpoints like /api/v1/secrets where the same path serves
// reads, writes, and deletes that demand different minimum roles.
func roleByMethod(repo Repository, next http.Handler, perMethod map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		min, ok := perMethod[r.Method]
		if !ok {
			min = "admin" // fail-closed default
		}
		RequireTenantRoleFromHeader(repo, min)(next).ServeHTTP(w, r)
	})
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

	// OIDC / SSO endpoints (Phase 1C — #68). All public — the SSO flow
	// itself authenticates the user.
	mux.HandleFunc("GET /api/v1/auth/oidc/{slug}/available", h.HandleOIDCAvailable)
	mux.HandleFunc("GET /api/v1/auth/oidc/{slug}/login", h.HandleOIDCLogin)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", h.HandleOIDCCallback)

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

	// OIDC admin CRUD (#68). Owners + admins only — keep SSO config off the
	// general member's reach.
	adminMW := RequireRole("admin")
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/oidc", sessionMW(tenantMW(adminMW(http.HandlerFunc(h.HandleOIDCConfigRead)))).ServeHTTP)
	mux.HandleFunc("PUT /api/v1/tenants/{tenant_id}/oidc", sessionMW(tenantMW(adminMW(http.HandlerFunc(h.HandleOIDCConfigUpsert)))).ServeHTTP)
	mux.HandleFunc("DELETE /api/v1/tenants/{tenant_id}/oidc", sessionMW(tenantMW(adminMW(http.HandlerFunc(h.HandleOIDCConfigDelete)))).ServeHTTP)

	// Tenant members admin (#69). Reading membership: any member.
	// Mutations (set role / remove): owner only — separation of duties
	// because admins can manage resources but not redistribute power.
	ownerMW := RequireRole("owner")
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/members", sessionMW(tenantMW(http.HandlerFunc(h.HandleListMembers))).ServeHTTP)
	mux.HandleFunc("PUT /api/v1/tenants/{tenant_id}/members/{user_id}", sessionMW(tenantMW(ownerMW(http.HandlerFunc(h.HandleSetMemberRole)))).ServeHTTP)
	mux.HandleFunc("DELETE /api/v1/tenants/{tenant_id}/members/{user_id}", sessionMW(tenantMW(ownerMW(http.HandlerFunc(h.HandleRemoveMember)))).ServeHTTP)

	// Tenant quotas (#74). Reads = any member; writes = owner.
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/quotas", sessionMW(tenantMW(http.HandlerFunc(h.HandleGetQuotas))).ServeHTTP)
	mux.HandleFunc("PUT /api/v1/tenants/{tenant_id}/quotas", sessionMW(tenantMW(ownerMW(http.HandlerFunc(h.HandleUpdateQuotas)))).ServeHTTP)

	// Per-tenant usage metering (#92). Any member can read usage + export CSV.
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/usage", sessionMW(tenantMW(http.HandlerFunc(h.HandleGetUsage))).ServeHTTP)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/usage/export", sessionMW(tenantMW(http.HandlerFunc(h.HandleExportUsage))).ServeHTTP)

	// Subscription plan (#90). Owner-only; drives the gateway's per-tenant edge
	// rate limit (free/pro/enterprise).
	mux.HandleFunc("PUT /api/v1/tenants/{tenant_id}/plan", sessionMW(tenantMW(ownerMW(http.HandlerFunc(h.HandlePlanUpdate)))).ServeHTTP)

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
