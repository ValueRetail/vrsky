package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	iolib "io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthtoken"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/tlsconfig"
)

// httpProducer delivers pipeline envelopes to external HTTP endpoints. It is a
// connector built on the SDK: the runner owns NATS/JetStream, the durable
// subscription, the health server, signal handling and graceful shutdown; this
// type implements only Configure (DB + caches + the /events SSE API) and
// Deliver (send one envelope to every matching HTTP node).
type httpProducer struct {
	sdk.BaseProducer

	db     *sql.DB
	logger *slog.Logger

	// Cache for connection HTTP configs (multiple producer nodes per connection).
	configCache     map[string][]*HTTPConfig
	configCacheMu   sync.RWMutex
	configCacheTime map[string]time.Time
	configCacheTTL  time.Duration

	// SSE event subscribers + recent-event buffer for replay on connect.
	eventSubs      map[string][]chan HTTPEvent
	eventSubsMu    sync.RWMutex
	recentEvents   map[string][]HTTPEvent
	recentEventsMu sync.RWMutex

	// OAuth output (#97): resolve a grant's access token for nodes with
	// auth_type=oauth. resolveToken is defaulted in Configure to the oauthtoken
	// client; tests inject a stub.
	tokens       *oauthtoken.Client
	resolveToken func(ctx context.Context, tenantID, grantID string, force bool) (string, error)
}

type HTTPEvent struct {
	Type       string `json:"type"` // "sent", "error", "info"
	Message    string `json:"message,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Time       string `json:"time"`
	Payload    string `json:"payload,omitempty"`  // request body (truncated)
	Response   string `json:"response,omitempty"` // response body (truncated)
}

type HTTPConfig struct {
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	AuthType       string            `json:"auth_type"`      // "", "none", or "oauth"
	OAuthGrantID   string            `json:"oauth_grant_id"` // grant id when auth_type=oauth
	PredecessorID  string
	PredIsConsumer bool

	// mTLS (#89): when the node carries a "tls" block with a resolved client
	// cert/key, client is a pre-built *http.Client presenting that cert to the
	// endpoint. nil means use the default (non-mTLS) client.
	client *http.Client
}

func main() {
	if err := sdk.RunProducer(context.Background(), "http-producer", &httpProducer{}); err != nil {
		slog.Error("http-producer exited", "error", err)
		os.Exit(1)
	}
}

// Configure wires the producer's dependencies. Called once by the runner before
// the subscription starts.
func (p *httpProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("http-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	p.configCache = make(map[string][]*HTTPConfig)
	p.configCacheTime = make(map[string]time.Time)
	if p.configCacheTTL == 0 {
		p.configCacheTTL = 5 * time.Minute
	}
	p.eventSubs = make(map[string][]chan HTTPEvent)
	p.recentEvents = make(map[string][]HTTPEvent)

	// OAuth output (#97): resolve access tokens for nodes with auth_type=oauth.
	p.tokens = oauthtoken.New(os.Getenv("MGMT_API_URL"), os.Getenv("OAUTH_TOKEN_SERVICE_SECRET"))
	if p.resolveToken == nil {
		p.resolveToken = func(ctx context.Context, tenantID, grantID string, force bool) (string, error) {
			if !p.tokens.Configured() {
				return "", errors.New("node uses OAuth but token resolution is not configured (set MGMT_API_URL + OAUTH_TOKEN_SERVICE_SECRET)")
			}
			if force {
				return p.tokens.ForceToken(ctx, tenantID, grantID)
			}
			return p.tokens.Token(ctx, tenantID, grantID)
		}
	}

	// Serve the live SSE event stream on the SDK auxiliary HTTP port
	// (WORKER_HTTP_PORT, 9400 in compose) via the custom-handler hook — the UI
	// connects to /events/{connectionID}.
	p.RegisterHTTPHandler("/events/", p.eventsHandler())

	p.logger.Info("http-producer configured")
	return nil
}

// Deliver sends one envelope to every matching HTTP-producer node configured
// for its connection. A transient failure (network error, 5xx, 429) returns
// sdk.Retriable (the SDK NAKs → retries → DLQs); 4xx / malformed-request errors
// are logged-and-acked (retrying can't help). A missing producer config for the
// connection is not an error — this binary just isn't the producer for it.
func (p *httpProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connectionID := env.IntegrationID
	if connectionID == "" {
		// No routing key — nothing this producer can do with it.
		return nil
	}

	httpConfigs, err := p.getHTTPConfigs(ctx, connectionID, env.TenantID)
	if err != nil {
		// Not an HTTP producer for this pipeline — ack and move on.
		p.logger.Debug("No HTTP producer config for connection", "connection_id", connectionID, "error", err)
		return nil
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	var transient error
	for _, httpCfg := range httpConfigs {
		if httpCfg.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !httpCfg.PredIsConsumer && httpCfg.PredecessorID != "" && lastProcessedBy != httpCfg.PredecessorID {
			continue
		}
		if err := p.sendHTTPRequest(ctx, connectionID, httpCfg, env); err != nil && transient == nil {
			transient = err
		}
	}
	if transient != nil {
		return sdk.Retriable(transient)
	}
	return nil
}

// sendHTTPRequest returns nil on a successful 2xx response or a non-retriable
// 4xx, and a non-nil error on any *retriable* failure (network error, 5xx,
// 429). 4xx responses return nil so the SDK doesn't retry them — the request is
// malformed and will keep failing on every redelivery.
func (p *httpProducer) sendHTTPRequest(ctx context.Context, connectionID string, httpCfg *HTTPConfig, env *envelope.Envelope) error {
	payloadPreview := string(env.Payload)
	if len(payloadPreview) > 2000 {
		payloadPreview = payloadPreview[:2000] + "..."
	}

	p.emitEvent(connectionID, HTTPEvent{
		Type:    "info",
		Message: fmt.Sprintf("Sending %d bytes to %s", len(env.Payload), httpCfg.URL),
		Payload: payloadPreview,
		Time:    now(),
	})

	method := httpCfg.Method
	if method == "" {
		method = "POST"
	}

	// Bad URL won't improve on retry — drop early (validated once).
	if _, err := http.NewRequestWithContext(ctx, method, httpCfg.URL, nil); err != nil {
		p.emitEvent(connectionID, HTTPEvent{
			Type: "error", Message: "Failed to create request: " + err.Error(),
			Time: now(),
		})
		return nil
	}

	// otelhttp transport makes the outbound call a child span of the pipeline
	// trace and injects traceparent into the external request. No-op when
	// tracing is disabled. When the node configured mTLS, httpCfg.client is a
	// pre-built client that presents the client cert; otherwise use the default.
	client := httpCfg.client
	if client == nil {
		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		}
	}
	// buildAndSend creates a fresh request (re-readable body) per attempt and,
	// for auth_type=oauth, attaches a Bearer token. force=true refreshes it.
	buildAndSend := func(force bool) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, method, httpCfg.URL, bytes.NewReader(env.Payload))
		if env.ContentType != "" {
			req.Header.Set("Content-Type", env.ContentType)
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Message-ID", env.ID)
		for k, v := range httpCfg.Headers {
			req.Header.Set(k, v)
		}
		if httpCfg.AuthType == "oauth" && httpCfg.OAuthGrantID == "" {
			// OAuth selected but no grant (e.g. the grant was revoked, which
			// clears the selection on the node). Rather than blocking the send
			// — or calling /oauth/grants//token with an empty id and getting an
			// opaque 500 — fall back to sending unauthenticated and warn. A
			// proper "reconnect required" hint in the UI is tracked in #98.
			p.logger.Warn("OAuth output has no grant selected; sending without authentication (reconnect the grant to authenticate)",
				"connection_id", connectionID, "url", httpCfg.URL)
		} else if httpCfg.AuthType == "oauth" {
			tok, terr := p.resolveToken(ctx, env.TenantID, httpCfg.OAuthGrantID, force)
			if terr != nil {
				return nil, fmt.Errorf("resolve oauth token: %w", terr)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		return client.Do(req)
	}

	resp, err := buildAndSend(false)
	if err != nil {
		p.logger.Error("HTTP request failed", "error", err, "connection_id", connectionID)
		p.emitEvent(connectionID, HTTPEvent{
			Type: "error", Message: err.Error(),
			Time: now(),
		})
		return fmt.Errorf("transport: %w", err) // retriable (transport or token-service hiccup)
	}

	// OAuth: an access token may have expired between resolution and the call —
	// refresh once and retry (mirrors api-consumer's callEndpoint).
	if resp.StatusCode == http.StatusUnauthorized && httpCfg.AuthType == "oauth" && httpCfg.OAuthGrantID != "" {
		_ = resp.Body.Close()
		p.emitEvent(connectionID, HTTPEvent{Type: "info", Message: "401 — refreshing OAuth token and retrying", Time: now()})
		resp, err = buildAndSend(true)
		if err != nil {
			p.emitEvent(connectionID, HTTPEvent{Type: "error", Message: err.Error(), Time: now()})
			return fmt.Errorf("after token refresh: %w", err) // retriable
		}
	}
	defer resp.Body.Close()

	respBody, _ := iolib.ReadAll(iolib.LimitReader(resp.Body, 4096))
	respPreview := string(respBody)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		p.logger.Info("HTTP request sent", "connection_id", connectionID, "status", resp.StatusCode, "size", len(env.Payload))
		p.emitEvent(connectionID, HTTPEvent{
			Type: "sent", Message: fmt.Sprintf("%s %s → %d", method, httpCfg.URL, resp.StatusCode),
			StatusCode: resp.StatusCode, Payload: payloadPreview, Response: respPreview,
			Time: now(),
		})
		return nil
	}

	p.logger.Error("HTTP request returned error", "status", resp.StatusCode, "connection_id", connectionID)
	p.emitEvent(connectionID, HTTPEvent{
		Type: "error", Message: fmt.Sprintf("%s %s → %d", method, httpCfg.URL, resp.StatusCode),
		StatusCode: resp.StatusCode, Response: respPreview,
		Time: now(),
	})
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return fmt.Errorf("upstream %d", resp.StatusCode) // retriable
	}
	return nil // 4xx — don't retry
}

// getHTTPConfigs returns all HTTP-producer node configs for a connection (with
// a short cache). // lint:tenant-ok — connection lookup by PK; tenant scoping is enforced upstream when the pipeline is deployed.
func (p *httpProducer) getHTTPConfigs(ctx context.Context, connectionID, tenantID string) ([]*HTTPConfig, error) {
	p.configCacheMu.RLock()
	if cfg, ok := p.configCache[connectionID]; ok {
		if time.Since(p.configCacheTime[connectionID]) < p.configCacheTTL {
			p.configCacheMu.RUnlock()
			return cfg, nil
		}
	}
	p.configCacheMu.RUnlock()

	var nodesJSON, edgesJSON []byte
	err := p.db.QueryRowContext(ctx, `SELECT nodes, edges FROM connections WHERE id = $1`, connectionID).Scan(&nodesJSON, &edgesJSON)
	if err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
	}

	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("failed to parse nodes: %w", err)
	}

	// Decrypt any *_secret_id references (e.g. an encrypted auth-header value)
	// so the typed config below sees plaintext.
	reader := crypto.NewSQLSecretReader(p.db)
	for i := range nodes {
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, nodes[i].Config)
		if rerr != nil {
			return nil, fmt.Errorf("resolve secrets for node %s: %w", nodes[i].ID, rerr)
		}
		nodes[i].Config = resolved
	}

	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	var configs []*HTTPConfig
	for _, node := range nodes {
		if node.Type != "producer" {
			continue
		}

		var nodeConfig struct {
			Type string `json:"type"`
			HTTP struct {
				URL          string            `json:"url"`
				Method       string            `json:"method"`
				Headers      map[string]string `json:"headers"`
				AuthType     string            `json:"auth_type"`
				OAuthGrantID string            `json:"oauth_grant_id"`
			} `json:"http"`
			// mTLS material (#89). In the stored config these are *_secret_id
			// refs; crypto.ResolveSecretsInJSON has already replaced them with
			// plaintext PEM by the time we parse here.
			TLS tlsconfig.NodeConfig `json:"tls"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			continue
		}
		if nodeConfig.Type != "http" || nodeConfig.HTTP.URL == "" {
			continue
		}

		var predID string
		var predIsConsumer bool
		for _, e := range edges {
			if e.Target == node.ID {
				predID = e.Source
				for _, n := range nodes {
					if n.ID == predID && n.Type == "consumer" {
						predIsConsumer = true
						break
					}
				}
				break
			}
		}

		cfg := &HTTPConfig{
			URL:            nodeConfig.HTTP.URL,
			Method:         nodeConfig.HTTP.Method,
			Headers:        nodeConfig.HTTP.Headers,
			AuthType:       nodeConfig.HTTP.AuthType,
			OAuthGrantID:   nodeConfig.HTTP.OAuthGrantID,
			PredecessorID:  predID,
			PredIsConsumer: predIsConsumer,
		}

		// mTLS: when the node presents a client cert, pre-build a dedicated
		// http.Client that presents it (and trusts the configured server CA, if
		// any). Built once here and reused for every send to this node.
		if nodeConfig.TLS.Enabled() {
			tlsCfg, err := tlsconfig.ClientConfig(
				[]byte(nodeConfig.TLS.Cert),
				[]byte(nodeConfig.TLS.Key),
				[]byte(nodeConfig.TLS.ClientCA),
			)
			if err != nil {
				return nil, fmt.Errorf("node %s: build mTLS client: %w", node.ID, err)
			}
			// Clone DefaultTransport so proxy support, dial/idle timeouts and
			// HTTP/2 settings are preserved; only swap in the mTLS config.
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.TLSClientConfig = tlsCfg
			cfg.client = &http.Client{
				Timeout:   30 * time.Second,
				Transport: otelhttp.NewTransport(transport),
			}
		}

		configs = append(configs, cfg)
	}

	if len(configs) == 0 {
		return nil, fmt.Errorf("no HTTP producer config found")
	}

	p.configCacheMu.Lock()
	p.configCache[connectionID] = configs
	p.configCacheTime[connectionID] = time.Now()
	p.configCacheMu.Unlock()

	return configs, nil
}

// --- Event broadcasting (SSE) ---

func (p *httpProducer) subscribeEvents(connectionID string) (chan HTTPEvent, func()) {
	ch := make(chan HTTPEvent, 50)
	p.eventSubsMu.Lock()
	p.eventSubs[connectionID] = append(p.eventSubs[connectionID], ch)
	p.eventSubsMu.Unlock()

	return ch, func() {
		p.eventSubsMu.Lock()
		defer p.eventSubsMu.Unlock()
		subs := p.eventSubs[connectionID]
		for i, sub := range subs {
			if sub == ch {
				p.eventSubs[connectionID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}
}

func (p *httpProducer) emitEvent(connectionID string, event HTTPEvent) {
	p.recentEventsMu.Lock()
	p.recentEvents[connectionID] = append(p.recentEvents[connectionID], event)
	if len(p.recentEvents[connectionID]) > 50 {
		p.recentEvents[connectionID] = p.recentEvents[connectionID][len(p.recentEvents[connectionID])-50:]
	}
	p.recentEventsMu.Unlock()

	p.eventSubsMu.RLock()
	defer p.eventSubsMu.RUnlock()
	for _, ch := range p.eventSubs[connectionID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (p *httpProducer) getRecentEvents(connectionID string) []HTTPEvent {
	p.recentEventsMu.RLock()
	defer p.recentEventsMu.RUnlock()
	events := p.recentEvents[connectionID]
	cp := make([]HTTPEvent, len(events))
	copy(cp, events)
	return cp
}

// eventsHandler returns the /events/{connectionID} SSE handler, served on the
// SDK auxiliary HTTP port.
func (p *httpProducer) eventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		connectionID := strings.TrimPrefix(r.URL.Path, "/events/")
		connectionID = strings.TrimSuffix(connectionID, "/")
		if connectionID == "" {
			http.Error(w, "Missing connection ID", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch, unsub := p.subscribeEvents(connectionID)
		defer unsub()

		fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Listening for HTTP producer events\"}\n\n")
		flusher.Flush()

		// Replay recent events so the client catches up.
		for _, event := range p.getRecentEvents(connectionID) {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				data, _ := json.Marshal(event)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

// --- Helpers ---

func now() string { return time.Now().UTC().Format(time.RFC3339) }
