package managementapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ============================================================================
// API CONSUMER REQUEST/RESPONSE TYPES
// ============================================================================

// APIConsumerEndpointRequest represents a single endpoint configuration in API requests
type APIConsumerEndpointRequest struct {
	Path      string `json:"path"`       // Endpoint path (e.g., "/api/v2/forecast")
	AuthType  string `json:"auth_type"`  // "none", "bearer", "api_key"
	AuthValue string `json:"auth_value"` // Token or API key value
}

// APIConsumerRequest represents the API consumer configuration in requests
type APIConsumerRequest struct {
	BaseURL             string                       `json:"base_url"`
	Endpoints           []APIConsumerEndpointRequest `json:"endpoints"`
	PollIntervalSeconds int                          `json:"poll_interval_seconds"`
	OneTimeOnly         bool                         `json:"one_time_only"` // If true, retrieve data once and stop (no polling)
}

// CreateAPIConsumerRequest is the request body for creating an API consumer
type CreateAPIConsumerRequest struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	APIConsumer APIConsumerRequest `json:"api_consumer"`
}

// UpdateAPIConsumerRequest is the request body for updating an API consumer
type UpdateAPIConsumerRequest struct {
	Name        *string             `json:"name,omitempty"`
	Description *string             `json:"description,omitempty"`
	APIConsumer *APIConsumerRequest `json:"api_consumer,omitempty"`
}

// APIConsumerSourceConfig extends SourceConfig for API consumers
type APIConsumerSourceConfig struct {
	BaseURL             string                      `json:"base_url"`
	Endpoints           []APIConsumerEndpointConfig `json:"endpoints"`
	PollIntervalSeconds int                         `json:"poll_interval_seconds"`
	OneTimeOnly         bool                        `json:"one_time_only"` // If true, retrieve data once and stop (no polling)
}

// APIConsumerEndpointConfig represents endpoint config stored in the database
type APIConsumerEndpointConfig struct {
	Path string      `json:"path"`
	Auth *AuthConfig `json:"auth,omitempty"`
}

// ============================================================================
// VALIDATION
// ============================================================================

// validateAPIConsumerRequest validates the API consumer request
func validateAPIConsumerRequest(req *APIConsumerRequest) error {
	// Validate base URL
	if strings.TrimSpace(req.BaseURL) == "" {
		return fmt.Errorf("base_url is required")
	}

	// Validate URL format
	parsedURL, err := url.Parse(req.BaseURL)
	if err != nil {
		return fmt.Errorf("invalid base_url format: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("base_url must use http or https scheme")
	}
	if parsedURL.Host == "" {
		return fmt.Errorf("base_url must have a valid host")
	}

	// Validate endpoints
	if len(req.Endpoints) == 0 {
		return fmt.Errorf("at least one endpoint is required")
	}

	for i, ep := range req.Endpoints {
		if strings.TrimSpace(ep.Path) == "" {
			return fmt.Errorf("endpoint[%d]: path is required", i)
		}
		if !strings.HasPrefix(ep.Path, "/") {
			return fmt.Errorf("endpoint[%d]: path must start with /", i)
		}

		// Validate auth type
		switch ep.AuthType {
		case "none", "":
			// No validation needed
		case "bearer", "api_key":
			if strings.TrimSpace(ep.AuthValue) == "" {
				return fmt.Errorf("endpoint[%d]: auth_value is required for %s auth", i, ep.AuthType)
			}
		default:
			return fmt.Errorf("endpoint[%d]: invalid auth_type '%s', must be 'none', 'bearer', or 'api_key'", i, ep.AuthType)
		}
	}

	// Validate poll interval (only required for continuous polling)
	if !req.OneTimeOnly {
		if req.PollIntervalSeconds < 10 {
			return fmt.Errorf("poll_interval_seconds must be at least 10")
		}
		if req.PollIntervalSeconds > 3600 {
			return fmt.Errorf("poll_interval_seconds must be at most 3600 (1 hour)")
		}
	}

	return nil
}

// ============================================================================
// CONVERSION HELPERS
// ============================================================================

// convertAPIConsumerToSourceConfig converts API consumer request to SourceConfig
func convertAPIConsumerToSourceConfig(req *APIConsumerRequest) SourceConfig {
	endpoints := make([]APIConsumerEndpointConfig, len(req.Endpoints))

	for i, ep := range req.Endpoints {
		endpoints[i] = APIConsumerEndpointConfig{
			Path: ep.Path,
			Auth: convertAuthType(ep.AuthType, ep.AuthValue),
		}
	}

	// Store as JSON in the API field
	apiConfig := &APIConsumerSourceConfig{
		BaseURL:             req.BaseURL,
		Endpoints:           endpoints,
		PollIntervalSeconds: req.PollIntervalSeconds,
		OneTimeOnly:         req.OneTimeOnly,
	}

	// Serialize to raw JSON for storage
	apiConfigJSON, _ := json.Marshal(apiConfig)

	return SourceConfig{
		Type: "api",
		// Store raw config in a way that can be retrieved
		// We use the HTTP field temporarily - in production you'd add an API field
		HTTP: &HTTPSourceConfig{
			URL:    req.BaseURL,
			Method: "GET",
			Polling: &PollingConfig{
				Interval: req.PollIntervalSeconds,
				Timeout:  30,
			},
		},
		// Also store endpoints data as file pattern for now (hacky but works)
		File: &FileSourceConfig{
			Path:    string(apiConfigJSON), // Store full config as JSON string
			Pattern: "api_consumer",        // Marker to identify this as API consumer
		},
	}
}

// convertAuthType converts auth type string to AuthConfig
func convertAuthType(authType, authValue string) *AuthConfig {
	switch authType {
	case "bearer":
		return &AuthConfig{
			Type: "bearer",
			Bearer: &BearerAuthConfig{
				Token: authValue,
			},
		}
	case "api_key":
		return &AuthConfig{
			Type: "api_key",
			APIKey: &APIKeyAuthConfig{
				HeaderName: "X-API-Key",
				Key:        authValue,
			},
		}
	default:
		return nil
	}
}

// extractAPIConsumerFromSourceConfig extracts API consumer config from SourceConfig
func extractAPIConsumerFromSourceConfig(cfg SourceConfig) (*APIConsumerRequest, error) {
	if cfg.Type != "api" {
		return nil, fmt.Errorf("not an API consumer config")
	}

	// Check if this is our API consumer marker
	if cfg.File == nil || cfg.File.Pattern != "api_consumer" {
		return nil, fmt.Errorf("invalid API consumer config format")
	}

	// Parse the stored JSON config
	var apiConfig APIConsumerSourceConfig
	if err := json.Unmarshal([]byte(cfg.File.Path), &apiConfig); err != nil {
		return nil, fmt.Errorf("failed to parse API consumer config: %w", err)
	}

	// Convert back to request format
	endpoints := make([]APIConsumerEndpointRequest, len(apiConfig.Endpoints))
	for i, ep := range apiConfig.Endpoints {
		authType := "none"
		authValue := ""
		if ep.Auth != nil {
			authType = ep.Auth.Type
			if ep.Auth.Bearer != nil {
				authValue = ep.Auth.Bearer.Token
			} else if ep.Auth.APIKey != nil {
				authValue = ep.Auth.APIKey.Key
			}
		}
		endpoints[i] = APIConsumerEndpointRequest{
			Path:      ep.Path,
			AuthType:  authType,
			AuthValue: authValue,
		}
	}

	return &APIConsumerRequest{
		BaseURL:             apiConfig.BaseURL,
		Endpoints:           endpoints,
		PollIntervalSeconds: apiConfig.PollIntervalSeconds,
	}, nil
}

// ============================================================================
// HTTP HANDLERS
// ============================================================================

// CreateAPIConsumer handles POST /api/v1/api-consumers
func (h *Handler) CreateAPIConsumer(w http.ResponseWriter, r *http.Request) {
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
	var req CreateAPIConsumerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err == io.EOF {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
		} else {
			_ = writeError(w, http.StatusBadRequest, "InvalidJSON", fmt.Sprintf("failed to parse request: %v", err), nil)
		}
		return
	}

	// Validate name
	if strings.TrimSpace(req.Name) == "" {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "name is required", nil)
		return
	}

	// Validate API consumer config
	if err := validateAPIConsumerRequest(&req.APIConsumer); err != nil {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
		return
	}

	// Convert to connection with API consumer source config
	connReq := CreateConnectionRequest{
		Name:         req.Name,
		Description:  req.Description,
		SourceConfig: convertAPIConsumerToSourceConfig(&req.APIConsumer),
		// Set minimal converter/filter/destination configs
		ConverterConfig: ConverterConfig{},
		FilterConfig:    FilterConfig{},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://localhost:8080/output", // Placeholder
				Method: "POST",
			},
		},
	}

	// Create connection
	conn := NewConnection(tenantID, connReq)

	// Save to database
	if err := h.repo.CreateConnection(ctx, conn); err != nil {
		if conflictErr, ok := err.(*ConflictError); ok {
			_ = writeError(w, http.StatusConflict, "Conflict", conflictErr.Error(), nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to create API consumer", nil)
		}
		return
	}

	// Return created response with API consumer format
	response := map[string]interface{}{
		"id":           conn.ID,
		"tenant_id":    conn.TenantID,
		"name":         conn.Name,
		"description":  conn.Description,
		"status":       conn.Status,
		"api_consumer": req.APIConsumer,
		"created_at":   conn.CreatedAt,
		"updated_at":   conn.UpdatedAt,
	}

	w.Header().Set("Location", fmt.Sprintf("/api/v1/api-consumers/%s", conn.ID))
	_ = writeJSON(w, http.StatusCreated, SuccessResponse{Data: response})
}

// GetAPIConsumer handles GET /api/v1/api-consumers/{id}
func (h *Handler) GetAPIConsumer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "API consumer ID is required", nil)
		return
	}

	// Get connection
	conn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "API consumer not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get API consumer", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	// Verify it's an API consumer
	if conn.SourceConfig.Type != "api" {
		_ = writeError(w, http.StatusNotFound, "NotFound", "API consumer not found", nil)
		return
	}

	// Extract API consumer config
	apiConsumer, err := extractAPIConsumerFromSourceConfig(conn.SourceConfig)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to parse API consumer config", nil)
		return
	}

	// Return response
	response := map[string]interface{}{
		"id":           conn.ID,
		"tenant_id":    conn.TenantID,
		"name":         conn.Name,
		"description":  conn.Description,
		"status":       conn.Status,
		"api_consumer": apiConsumer,
		"created_at":   conn.CreatedAt,
		"updated_at":   conn.UpdatedAt,
		"started_at":   conn.StartedAt,
		"stopped_at":   conn.StoppedAt,
	}

	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: response})
}

// UpdateAPIConsumer handles PUT /api/v1/api-consumers/{id}
func (h *Handler) UpdateAPIConsumer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "API consumer ID is required", nil)
		return
	}

	// Get existing connection
	conn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "API consumer not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get API consumer", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	// Verify it's an API consumer
	if conn.SourceConfig.Type != "api" {
		_ = writeError(w, http.StatusNotFound, "NotFound", "API consumer not found", nil)
		return
	}

	// Prevent updating running connections
	if conn.Status == "running" {
		_ = writeError(w, http.StatusBadRequest, "InvalidState", "cannot update a running API consumer, please stop it first", nil)
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

	// Parse request body
	var req UpdateAPIConsumerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", fmt.Sprintf("failed to parse request: %v", err), nil)
		return
	}

	// Apply updates
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		conn.Name = *req.Name
	}
	if req.Description != nil {
		conn.Description = *req.Description
	}
	if req.APIConsumer != nil {
		// Validate new API consumer config
		if err := validateAPIConsumerRequest(req.APIConsumer); err != nil {
			_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
			return
		}
		conn.SourceConfig = convertAPIConsumerToSourceConfig(req.APIConsumer)
	}

	// Update in database
	if err := h.repo.UpdateConnection(ctx, conn); err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "API consumer not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to update API consumer", nil)
		}
		return
	}

	// Extract updated API consumer config
	apiConsumer, err := extractAPIConsumerFromSourceConfig(conn.SourceConfig)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to parse API consumer config", nil)
		return
	}

	// Return response
	response := map[string]interface{}{
		"id":           conn.ID,
		"tenant_id":    conn.TenantID,
		"name":         conn.Name,
		"description":  conn.Description,
		"status":       conn.Status,
		"api_consumer": apiConsumer,
		"created_at":   conn.CreatedAt,
		"updated_at":   conn.UpdatedAt,
	}

	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: response})
}

// DeleteAPIConsumer handles DELETE /api/v1/api-consumers/{id}
func (h *Handler) DeleteAPIConsumer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract ID from URL path
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "API consumer ID is required", nil)
		return
	}

	// Get connection to verify ownership and type
	conn, err := h.repo.GetConnection(ctx, id)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "API consumer not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to get API consumer", nil)
		}
		return
	}

	// Verify tenant ownership
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return
	}

	// Verify it's an API consumer
	if conn.SourceConfig.Type != "api" {
		_ = writeError(w, http.StatusNotFound, "NotFound", "API consumer not found", nil)
		return
	}

	// Prevent deleting running connections
	if conn.Status == "running" {
		_ = writeError(w, http.StatusBadRequest, "InvalidState", "cannot delete a running API consumer, please stop it first", nil)
		return
	}

	// Delete connection
	if err := h.repo.DeleteConnection(ctx, id); err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "API consumer not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to delete API consumer", nil)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListAPIConsumers handles GET /api/v1/api-consumers
func (h *Handler) ListAPIConsumers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Get all connections and filter for API consumers
	connections, _, err := h.repo.ListConnections(ctx, tenantID, &ListFilters{
		Limit:  100,
		Offset: 0,
	})
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to list API consumers", nil)
		return
	}

	// Filter for API consumers and convert to response format
	apiConsumers := make([]map[string]interface{}, 0)
	for _, conn := range connections {
		if conn.SourceConfig.Type != "api" {
			continue
		}

		apiConsumer, err := extractAPIConsumerFromSourceConfig(conn.SourceConfig)
		if err != nil {
			continue // Skip invalid entries
		}

		apiConsumers = append(apiConsumers, map[string]interface{}{
			"id":           conn.ID,
			"tenant_id":    conn.TenantID,
			"name":         conn.Name,
			"description":  conn.Description,
			"status":       conn.Status,
			"api_consumer": apiConsumer,
			"created_at":   conn.CreatedAt,
			"updated_at":   conn.UpdatedAt,
			"started_at":   conn.StartedAt,
			"stopped_at":   conn.StoppedAt,
		})
	}

	_ = writeJSON(w, http.StatusOK, ListResponse{
		Data:   nil, // Not used for this response
		Total:  int64(len(apiConsumers)),
		Limit:  100,
		Offset: 0,
	})

	// Override with custom response
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data":   apiConsumers,
		"total":  len(apiConsumers),
		"limit":  100,
		"offset": 0,
	})
}

// RegisterAPIConsumerRoutes registers API consumer specific routes
func (h *Handler) RegisterAPIConsumerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/api-consumers", h.CreateAPIConsumer)
	mux.HandleFunc("GET /api/v1/api-consumers", h.ListAPIConsumers)
	mux.HandleFunc("GET /api/v1/api-consumers/{id}", h.GetAPIConsumer)
	mux.HandleFunc("PUT /api/v1/api-consumers/{id}", h.UpdateAPIConsumer)
	mux.HandleFunc("DELETE /api/v1/api-consumers/{id}", h.DeleteAPIConsumer)
}
