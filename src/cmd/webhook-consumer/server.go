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

// WebhookServer is the shared HTTP server that routes webhook requests to active connections
type WebhookServer struct {
	port    string
	service *WebhookConsumerService
	server  *http.Server
	logger  *slog.Logger
}

// NewWebhookServer creates a new webhook HTTP server
func NewWebhookServer(port string, service *WebhookConsumerService, logger *slog.Logger) *WebhookServer {
	return &WebhookServer{
		port:    port,
		service: service,
		logger:  logger,
	}
}

// Start begins listening for HTTP webhook requests
func (ws *WebhookServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/", ws.handleWebhook)
	mux.HandleFunc("/health", ws.handleHealth)

	ws.server = &http.Server{
		Addr:         ":" + ws.port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		ws.logger.Info("Webhook HTTP server started", "port", ws.port)
		if err := ws.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			ws.logger.Error("Webhook HTTP server error", "error", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server
func (ws *WebhookServer) Stop() {
	if ws.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ws.server.Shutdown(ctx)
	}
}

// handleHealth returns 200 OK for health checks
func (ws *WebhookServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleWebhook routes POST /webhook/{connectionId} to the appropriate handler
func (ws *WebhookServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// CORS headers for browser-based testing
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract connectionId from path: /webhook/{connectionId}
	path := strings.TrimPrefix(r.URL.Path, "/webhook/")
	connectionID := strings.TrimSuffix(path, "/")

	if connectionID == "" {
		http.Error(w, "Missing connection ID", http.StatusBadRequest)
		return
	}

	// Look up active connection
	ac := ws.service.getActiveConnection(connectionID)
	if ac == nil {
		http.Error(w, "Connection not found or not running", http.StatusNotFound)
		return
	}

	// Read request body
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		ws.logger.Error("Failed to read request body", "error", err)
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if len(body) == 0 {
		http.Error(w, "Empty request body", http.StatusBadRequest)
		return
	}

	// Create envelope
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	env := &envelope.Envelope{
		ID:            uuid.New().String(),
		TenantID:      ac.TenantID,
		IntegrationID: ac.ConnectionID,
		Payload:       body,
		PayloadSize:   int64(len(body)),
		ContentType:   contentType,
		Source:        "webhook",
		CurrentStep:   0,
		StepHistory:   []string{"webhook-consumer"},
		CreatedAt:     time.Now().UTC(),
	}

	// Serialize envelope
	data, err := json.Marshal(env)
	if err != nil {
		ws.logger.Error("Failed to marshal envelope", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Publish to NATS
	topic := fmt.Sprintf("vrsky.data.%s.pipeline.%s", ac.TenantID, ac.ConnectionID)
	if err := ws.service.nc.Publish(topic, data); err != nil {
		ws.logger.Error("Failed to publish to NATS", "error", err, "topic", topic)
		http.Error(w, "Failed to process webhook", http.StatusInternalServerError)
		return
	}

	ws.logger.Info("Webhook received and published",
		"connection_id", ac.ConnectionID,
		"tenant_id", ac.TenantID,
		"topic", topic,
		"payload_size", len(body),
		"envelope_id", env.ID)

	// Store last payload for tenant-consumer bridges
	_, _ = ws.service.db.Exec("UPDATE connections SET last_payload = $1 WHERE id = $2", data, ac.ConnectionID)

	// Return 202 Accepted
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"accepted","envelope_id":"%s"}`, env.ID)))
}
