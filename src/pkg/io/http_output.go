package io

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// HTTPOutputConfig defines the configuration for HTTP Output
type HTTPOutputConfig struct {
	URL     string            `json:"url"`               // Target HTTP endpoint
	Method  string            `json:"method,omitempty"`  // HTTP method (default: POST)
	Timeout int               `json:"timeout,omitempty"` // Request timeout in seconds (default: 30)
	Retries int               `json:"retries,omitempty"` // Number of retries (default: 1)
	Headers map[string]string `json:"headers,omitempty"` // Additional headers
}

// HTTPOutput writes messages to an HTTP endpoint with retry logic
type HTTPOutput struct {
	url      string
	method   string
	timeout  time.Duration
	maxRetry int
	headers  map[string]string
	client   *http.Client
}

// NewHTTPOutput creates a new HTTP output writer from JSON config
func NewHTTPOutput(configJSON json.RawMessage) (*HTTPOutput, error) {
	config := HTTPOutputConfig{
		Method:  "POST",
		Timeout: 30,
		Retries: 1,
	}

	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, fmt.Errorf("failed to parse HTTP output config: %w", err)
	}

	if config.URL == "" {
		return nil, fmt.Errorf("HTTP output URL is required")
	}

	timeout := time.Duration(config.Timeout) * time.Second
	return &HTTPOutput{
		url:      config.URL,
		method:   config.Method,
		timeout:  timeout,
		maxRetry: config.Retries,
		headers:  config.Headers,
		client: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

// Start initializes the HTTP output (no-op for HTTP)
func (h *HTTPOutput) Start(ctx context.Context) error {
	slog.Info("HTTP output started", "url", h.url)
	return nil
}

// Write sends the envelope to the configured HTTP endpoint with retries
func (h *HTTPOutput) Write(ctx context.Context, env *envelope.Envelope) error {
	if h.url == "" {
		return fmt.Errorf("HTTP output URL not configured")
	}

	slog.Debug("Writing to HTTP endpoint",
		"url", h.url,
		"method", h.method,
		"message_id", env.ID)

	// Create request with payload
	req, err := http.NewRequestWithContext(ctx, h.method, h.url, nil)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Retry logic with exponential backoff
	var lastErr error
	for attempt := 0; attempt < h.maxRetry; attempt++ {
		// Create fresh request body for retry
		req.Body = io.NopCloser(bytes.NewReader(env.Payload))
		req.ContentLength = int64(len(env.Payload))

		// Set content type
		if env.ContentType != "" {
			req.Header.Set("Content-Type", env.ContentType)
		} else {
			req.Header.Set("Content-Type", "text/plain")
		}

		// Add custom headers
		for k, v := range h.headers {
			req.Header.Set(k, v)
		}

		// Add X-Message-ID header for tracking
		req.Header.Set("X-Message-ID", env.ID)

		slog.Debug("HTTP request attempt",
			"attempt", attempt+1,
			"url", h.url,
			"message_id", env.ID)

		// Send request
		resp, err := h.client.Do(req)
		if err != nil {
			lastErr = err
			slog.Debug("HTTP request failed", "error", err, "attempt", attempt+1)
			// Backoff before retry
			if attempt < h.maxRetry-1 {
				waitTime := time.Duration(1<<uint(attempt)) * time.Second
				select {
				case <-time.After(waitTime):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			continue
		}

		// Check response status
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			slog.Info("Message sent successfully via HTTP",
				"url", h.url,
				"status", resp.StatusCode,
				"message_id", env.ID)
			return nil
		}

		// Non-success status - retry or fail
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
		slog.Debug("HTTP request returned error status",
			"status", resp.StatusCode,
			"attempt", attempt+1,
			"message_id", env.ID)

		// Backoff before retry
		if attempt < h.maxRetry-1 {
			waitTime := time.Duration(1<<uint(attempt)) * time.Second
			select {
			case <-time.After(waitTime):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	return fmt.Errorf("failed to write to HTTP after %d attempts: %w", h.maxRetry, lastErr)
}

// Close closes the HTTP client
func (h *HTTPOutput) Close() error {
	if h.client != nil {
		h.client.CloseIdleConnections()
	}
	return nil
}
