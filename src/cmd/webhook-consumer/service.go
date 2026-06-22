package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/tlsconfig"
	"github.com/nats-io/nats.go"
)

// webhookConsumer receives inbound HTTP webhooks per active connection and
// publishes their bodies into the pipeline. It is an SDK Consumer: the runner
// provides NATS/DB/health/lifecycle; this type implements Configure (wire deps
// + register HTTP handlers), Run (subscribe to command subjects, block), and
// Stop (cancel connections, kill the tunnel).
type webhookConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc // injected by the runner; the one data-emit path
	logger  *slog.Logger

	// auxPort is the SDK auxiliary HTTP port (WORKER_HTTP_PORT) that /webhook is
	// served on; the cloudflared tunnel forwards to it.
	auxPort string

	// mTLS (#89): a dedicated TLS listener for high-security connections that
	// require an inbound client certificate. Empty mtlsPort disables it. The
	// listener demands *a* client cert at the handshake (RequireAnyClientCert);
	// handleWebhook then verifies the cert chains to the per-connection client CA.
	mtlsPort   string
	mtlsServer *http.Server

	// Active connections: connectionId → connection info
	activeConnections map[string]*ActiveConnection
	mu                sync.RWMutex

	// tunnel tracks the on-demand cloudflared quick tunnel.
	tunnel tunnelState

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// ActiveConnection tracks a registered webhook connection
type ActiveConnection struct {
	ConnectionID string
	TenantID     string
	Cancel       context.CancelFunc

	// Signature, when non-nil, makes the webhook reject any request whose
	// HMAC signature header does not match cfg.Secret. Populated at start
	// time from the connection's http.signature config (#67 / Phase 1B).
	Signature *signatureConfig

	// ClientCA, when non-empty, makes the webhook require a client certificate
	// that chains to this CA (PEM). Populated at start time from the
	// connection's tls.client_ca config (#89 / Phase 3F). Requests that present
	// no client cert — or one signed by a different CA — are rejected 401.
	ClientCA []byte
}

// Configure wires dependencies and registers the HTTP endpoints the UI uses
// (served on the SDK auxiliary HTTP port, WORKER_HTTP_PORT/9100). Called once
// before Run.
func (s *webhookConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("webhook-consumer requires DATABASE_URL")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.activeConnections = make(map[string]*ActiveConnection)
	s.auxPort = envOr("WORKER_HTTP_PORT", "9100")
	s.mtlsPort = os.Getenv("WORKER_MTLS_PORT") // empty → mTLS listener disabled

	s.RegisterHTTPHandler("/webhook/", s.handleWebhook())
	s.RegisterHTTPHandler("/sample-data/", s.handleSampleData())
	s.RegisterHTTPHandler("/tunnel/start", s.handleTunnelStart())
	s.RegisterHTTPHandler("/tunnel/stop", s.handleTunnelStop())
	s.RegisterHTTPHandler("/tunnel/status", s.handleTunnelStatus())
	s.RegisterHTTPHandler("/tunnel/register", s.handleTunnelRegister())

	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until the runner
// cancels ctx. Webhook delivery is driven by inbound HTTP requests.
func (s *webhookConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish
	s.logger.Info("Starting Webhook Consumer Service")

	if err := s.startMTLSListener(); err != nil {
		return fmt.Errorf("failed to start mTLS listener: %w", err)
	}

	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to start commands: %w", err)
	}
	s.startSub = startSub

	stopSub, err := s.nc.Subscribe("vrsky.commands.*.connection.stop", s.handleStopCommand)
	if err != nil {
		return fmt.Errorf("failed to subscribe to stop commands: %w", err)
	}
	s.stopSub = stopSub

	s.logger.Info("Subscribed to NATS command topics")
	<-ctx.Done()
	return nil
}

// Stop unregisters all webhooks and kills the cloudflared tunnel. The SDK runner
// calls this after Run returns; it also shuts down the aux HTTP server it owns.
func (s *webhookConsumer) Stop(ctx context.Context) error {
	s.logger.Info("Stopping Webhook Consumer Service")

	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}

	s.mu.Lock()
	for connId, ac := range s.activeConnections {
		s.logger.Info("Unregistering webhook", "connection_id", connId)
		ac.Cancel()
	}
	s.activeConnections = make(map[string]*ActiveConnection)
	s.mu.Unlock()

	s.stopTunnel()

	if s.mtlsServer != nil {
		_ = s.mtlsServer.Shutdown(ctx)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}

// startMTLSListener brings up a dedicated TLS listener on WORKER_MTLS_PORT that
// serves /webhook and demands a client certificate at the handshake
// (RequireAnyClientCert). Per-connection client-CA enforcement happens in
// handleWebhook. No-op when WORKER_MTLS_PORT is unset. The server identity comes
// from WEBHOOK_TLS_CERT_FILE/_KEY_FILE, or a generated self-signed cert (dev).
func (s *webhookConsumer) startMTLSListener() error {
	if s.mtlsPort == "" {
		return nil
	}

	certPEM, keyPEM, err := s.loadServerCert()
	if err != nil {
		return fmt.Errorf("load mTLS server cert: %w", err)
	}
	tlsCfg, err := tlsconfig.ServerConfig(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("build mTLS server config: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/", s.handleWebhook())
	s.mtlsServer = &http.Server{Addr: ":" + s.mtlsPort, Handler: mux, TLSConfig: tlsCfg}

	ln, err := tls.Listen("tcp", s.mtlsServer.Addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("listen on mTLS port %s: %w", s.mtlsPort, err)
	}
	go func() {
		s.logger.Info("mTLS webhook listener started", "port", s.mtlsPort)
		if serr := s.mtlsServer.Serve(ln); serr != nil && serr != http.ErrServerClosed {
			s.logger.Error("mTLS listener stopped", "error", serr)
		}
	}()
	return nil
}

// loadServerCert returns the mTLS listener's server cert/key as PEM. It reads
// WEBHOOK_TLS_CERT_FILE/_KEY_FILE when both are set; otherwise it generates a
// throwaway self-signed cert (dev fallback). mTLS security comes from verifying
// the inbound *client* cert, not from the client trusting this server cert.
func (s *webhookConsumer) loadServerCert() ([]byte, []byte, error) {
	certFile := os.Getenv("WEBHOOK_TLS_CERT_FILE")
	keyFile := os.Getenv("WEBHOOK_TLS_KEY_FILE")
	if certFile != "" && keyFile != "" {
		cert, err := os.ReadFile(certFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read cert file: %w", err)
		}
		key, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read key file: %w", err)
		}
		return cert, key, nil
	}
	s.logger.Warn("WEBHOOK_TLS_CERT_FILE/_KEY_FILE not set; generating self-signed mTLS server cert (dev only)")
	return tlsconfig.SelfSignedServer("webhook-consumer")
}

// CommandMessage represents a start/stop command from NATS
type CommandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

// handleStartCommand processes a start command from NATS
func (s *webhookConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse start command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received start command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	s.mu.RLock()
	_, exists := s.activeConnections[cmd.ConnectionID]
	s.mu.RUnlock()
	if exists {
		s.logger.Warn("Webhook already registered", "connection_id", cmd.ConnectionID)
		return
	}

	conn, err := s.getConnection(cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		s.logger.Error("Failed to fetch connection", "error", err, "connection_id", cmd.ConnectionID)
		return
	}

	if !s.hasWebhookConsumer(conn) {
		s.logger.Debug("Not a webhook consumer, ignoring", "connection_id", cmd.ConnectionID)
		return
	}

	_, cancel := context.WithCancel(context.Background())

	// Extract optional HMAC signature config. resolveSecretsInNodes has already
	// turned <field>_secret_id refs into plaintext, so the secret is sitting in
	// node.config.http.signature.secret.
	sig := s.extractSignature(conn)

	// Extract optional per-connection mTLS client CA (#89). When set, the webhook
	// requires an inbound client cert that chains to it.
	clientCA := s.extractClientCA(conn)
	if len(clientCA) > 0 && s.mtlsPort == "" {
		s.logger.Warn("Connection requires mTLS but WORKER_MTLS_PORT is unset; client-cert checks only apply to TLS-port requests",
			"connection_id", cmd.ConnectionID)
	}

	s.mu.Lock()
	s.activeConnections[cmd.ConnectionID] = &ActiveConnection{
		ConnectionID: cmd.ConnectionID,
		TenantID:     cmd.TenantID,
		Cancel:       cancel,
		Signature:    sig,
		ClientCA:     clientCA,
	}
	s.mu.Unlock()

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	s.logger.Info("Webhook registered",
		"connection_id", cmd.ConnectionID,
		"path", fmt.Sprintf("/webhook/%s", cmd.ConnectionID))
}

// handleStopCommand processes a stop command from NATS
func (s *webhookConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("Failed to parse stop command", "error", err, "data", string(msg.Data))
		return
	}

	s.logger.Info("Received stop command", "connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	s.mu.Lock()
	ac, exists := s.activeConnections[cmd.ConnectionID]
	if exists {
		ac.Cancel()
		delete(s.activeConnections, cmd.ConnectionID)
	}
	s.mu.Unlock()

	if !exists {
		s.logger.Warn("Webhook not registered", "connection_id", cmd.ConnectionID)
		return
	}

	if err := s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped"); err != nil {
		s.logger.Error("Failed to update connection status", "error", err)
	}

	s.logger.Info("Webhook unregistered", "connection_id", cmd.ConnectionID)
}

// getActiveConnection returns the active connection info for a given connection ID
func (s *webhookConsumer) getActiveConnection(connectionID string) *ActiveConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeConnections[connectionID]
}

// Connection represents a pipeline connection from the database
type Connection struct {
	ID       string
	TenantID string
	Name     string
	Nodes    json.RawMessage
	Edges    json.RawMessage
}

// getConnection fetches a connection and resolves any *_secret_id references in
// its node configs to plaintext (e.g. the HMAC signing secret).
func (s *webhookConsumer) getConnection(connectionID, tenantID string) (*Connection, error) {
	var conn Connection
	err := s.db.QueryRow(
		`SELECT id, tenant_id, name, nodes, edges FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&conn.ID, &conn.TenantID, &conn.Name, &conn.Nodes, &conn.Edges)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("connection not found: %s", connectionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query connection: %w", err)
	}

	resolved, rerr := s.resolveSecretsInNodes(conn.Nodes, tenantID)
	if rerr != nil {
		return nil, fmt.Errorf("resolve secrets: %w", rerr)
	}
	conn.Nodes = resolved
	return &conn, nil
}

// resolveSecretsInNodes parses nodes[], runs the resolver on each config,
// re-marshals. Returns the original bytes on parse failure.
func (s *webhookConsumer) resolveSecretsInNodes(nodesJSON json.RawMessage, tenantID string) (json.RawMessage, error) {
	if len(nodesJSON) == 0 {
		return nodesJSON, nil
	}
	var nodes []map[string]any
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nodesJSON, err
	}
	reader := crypto.NewSQLSecretReader(s.db)
	for _, n := range nodes {
		cfg, ok := n["config"].(map[string]any)
		if !ok {
			continue
		}
		if err := crypto.ResolveSecrets(context.Background(), reader, tenantID, cfg); err != nil {
			return nodesJSON, err
		}
	}
	return json.Marshal(nodes)
}

// Node represents a pipeline node
type Node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// extractSignature returns the HMAC verification config for a webhook
// connection, or nil if the connection has no signature block configured.
// Reads from nodes[].config.http.signature; the shared secret has already been
// resolved to plaintext by resolveSecretsInNodes (see #66).
func (s *webhookConsumer) extractSignature(conn *Connection) *signatureConfig {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return nil
	}
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		var cfg struct {
			Type string `json:"type"`
			HTTP struct {
				Signature *struct {
					Header    string `json:"header"`
					Algorithm string `json:"algorithm"`
					Encoding  string `json:"encoding"`
					Prefix    string `json:"prefix"`
					Secret    string `json:"secret"` // resolved by ResolveSecrets
				} `json:"signature"`
			} `json:"http"`
		}
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			continue
		}
		if cfg.Type != "http" || cfg.HTTP.Signature == nil {
			continue
		}
		sig := cfg.HTTP.Signature
		if sig.Header == "" || sig.Secret == "" {
			s.logger.Warn("Webhook signature block present but incomplete; skipping verification",
				"connection_id", conn.ID,
				"has_header", sig.Header != "",
				"has_secret", sig.Secret != "")
			return nil
		}
		return &signatureConfig{
			Header:    sig.Header,
			Algorithm: sig.Algorithm,
			Encoding:  sig.Encoding,
			Prefix:    sig.Prefix,
			Secret:    sig.Secret,
		}
	}
	return nil
}

// extractClientCA returns the per-connection mTLS client CA (PEM) for a webhook
// connection, or nil if the connection has no tls.client_ca configured. The CA
// has already been resolved to plaintext by resolveSecretsInNodes (#66). When
// set, handleWebhook requires inbound requests to present a client cert that
// chains to this CA.
func (s *webhookConsumer) extractClientCA(conn *Connection) []byte {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return nil
	}
	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		var cfg struct {
			Type string `json:"type"`
			TLS  struct {
				ClientCA string `json:"client_ca"` // resolved by ResolveSecrets
			} `json:"tls"`
		}
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			continue
		}
		if cfg.Type != "http" || cfg.TLS.ClientCA == "" {
			continue
		}
		return []byte(cfg.TLS.ClientCA)
	}
	return nil
}

// hasWebhookConsumer checks if the connection has an HTTP webhook consumer node
func (s *webhookConsumer) hasWebhookConsumer(conn *Connection) bool {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		s.logger.Warn("Failed to parse nodes", "error", err)
		return false
	}

	for _, node := range nodes {
		if node.Type != "consumer" {
			continue
		}
		var config map[string]json.RawMessage
		if err := json.Unmarshal(node.Config, &config); err != nil {
			continue
		}
		if typeRaw, ok := config["type"]; ok {
			var configType string
			if json.Unmarshal(typeRaw, &configType) == nil && configType == "http" {
				return true
			}
		}
	}
	return false
}

// updateConnectionStatus updates the connection status in the database
func (s *webhookConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	if err != nil {
		return fmt.Errorf("failed to update connection status: %w", err)
	}
	s.logger.Info("Updated connection status", "connection_id", connectionID, "status", status)
	return nil
}

// envOr returns the environment variable value or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
