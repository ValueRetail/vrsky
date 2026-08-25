package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	defaultTokenURL = "https://connect.visma.com/connect/token"
	defaultMethod   = http.MethodPost
)

// vismaProducer delivers pipeline envelopes into Visma.net. It is an SDK
// Producer: Configure wires deps, Deliver writes one envelope.
type vismaProducer struct {
	sdk.BaseProducer

	db         *sql.DB
	logger     *slog.Logger
	httpClient *http.Client
}

// VismaProducerConfig is the per-node configuration (config.visma).
type VismaProducerConfig struct {
	BaseURL      string `json:"base_url"` // per-service host+version (required)
	TokenURL     string `json:"token_url"`
	Scope        string `json:"scope"` // per-service scope (required)
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` // from client_secret_secret_id
	CompanyID    string `json:"company_id"`    // ipp-company-id header
	Resource     string `json:"resource"`      // write target, e.g. SalesOrders
	Method       string `json:"method"`        // POST (default) or PUT/PATCH
}

type nodeConfig struct {
	Type  string               `json:"type"`
	Visma *VismaProducerConfig `json:"visma"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (p *vismaProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("visma-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	p.logger.Info("visma-producer configured")
	return nil
}

func (p *vismaProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	if env.IntegrationID == "" {
		return nil
	}
	cfg, err := p.getConfig(ctx, env.IntegrationID, env.TenantID)
	if err != nil {
		p.logger.Debug("No Visma producer config", "connection_id", env.IntegrationID, "error", err)
		return nil
	}
	if cfg.BaseURL == "" || cfg.Scope == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.Resource == "" {
		return sdk.Permanent(fmt.Errorf("visma producer config incomplete for connection %s", env.IntegrationID))
	}
	if !json.Valid(env.Payload) {
		p.logger.Error("dropping: payload is not valid JSON", "envelope_id", env.ID)
		return sdk.Permanent(errors.New("payload is not valid JSON"))
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(p.httpClient)
	return p.write(ctx, cfg, tok, env.Payload)
}

func (p *vismaProducer) write(ctx context.Context, cfg *VismaProducerConfig, tok *oauthcc.Client, payload []byte) error {
	access, err := tok.Token(ctx)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("acquire token: %w", err))
	}
	req, err := http.NewRequestWithContext(ctx, cfg.effectiveMethod(), cfg.resourceURL(), bytes.NewReader(payload))
	if err != nil {
		return sdk.Permanent(err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if cfg.CompanyID != "" {
		req.Header.Set("ipp-company-id", cfg.CompanyID)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("visma request: %w", err))
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.logger.Info("Delivered to Visma", "resource", cfg.Resource, "status", resp.StatusCode)
		return nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable:
		return sdk.RateLimited(fmt.Errorf("visma %d: %s", resp.StatusCode, snippet(body)), 5*time.Second)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return sdk.Permanent(fmt.Errorf("visma auth %d: %s", resp.StatusCode, snippet(body)))
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return sdk.Permanent(fmt.Errorf("visma %d: %s", resp.StatusCode, snippet(body)))
	default:
		return sdk.Retriable(fmt.Errorf("visma %d: %s", resp.StatusCode, snippet(body)))
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func (cfg *VismaProducerConfig) effectiveTokenURL() string {
	if cfg.TokenURL != "" {
		return cfg.TokenURL
	}
	return defaultTokenURL
}

func (cfg *VismaProducerConfig) effectiveMethod() string {
	switch strings.ToUpper(strings.TrimSpace(cfg.Method)) {
	case http.MethodPut:
		return http.MethodPut
	case http.MethodPatch:
		return http.MethodPatch
	case http.MethodPost:
		return http.MethodPost
	}
	return defaultMethod
}

func (cfg *VismaProducerConfig) resourceURL() string {
	return strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.TrimLeft(cfg.Resource, "/")
}

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Visma producer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (p *vismaProducer) getConfig(ctx context.Context, connectionID, tenantID string) (*VismaProducerConfig, error) {
	var nodesJSON json.RawMessage
	if err := p.db.QueryRowContext(ctx,
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON); err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
	}
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	reader := crypto.NewSQLSecretReader(p.db)
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, n.Config)
		if rerr != nil {
			return nil, fmt.Errorf("resolve secrets: %w", rerr)
		}
		var nc nodeConfig
		if err := json.Unmarshal(resolved, &nc); err != nil {
			continue
		}
		if nc.Type == "visma" && nc.Visma != nil {
			return nc.Visma, nil
		}
	}
	return nil, errors.New("no visma producer node found")
}


// ServesConnection reports whether this connection has a Visma destination —
// mirroring Deliver's own "no config -> not ours" semantics — so the SDK can
// ack foreign connections before rehydrating large payloads (sdk.ConnectionScoped).
func (p *vismaProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	_, err := p.getConfig(ctx, connectionID, tenantID)
	return err == nil
}
