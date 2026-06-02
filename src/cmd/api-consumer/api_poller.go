package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
)

// pollConnection runs the main polling loop for an API consumer
func (s *APIConsumerService) pollConnection(ctx context.Context, connectionID, tenantID string, config *APIConsumerConfig) {
	logger := s.logger.With("connection_id", connectionID, "tenant_id", tenantID)
	logger.Info("Starting API polling",
		"base_url", config.BaseURL,
		"endpoints", len(config.Endpoints),
		"poll_interval", config.PollIntervalSeconds,
		"one_time_only", config.OneTimeOnly)

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: s.config.PollTimeout,
	}

	// If one-time-only mode, just poll once and return
	if config.OneTimeOnly {
		logger.Info("One-time-only mode: retrieving data once")
		s.pollAllEndpoints(ctx, client, connectionID, tenantID, config, logger)
		logger.Info("One-time-only mode: data retrieval complete")
		// Update status to stopped since poll is done
		if err := s.updateConnectionStatus(connectionID, tenantID, "stopped"); err != nil {
			logger.Error("Failed to update connection status after one-time poll", "error", err)
		}
		// Remove from active pipelines
		s.mu.Lock()
		delete(s.activePipelines, connectionID)
		s.mu.Unlock()
		return
	}

	// Calculate poll interval for continuous polling
	pollInterval := time.Duration(config.PollIntervalSeconds) * time.Second
	if pollInterval <= 0 {
		pollInterval = s.config.DefaultPollInterval
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Poll immediately on start
	s.pollAllEndpoints(ctx, client, connectionID, tenantID, config, logger)

	// Then poll on interval
	for {
		select {
		case <-ctx.Done():
			logger.Info("Polling stopped")
			return
		case <-ticker.C:
			s.pollAllEndpoints(ctx, client, connectionID, tenantID, config, logger)
		}
	}
}

// pollAllEndpoints polls all configured endpoints
func (s *APIConsumerService) pollAllEndpoints(ctx context.Context, client *http.Client, connectionID, tenantID string, config *APIConsumerConfig, logger *slog.Logger) {
	for i, endpoint := range config.Endpoints {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Build full URL — if path is already a full URL, use it directly
		var url string
		if strings.HasPrefix(endpoint.Path, "http://") || strings.HasPrefix(endpoint.Path, "https://") {
			url = endpoint.Path
		} else {
			url = strings.TrimSuffix(config.BaseURL, "/") + endpoint.Path
		}
		if endpoint.Params != "" {
			// Add query parameters
			if strings.Contains(url, "?") {
				url += "&" + endpoint.Params
			} else {
				url += "?" + endpoint.Params
			}
		}

		logger.Debug("Polling endpoint", "url", url, "endpoint_index", i)

		// Make the API call
		payload, contentType, err := s.callEndpoint(ctx, client, url, tenantID, endpoint, logger)
		if err != nil {
			logger.Error("Failed to poll endpoint", "url", url, "error", err)
			// Continue to next endpoint, don't fail the whole polling cycle
			continue
		}

		logger.Info("Successfully polled endpoint", "url", url, "payload_size", len(payload), "content_type", contentType)

		// Publish to NATS
		if err := s.publishToNATS(connectionID, tenantID, payload, contentType, url); err != nil {
			logger.Error("Failed to publish to NATS", "error", err)
			continue
		}

		logger.Debug("Published to NATS", "connection_id", connectionID)
	}
}

// callEndpoint makes an HTTP request to the specified endpoint. For
// auth_type=oauth it resolves a fresh access token from management-api; if the
// endpoint answers 401 it refreshes the token and retries exactly once
// (transparent refresh — #75 criterion #3).
func (s *APIConsumerService) callEndpoint(ctx context.Context, client *http.Client, url, tenantID string, endpoint APIEndpoint, logger *slog.Logger) ([]byte, string, error) {
	// buildAndSend issues one request. forceRefresh asks the token service to
	// refresh the grant first (the post-401 retry).
	buildAndSend := func(forceRefresh bool) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		switch {
		case endpoint.AuthType == "oauth" && endpoint.OAuthGrantID == "":
			// OAuth selected but no grant (e.g. the grant was revoked, which
			// clears the selection on the node). Rather than blocking the poll
			// — or calling /oauth/grants//token with an empty id and getting an
			// opaque 500 — fall back to sending unauthenticated and warn. A
			// proper "reconnect required" hint in the UI is tracked in #98.
			logger.Warn("OAuth endpoint has no grant selected; polling without authentication (reconnect the grant to authenticate)",
				"url", url)
		case endpoint.AuthType == "oauth":
			if s.oauthTokens == nil || !s.oauthTokens.Configured() {
				return nil, fmt.Errorf("endpoint uses OAuth but token resolution is not configured (set MGMT_API_URL + OAUTH_TOKEN_SERVICE_SECRET)")
			}
			var tok string
			if forceRefresh {
				tok, err = s.oauthTokens.ForceToken(ctx, tenantID, endpoint.OAuthGrantID)
			} else {
				tok, err = s.oauthTokens.Token(ctx, tenantID, endpoint.OAuthGrantID)
			}
			if err != nil {
				return nil, fmt.Errorf("resolve oauth token: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		default:
			applyAuth(req, endpoint.AuthType, endpoint.AuthValue)
		}
		req.Header.Set("User-Agent", "VRSky-API-Consumer/1.0")
		req.Header.Set("Accept", "*/*")
		return client.Do(req)
	}

	resp, err := buildAndSend(false)
	if err != nil {
		return nil, "", fmt.Errorf("request failed: %w", err)
	}
	// On a 401 for an OAuth endpoint, the token may have been revoked or
	// rotated server-side; refresh and retry once before giving up.
	if resp.StatusCode == http.StatusUnauthorized && endpoint.AuthType == "oauth" && endpoint.OAuthGrantID != "" {
		_ = resp.Body.Close()
		logger.Info("OAuth endpoint returned 401; refreshing token and retrying once",
			"grant_id", endpoint.OAuthGrantID)
		resp, err = buildAndSend(true)
		if err != nil {
			return nil, "", fmt.Errorf("request failed after token refresh: %w", err)
		}
	}
	defer resp.Body.Close()

	// Check status code - only accept 2xx responses
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024)) // Read first 1KB for error message
		if readErr != nil {
			logger.Warn("Failed to read error response body", "error", readErr)
		}
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response: %w", err)
	}

	// Get content type
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = detectContentType(body)
	}

	return body, contentType, nil
}

// applyAuth adds authentication headers to the request
func applyAuth(req *http.Request, authType, authValue string) {
	if authValue == "" {
		return
	}

	switch authType {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+authValue)
	case "api_key":
		// Common API key header names
		req.Header.Set("X-API-Key", authValue)
	case "none", "":
		// No authentication
	}
}

// detectContentType attempts to detect content type from payload
func detectContentType(payload []byte) string {
	if len(payload) == 0 {
		return "application/octet-stream"
	}

	// Try to detect JSON
	payload = trimWhitespace(payload)
	if len(payload) > 0 && (payload[0] == '{' || payload[0] == '[') {
		// Validate it's actually JSON
		var js json.RawMessage
		if json.Unmarshal(payload, &js) == nil {
			return "application/json"
		}
	}

	// Try to detect XML
	if len(payload) > 0 && payload[0] == '<' {
		return "application/xml"
	}

	// Try to detect CSV (simple heuristic)
	if containsCSVPattern(payload) {
		return "text/csv"
	}

	// Default to plain text if printable, otherwise binary
	if isPrintable(payload) {
		return "text/plain"
	}

	return "application/octet-stream"
}

// trimWhitespace removes leading whitespace from payload
func trimWhitespace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	return b
}

// containsCSVPattern checks if payload looks like CSV
func containsCSVPattern(payload []byte) bool {
	// Simple heuristic: contains commas and newlines
	hasComma := false
	hasNewline := false
	for _, b := range payload {
		if b == ',' {
			hasComma = true
		}
		if b == '\n' {
			hasNewline = true
		}
		if hasComma && hasNewline {
			return true
		}
	}
	return false
}

// isPrintable checks if payload contains only printable ASCII
func isPrintable(payload []byte) bool {
	for _, b := range payload {
		if b < 32 && b != '\t' && b != '\n' && b != '\r' {
			return false
		}
	}
	return true
}

// publishToNATS wraps the payload in an envelope and publishes to NATS
func (s *APIConsumerService) publishToNATS(connectionID, tenantID string, payload []byte, contentType, source string) error {
	// Create envelope
	env := &envelope.Envelope{
		ID:            uuid.New().String(),
		TenantID:      tenantID,
		IntegrationID: connectionID, // Used for routing to the correct producer
		Payload:       payload,
		PayloadSize:   int64(len(payload)),
		ContentType:   contentType,
		Source:        source,
		CurrentStep:   0,
		StepHistory:   []string{"api-consumer"},
		CreatedAt:     time.Now().UTC(),
	}

	// Serialize envelope
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}

	// Publish to JetStream (at-least-once). The MsgID dedupes inside the
	// stream's 5-min window so a retried poll cycle does not duplicate.
	if err := s.pub.Publish(context.Background(), tenantID, connectionID, env.ID, data); err != nil {
		return fmt.Errorf("failed to publish to JetStream: %w", err)
	}

	s.logger.Debug("Published envelope to JetStream",
		"tenant", tenantID,
		"connection", connectionID,
		"envelope_id", env.ID,
		"payload_size", env.PayloadSize)

	// Store last payload in DB for tenant-consumer bridges to read
	_, _ = s.db.Exec("UPDATE connections SET last_payload = $1 WHERE id = $2", data, connectionID)

	return nil
}
