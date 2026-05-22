package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// tunnelState tracks a running cloudflared quick tunnel
type tunnelState struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	publicURL string
	running   bool
}

// WebhookServer is the shared HTTP server that routes webhook requests to active connections
type WebhookServer struct {
	port    string
	service *WebhookConsumerService
	server  *http.Server
	logger  *slog.Logger
	tunnel  tunnelState
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
	mux.HandleFunc("/tunnel/start", ws.handleTunnelStart)
	mux.HandleFunc("/tunnel/stop", ws.handleTunnelStop)
	mux.HandleFunc("/tunnel/status", ws.handleTunnelStatus)
	mux.HandleFunc("/tunnel/register", ws.handleTunnelRegister)
	mux.HandleFunc("/sample-data/", ws.handleSampleData)
	mux.Handle("/metrics", promhttp.Handler())

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

// Stop gracefully shuts down the HTTP server and tunnel
func (ws *WebhookServer) Stop() {
	ws.stopTunnel()
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

	// HMAC signature verification (Phase 1B / #67). Skipped when the
	// connection has no signature block configured.
	if ac.Signature != nil {
		headerVal := r.Header.Get(ac.Signature.Header)
		if headerVal == "" {
			incSignatureFailure(ac.ConnectionID, "missing_header")
			ws.logger.Warn("Webhook signature header missing",
				"connection_id", ac.ConnectionID,
				"header", ac.Signature.Header)
			http.Error(w, "Missing signature header", http.StatusUnauthorized)
			return
		}
		if err := verifyHMAC(body, headerVal, *ac.Signature); err != nil {
			incSignatureFailure(ac.ConnectionID, classifySigErr(err))
			ws.logger.Warn("Webhook signature verification failed",
				"connection_id", ac.ConnectionID,
				"header", ac.Signature.Header,
				"reason", err.Error())
			http.Error(w, "Invalid signature", http.StatusUnauthorized)
			return
		}
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

	// Publish to JetStream (at-least-once delivery). The MsgID dedups
	// retries within JS's 5-minute window — prevents double-delivery if
	// the client retries after we publish but before responding 202.
	if err := ws.service.pub.Publish(r.Context(), ac.TenantID, ac.ConnectionID, env.ID, data); err != nil {
		ws.logger.Error("Failed to publish to JetStream", "error", err,
			"tenant", ac.TenantID, "connection", ac.ConnectionID)
		http.Error(w, "Failed to process webhook", http.StatusInternalServerError)
		return
	}

	ws.logger.Info("Webhook received and published",
		"connection_id", ac.ConnectionID,
		"tenant_id", ac.TenantID,
		"subject", fmt.Sprintf("vrsky.data.%s.pipeline.%s", ac.TenantID, ac.ConnectionID),
		"payload_size", len(body),
		"envelope_id", env.ID)

	// Store last payload for tenant-consumer bridges
	_, _ = ws.service.db.Exec("UPDATE connections SET last_payload = $1 WHERE id = $2", data, ac.ConnectionID)

	// Return 202 Accepted
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"accepted","envelope_id":"%s"}`, env.ID)))
}

// handleSampleData returns the last received webhook payload for a connection
func (ws *WebhookServer) handleSampleData(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Extract connectionId from path: /sample-data/{connectionId}
	path := strings.TrimPrefix(r.URL.Path, "/sample-data/")
	connectionID := strings.TrimSuffix(path, "/")

	if connectionID == "" {
		http.Error(w, `{"ok":false,"error":"Missing connection ID"}`, http.StatusBadRequest)
		return
	}

	// Read last_payload from DB — try exact connection first, then fall back to
	// most recent connection from the same tenant that has payload data
	var lastPayload []byte
	err := ws.service.db.QueryRow("SELECT last_payload FROM connections WHERE id = $1 AND last_payload IS NOT NULL", connectionID).Scan(&lastPayload)
	if err != nil || len(lastPayload) == 0 {
		// Fallback: find the most recent connection for the same tenant with payload
		err = ws.service.db.QueryRow(`
			SELECT c2.last_payload FROM connections c1
			JOIN connections c2 ON c2.tenant_id = c1.tenant_id AND c2.last_payload IS NOT NULL
			WHERE c1.id = $1
			ORDER BY c2.updated_at DESC LIMIT 1
		`, connectionID).Scan(&lastPayload)
	}
	if err != nil || len(lastPayload) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"No data received yet. Send a webhook first."}`))
		return
	}

	// last_payload is a serialized envelope — extract the payload field
	var env envelope.Envelope
	if err := json.Unmarshal(lastPayload, &env); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"Failed to parse stored payload"}`))
		return
	}

	// Try to parse the payload as JSON
	var parsed interface{}
	if err := json.Unmarshal(env.Payload, &parsed); err != nil {
		// Return raw string if not JSON
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]interface{}{"ok": true, "data": string(env.Payload)})
		_, _ = w.Write(resp)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp, _ := json.Marshal(map[string]interface{}{"ok": true, "data": parsed})
	_, _ = w.Write(resp)
}

// handleTunnelStart starts a cloudflared quick tunnel to make webhooks publicly accessible
func (ws *WebhookServer) handleTunnelStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	url, err := ws.ensureTunnel()
	if err != nil {
		ws.logger.Error("Failed to start tunnel", "error", err)
		http.Error(w, "Failed to start tunnel: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"started","url":"%s"}`, url)))
}

// handleTunnelStop stops the running cloudflared tunnel
func (ws *WebhookServer) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ws.stopTunnel()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"stopped"}`))
}

// handleTunnelStatus returns current tunnel state
func (ws *WebhookServer) handleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ws.tunnel.mu.Lock()
	running := ws.tunnel.running
	url := ws.tunnel.publicURL
	ws.tunnel.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"running":%t,"url":"%s"}`, running, url)))
}

// stopTunnel kills the cloudflared process
func (ws *WebhookServer) stopTunnel() {
	ws.tunnel.mu.Lock()
	defer ws.tunnel.mu.Unlock()

	if ws.tunnel.cmd != nil && ws.tunnel.cmd.Process != nil {
		_ = ws.tunnel.cmd.Process.Kill()
		_ = ws.tunnel.cmd.Wait()
	}
	ws.tunnel.cmd = nil
	ws.tunnel.publicURL = ""
	ws.tunnel.running = false
}

// ensureTunnel starts the tunnel if not running and returns the public URL.
// Blocks up to 15 seconds waiting for the URL.
func (ws *WebhookServer) ensureTunnel() (string, error) {
	ws.tunnel.mu.Lock()
	if ws.tunnel.running && ws.tunnel.publicURL != "" {
		url := ws.tunnel.publicURL
		ws.tunnel.mu.Unlock()
		return url, nil
	}
	ws.tunnel.mu.Unlock()

	// Start tunnel process
	cmd := exec.Command("cloudflared", "tunnel", "--url", fmt.Sprintf("http://localhost:%s", ws.port))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start cloudflared: %w", err)
	}

	ws.tunnel.mu.Lock()
	ws.tunnel.cmd = cmd
	ws.tunnel.running = true
	ws.tunnel.mu.Unlock()

	// Parse URL from cloudflared output
	urlCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			ws.logger.Debug("cloudflared", "line", line)
			if idx := strings.Index(line, "https://"); idx >= 0 {
				part := line[idx:]
				if end := strings.IndexAny(part, " \t\n"); end > 0 {
					part = part[:end]
				}
				if strings.Contains(part, "trycloudflare.com") {
					select {
					case urlCh <- part:
					default:
					}
				}
			}
		}
	}()

	select {
	case url := <-urlCh:
		ws.tunnel.mu.Lock()
		ws.tunnel.publicURL = url
		ws.tunnel.mu.Unlock()
		ws.logger.Info("Cloudflare tunnel started", "url", url)
		return url, nil
	case <-time.After(15 * time.Second):
		return "", fmt.Errorf("timed out waiting for tunnel URL")
	}
}

// handleTunnelRegister starts tunnel and optionally registers callback with external provider
func (ws *WebhookServer) handleTunnelRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var req struct {
		RegistrationURL string `json:"registration_url"`
		AuthToken       string `json:"auth_token"`
		CallbackPath    string `json:"callback_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Start tunnel
	tunnelURL, err := ws.ensureTunnel()
	if err != nil {
		ws.logger.Error("Failed to start tunnel", "error", err)
		http.Error(w, "Failed to start tunnel: "+err.Error(), http.StatusInternalServerError)
		return
	}

	callbackURL := tunnelURL + req.CallbackPath

	// If no registration URL, just return the tunnel URL
	if req.RegistrationURL == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"connected","tunnel_url":"%s","callback_url":"%s"}`, tunnelURL, callbackURL)))
		return
	}

	// Register callback with external provider
	regBody, _ := json.Marshal(map[string]string{"url": callbackURL})
	regReq, err := http.NewRequest("POST", req.RegistrationURL, bytes.NewReader(regBody))
	if err != nil {
		http.Error(w, "Invalid registration URL", http.StatusBadRequest)
		return
	}
	regReq.Header.Set("Content-Type", "application/json")
	if req.AuthToken != "" {
		regReq.Header.Set("Authorization", "Bearer "+req.AuthToken)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(regReq)
	if err != nil {
		ws.logger.Error("Failed to register callback", "error", err, "url", req.RegistrationURL)
		// Still return success since tunnel is running — registration just failed
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"tunnel_started","tunnel_url":"%s","callback_url":"%s","registration_error":"%s"}`,
			tunnelURL, callbackURL, err.Error())))
		return
	}
	defer resp.Body.Close()
	regRespBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))

	ws.logger.Info("Registered callback with provider",
		"registration_url", req.RegistrationURL,
		"callback_url", callbackURL,
		"status", resp.StatusCode)

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"registered","tunnel_url":"%s","callback_url":"%s","registration_status":%d,"registration_response":%s}`,
		tunnelURL, callbackURL, resp.StatusCode, string(regRespBody))))
}
