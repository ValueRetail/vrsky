package io

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// HTTPInput listens for webhooks on a configured HTTP port
type HTTPInput struct {
	port      string
	server    *http.Server
	messages  chan *envelope.Envelope
	closeOnce sync.Once
	closed    bool
	mu        sync.Mutex
}

// NewHTTPInput creates a new HTTP input handler
func NewHTTPInput(configJSON json.RawMessage) (*HTTPInput, error) {
	var config struct {
		Port string `json:"port"`
	}

	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, fmt.Errorf("parse http config: %w", err)
	}

	if config.Port == "" {
		config.Port = "8000"
	}

	return &HTTPInput{
		port:     config.Port,
		messages: make(chan *envelope.Envelope, 100),
	}, nil
}

// Start begins listening for HTTP webhooks
func (h *HTTPInput) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", h.handleWebhook)

	h.server = &http.Server{
		Addr:    ":" + h.port,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		_ = h.server.Close()
	}()

	slog.Info("HTTP input started", "port", h.port, "endpoint", "POST /webhook")

	if err := h.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP server error", "error", err)
		return fmt.Errorf("http server: %w", err)
	}

	return nil
}

// handleWebhook processes incoming webhook requests
func (h *HTTPInput) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("Failed to read request body", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Wrap in envelope
	env, err := h.wrapPayloadInEnvelope(r, body)
	if err != nil {
		slog.Error("Failed to wrap payload", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Send to message channel (non-blocking, fire-and-forget)
	select {
	case h.messages <- env:
		slog.Info("Webhook queued", "id", env.ID)
	default:
		// Channel full, drop message (fire-and-forget philosophy)
		slog.Warn("Message channel full, dropping webhook", "id", env.ID)
	}

	// Return 202 Accepted immediately (fire-and-forget)
	w.WriteHeader(http.StatusAccepted)
}

// wrapPayloadInEnvelope creates an envelope from the webhook payload
func (h *HTTPInput) wrapPayloadInEnvelope(r *http.Request, body []byte) (*envelope.Envelope, error) {
	env := envelope.New()

	// Generate ID if not present
	if env.ID == "" {
		env.ID = uuid.New().String()
	}

	// Set payload
	env.Payload = body
	env.PayloadSize = int64(len(body))
	env.ContentType = r.Header.Get("Content-Type")
	if env.ContentType == "" {
		env.ContentType = "application/json"
	}
	env.Source = "http"

	// Extract source IP
	sourceIP := getClientIP(r)

	// Record step in history
	env.StepHistory = append(env.StepHistory, fmt.Sprintf("http-input:%s", sourceIP))

	slog.Info("Received webhook",
		"id", env.ID,
		"source_ip", sourceIP,
		"content_type", env.ContentType,
		"payload_size", env.PayloadSize,
	)

	return env, nil
}

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (proxy)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := xff
		if idx := len(ips) - 1; idx >= 0 {
			if ips[idx] == ' ' {
				ips = ips[:idx]
			}
		}
		return ips
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Use remote address
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

// Read returns the next envelope from the webhook channel
func (h *HTTPInput) Read(ctx context.Context) (*envelope.Envelope, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case env := <-h.messages:
		return env, nil
	}
}

// Close gracefully shuts down the HTTP server
func (h *HTTPInput) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil
	}

	h.closed = true

	if h.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.server.Shutdown(ctx)
	}

	return nil
}
