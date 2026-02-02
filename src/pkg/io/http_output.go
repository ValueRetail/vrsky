package io

import (
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
	URL     string `json:"url"`              // Target HTTP endpoint
	Method  string `json:"method,omitempty"` // HTTP method (default: POST)
	Timeout int    `json:"timeout,omitempty"` // Request timeout in seconds (default: 30)
	Retries int    `json:"retries,omitempty"` // Number of retries (default: 1)
}

// HTTPOutput implements the Output interface for HTTP POST requests
type HTTPOutput struct {
	config HTTPOutputConfig
	client *http.Client
}

// NewHTTPOutput creates a new HTTP output from JSON configuration
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
		return nil, fmt.Errorf("HTTP URL is required")
	}

	client := &http.Client{
		Timeout: time.Duration(config.Timeout) * time.Second,
	}

	return &HTTPOutput{
		config: config,
		client: client,
	}, nil
}

// Write sends the envelope payload to the HTTP endpoint
func (h *HTTPOutput) Write(ctx context.Context, env *envelope.Envelope) error {
	var lastErr error

	// Attempt to send with retries
	attempts := h.config.Retries + 1 // +1 for initial attempt
	for attempt := 1; attempt <= attempts; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := h.sendRequest(ctx, env)
		if err == nil {
			slog.Info("HTTP POST successful",
				"url", h.config.URL,
				"message_id", env.ID,
				"size", len(env.Payload))
			return nil
		}

		lastErr = err
		if attempt < attempts {
			slog.Warn("HTTP POST failed, retrying",
				"url", h.config.URL,
				"attempt", attempt,
				"error", err)
			time.Sleep(time.Second * time.Duration(attempt)) // Exponential backoff
		}
	}

	slog.Error("HTTP POST failed after all retries",
		"url", h.config.URL,
		"message_id", env.ID,
		"retries", h.config.Retries,
		"error", lastErr)

	return lastErr
}

// sendRequest performs a single HTTP request
func (h *HTTPOutput) sendRequest(ctx context.Context, env *envelope.Envelope) error {
	req, err := http.NewRequestWithContext(ctx, h.config.Method, h.config.URL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set content type and body based on payload
	if env.ContentType != "" {
		req.Header.Set("Content-Type", env.ContentType)
	} else {
		req.Header.Set("Content-Type", "text/plain")
	}

	// Use raw payload as request body
	req.Body = io.NopCloser(io.NewReader(env.Payload))
	req.ContentLength = int64(len(env.Payload))

	// Add X-Message-ID header for tracking
	req.Header.Set("X-Message-ID", env.ID)

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check for HTTP errors (4xx, 5xx)
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Close closes the HTTP client connection
func (h *HTTPOutput) Close() error {
	h.client.CloseIdleConnections()
	return nil
}
