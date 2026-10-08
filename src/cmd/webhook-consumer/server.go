package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/idempotency"
	"github.com/ValueRetail/vrsky/pkg/tlsconfig"
	"github.com/google/uuid"
)

// HTTP handlers served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9100 in
// compose). Registered in Configure via RegisterHTTPHandler. /health and
// /metrics are served separately by the SDK on HEALTH_PORT (the signature-
// failure counter in metrics.go registers on the default Prometheus registry,
// which the SDK exposes there).

// tunnelState tracks a running cloudflared quick tunnel.
type tunnelState struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	publicURL string
	running   bool
}

// handleWebhook routes POST /webhook/{connectionId} to the active connection,
// verifies the HMAC signature (if configured), and publishes the body.
func (s *webhookConsumer) handleWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/webhook/")
		connectionID := strings.TrimSuffix(path, "/")
		if connectionID == "" {
			http.Error(w, "Missing connection ID", http.StatusBadRequest)
			return
		}

		ac := s.getActiveConnection(connectionID)
		if ac == nil {
			http.Error(w, "Connection not found or not running", http.StatusNotFound)
			return
		}

		// mTLS client-cert verification (#89 / Phase 3F). When the connection
		// configured a client CA, the caller must present a client cert that
		// chains to it. This also closes the plain-port bypass: a request over
		// the SDK aux HTTP port (no TLS) to an mTLS connection is rejected.
		if len(ac.ClientCA) > 0 {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				incClientCertFailure(ac.ConnectionID, "missing_cert")
				s.logger.Warn("Webhook requires mTLS but no client certificate presented",
					"connection_id", ac.ConnectionID)
				http.Error(w, "Client certificate required", http.StatusUnauthorized)
				return
			}
			if err := tlsconfig.VerifyClientCert(r.TLS.PeerCertificates, ac.ClientCA); err != nil {
				incClientCertFailure(ac.ConnectionID, "untrusted_cert")
				s.logger.Warn("Webhook client certificate verification failed",
					"connection_id", ac.ConnectionID, "reason", err.Error())
				http.Error(w, "Client certificate verification failed", http.StatusUnauthorized)
				return
			}
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB limit
		if err != nil {
			s.logger.Error("Failed to read request body", "error", err)
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
				s.logger.Warn("Webhook signature header missing",
					"connection_id", ac.ConnectionID, "header", ac.Signature.Header)
				http.Error(w, "Missing signature header", http.StatusUnauthorized)
				return
			}
			if err := verifyHMAC(body, headerVal, *ac.Signature); err != nil {
				incSignatureFailure(ac.ConnectionID, classifySigErr(err))
				s.logger.Warn("Webhook signature verification failed",
					"connection_id", ac.ConnectionID, "header", ac.Signature.Header, "reason", err.Error())
				http.Error(w, "Invalid signature", http.StatusUnauthorized)
				return
			}
		}

		contentType := r.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}

		// Idempotency-Key (plans/webhook-idempotency.md): a sender whose
		// outbox delivers at least once repeats the key on every retry.
		// Checked only after the signature, so an unsigned probe can neither
		// read nor plant keys. A repeat is acknowledged and not published; a
		// lookup that fails is logged and the request is treated as new (the
		// Nats-Msg-Id layer below still catches a repeat within five minutes).
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if len(key) > idempotency.MaxKeyLength {
			http.Error(w, fmt.Sprintf("Idempotency-Key longer than %d characters", idempotency.MaxKeyLength), http.StatusBadRequest)
			return
		}
		envID := uuid.New().String()
		if key != "" {
			if firstID, seen, err := s.keys.Seen(r.Context(), ac.TenantID, ac.ConnectionID, key); err != nil {
				s.logger.Warn("Idempotency key lookup failed; treating the request as new",
					"connection_id", ac.ConnectionID, "error", err)
			} else if seen {
				incReplayed(ac.ConnectionID)
				s.logger.Info("Webhook replayed; already published",
					"connection_id", ac.ConnectionID, "tenant_id", ac.TenantID, "envelope_id", firstID)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotency-Replayed", "true")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"accepted","envelope_id":"%s","replayed":true}`, firstID)))
				return
			}
			// The envelope id is the NATS message id: derived from the key it
			// is stable across retries, so JetStream drops a second publish of
			// the same request inside its dedup window — the second layer.
			envID = idempotencyEnvelopeID(ac.ConnectionID, key)
		}

		env := &envelope.Envelope{
			ID:            envID,
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
		if key != "" {
			env.Metadata = map[string]interface{}{"idempotency_key": key}
		}

		if s.publish == nil {
			http.Error(w, "Consumer not ready", http.StatusServiceUnavailable)
			return
		}
		// Publish via the SDK-injected closure (at-least-once; the MsgID dedups
		// retries within JS's 5-min window).
		if err := s.publish(r.Context(), env); err != nil {
			s.logger.Error("Failed to publish webhook", "error", err,
				"tenant", ac.TenantID, "connection", ac.ConnectionID)
			http.Error(w, "Failed to process webhook", http.StatusInternalServerError)
			return
		}

		// Remembered only now, after the publish: a failed publish answered
		// 5xx above and left nothing behind, so the sender's retry is a real
		// second attempt. A failed Remember is logged, not reported — the
		// message is on the stream, and a retry is what the next layer is for.
		if key != "" {
			if err := s.keys.Remember(r.Context(), ac.TenantID, ac.ConnectionID, key, env.ID); err != nil {
				s.logger.Warn("Could not remember idempotency key", "connection_id", ac.ConnectionID, "error", err)
			}
		}

		s.logger.Info("Webhook received and published",
			"connection_id", ac.ConnectionID,
			"tenant_id", ac.TenantID,
			"payload_size", len(body),
			"envelope_id", env.ID)

		// last_payload (the UI's data-structure preview) is written by the SDK's
		// publish closure: tenant-scoped and throttled to one write per
		// connection per 30 s. The unscoped write this handler used to make on
		// top of it is gone (plans/hygiene-fixes.md).

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"accepted","envelope_id":"%s"}`, env.ID)))
	}
}

// handleSampleData returns the last received webhook payload for a connection.
func (s *webhookConsumer) handleSampleData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/sample-data/")
		connectionID := strings.TrimSuffix(path, "/")
		if connectionID == "" {
			http.Error(w, `{"ok":false,"error":"Missing connection ID"}`, http.StatusBadRequest)
			return
		}

		// Read last_payload from DB — try exact connection first, then fall back to
		// most recent connection from the same tenant that has payload data.
		var lastPayload []byte
		// lint:tenant-ok — this endpoint is cluster-internal (the ingress only
		// routes /webhook) and the connection id is the key; the fallback below
		// scopes by that row's own tenant.
		err := s.db.QueryRowContext(r.Context(), `SELECT last_payload FROM connections WHERE id = $1 AND last_payload IS NOT NULL`, connectionID).Scan(&lastPayload)
		if err != nil || len(lastPayload) == 0 {
			err = s.db.QueryRowContext(r.Context(), `
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

		// last_payload is a serialized envelope — extract the payload field.
		var env envelope.Envelope
		if err := json.Unmarshal(lastPayload, &env); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":false,"error":"Failed to parse stored payload"}`))
			return
		}

		var parsed interface{}
		w.Header().Set("Content-Type", "application/json")
		if err := json.Unmarshal(env.Payload, &parsed); err != nil {
			resp, _ := json.Marshal(map[string]interface{}{"ok": true, "data": string(env.Payload)})
			_, _ = w.Write(resp)
			return
		}
		resp, _ := json.Marshal(map[string]interface{}{"ok": true, "data": parsed})
		_, _ = w.Write(resp)
	}
}

// --- cloudflared quick tunnel control ---

func (s *webhookConsumer) handleTunnelStart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		url, err := s.ensureTunnel()
		if err != nil {
			s.logger.Error("Failed to start tunnel", "error", err)
			http.Error(w, "Failed to start tunnel: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"started","url":"%s"}`, url)))
	}
}

func (s *webhookConsumer) handleTunnelStop() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.stopTunnel()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"stopped"}`))
	}
}

func (s *webhookConsumer) handleTunnelStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.tunnel.mu.Lock()
		running := s.tunnel.running
		url := s.tunnel.publicURL
		s.tunnel.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"running": running, "url": url})
	}
}

// stopTunnel kills the cloudflared process.
func (s *webhookConsumer) stopTunnel() {
	s.tunnel.mu.Lock()
	defer s.tunnel.mu.Unlock()

	if s.tunnel.cmd != nil && s.tunnel.cmd.Process != nil {
		_ = s.tunnel.cmd.Process.Kill()
		_ = s.tunnel.cmd.Wait()
	}
	s.tunnel.cmd = nil
	s.tunnel.publicURL = ""
	s.tunnel.running = false
}

// quickTunnelRe matches a cloudflared quick-tunnel public URL
// (https://<name>.trycloudflare.com) — no path, no trailing punctuation.
var quickTunnelRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// ensureTunnel starts the tunnel if not running and returns the public URL.
// Blocks up to 15 seconds waiting for the URL. The tunnel forwards to the SDK
// auxiliary HTTP port where /webhook is served.
func (s *webhookConsumer) ensureTunnel() (string, error) {
	s.tunnel.mu.Lock()
	if s.tunnel.running && s.tunnel.publicURL != "" {
		url := s.tunnel.publicURL
		s.tunnel.mu.Unlock()
		return url, nil
	}
	s.tunnel.mu.Unlock()

	cmd := exec.Command("cloudflared", "tunnel", "--url", fmt.Sprintf("http://localhost:%s", s.auxPort))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start cloudflared: %w", err)
	}

	s.tunnel.mu.Lock()
	s.tunnel.cmd = cmd
	s.tunnel.running = true
	s.tunnel.mu.Unlock()

	urlCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			s.logger.Debug("cloudflared", "line", line)
			// cloudflared logs the public URL (https://<name>.trycloudflare.com)
			// AND its api.trycloudflare.com control endpoint; match the former
			// and skip the latter. The regex also drops any trailing log
			// punctuation (e.g. a quote) that previously produced a broken URL.
			if m := quickTunnelRe.FindString(line); m != "" && !strings.HasPrefix(m, "https://api.") {
				select {
				case urlCh <- m:
				default:
				}
			}
		}
	}()

	select {
	case url := <-urlCh:
		s.tunnel.mu.Lock()
		s.tunnel.publicURL = url
		s.tunnel.mu.Unlock()
		s.logger.Info("Cloudflare tunnel started", "url", url)
		return url, nil
	case <-time.After(15 * time.Second):
		return "", fmt.Errorf("timed out waiting for tunnel URL")
	}
}

// handleTunnelRegister starts the tunnel and optionally registers the callback
// URL with an external provider.
func (s *webhookConsumer) handleTunnelRegister() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		tunnelURL, err := s.ensureTunnel()
		if err != nil {
			s.logger.Error("Failed to start tunnel", "error", err)
			http.Error(w, "Failed to start tunnel: "+err.Error(), http.StatusInternalServerError)
			return
		}

		callbackURL := tunnelURL + req.CallbackPath

		if req.RegistrationURL == "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"connected","tunnel_url":"%s","callback_url":"%s"}`, tunnelURL, callbackURL)))
			return
		}

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
			s.logger.Error("Failed to register callback", "error", err, "url", req.RegistrationURL)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"tunnel_started","tunnel_url":"%s","callback_url":"%s","registration_error":"%s"}`,
				tunnelURL, callbackURL, err.Error())))
			return
		}
		defer resp.Body.Close()
		regRespBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))

		s.logger.Info("Registered callback with provider",
			"registration_url", req.RegistrationURL, "callback_url", callbackURL, "status", resp.StatusCode)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"registered","tunnel_url":"%s","callback_url":"%s","registration_status":%d,"registration_response":%s}`,
			tunnelURL, callbackURL, resp.StatusCode, string(regRespBody))))
	}
}

// idempotencyNamespace is the UUID namespace for envelope ids derived from an
// Idempotency-Key. Fixed, so the same key on the same connection always maps
// to the same id — on every replica and across restarts.
var idempotencyNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("vrsky-webhook-idempotency"))

// idempotencyEnvelopeID is the envelope (and NATS message) id for a keyed
// request: a UUID v5 of the connection and the key.
func idempotencyEnvelopeID(connectionID, key string) string {
	return uuid.NewSHA1(idempotencyNamespace, []byte(connectionID+"\x00"+key)).String()
}
