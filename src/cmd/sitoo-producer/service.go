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
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	defaultBaseURL  = "https://api.mysitoo.com/v2"
	defaultResource = "warehouseitems" // stock is the common write target
	defaultMethod   = http.MethodPost
)

// sitooProducer delivers pipeline envelopes into Sitoo via its REST API. It is
// an SDK Producer: Configure wires deps, Deliver writes one envelope.
type sitooProducer struct {
	sdk.BaseProducer

	db         *sql.DB
	logger     *slog.Logger
	httpClient *http.Client
}

// SitooProducerConfig is the per-node configuration (config.sitoo). The API
// password is stored as `api_password_secret_id` and resolved to plaintext at
// delivery time.
type SitooProducerConfig struct {
	AccountID   int64  `json:"account_id"`
	SiteID      int64  `json:"site_id"`
	APIID       string `json:"api_id"`
	APIPassword string `json:"api_password"` // resolved from api_password_secret_id
	BaseURL     string `json:"base_url"`     // optional; default https://api.mysitoo.com/v2
	Resource    string `json:"resource"`     // e.g. warehouseitems, prices, products
	Method      string `json:"method"`       // POST (default) or PUT
}

type nodeConfig struct {
	Type  string               `json:"type"`
	Sitoo *SitooProducerConfig `json:"sitoo"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// Configure wires dependencies. Called once before Deliver.
func (p *sitooProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("sitoo-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.httpClient == nil {
		// Surface redirects instead of following them — requests carry the Sitoo
		// Basic-auth credential, which Go would forward on a same-host redirect.
		p.httpClient = &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	p.logger.Info("sitoo-producer configured")
	return nil
}

// Deliver writes the envelope payload into Sitoo. Missing producer config for
// the connection is not an error (the envelope simply isn't for us). A non-JSON
// payload is poison (Permanent); transient HTTP/network failures are Retriable.
func (p *sitooProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	if env.IntegrationID == "" {
		return nil
	}
	cfg, err := p.getSitooConfig(ctx, env.IntegrationID, env.TenantID)
	if err != nil {
		p.logger.Debug("No Sitoo producer config", "connection_id", env.IntegrationID, "error", err)
		return nil
	}
	if cfg.AccountID == 0 || cfg.SiteID == 0 || cfg.APIID == "" || cfg.APIPassword == "" {
		// Misconfiguration can't be fixed by retrying this message.
		return sdk.Permanent(fmt.Errorf("sitoo producer config incomplete for connection %s", env.IntegrationID))
	}

	// Payload must be valid JSON (an object or array) that Sitoo accepts.
	if !json.Valid(env.Payload) {
		p.logger.Error("dropping: payload is not valid JSON", "envelope_id", env.ID)
		return sdk.Permanent(errors.New("payload is not valid JSON"))
	}

	return p.write(ctx, cfg, env.Payload)
}

// write POSTs/PUTs the payload to the configured Sitoo resource with Basic auth
// and maps the response to the SDK's retry classification.
func (p *sitooProducer) write(ctx context.Context, cfg *SitooProducerConfig, payload []byte) error {
	reqURL := fmt.Sprintf("%s/accounts/%d/sites/%d/%s",
		cfg.effectiveBaseURL(), cfg.AccountID, cfg.SiteID, cfg.effectiveResource())

	req, err := http.NewRequestWithContext(ctx, cfg.effectiveMethod(), reqURL, bytes.NewReader(payload))
	if err != nil {
		return sdk.Permanent(err)
	}
	req.SetBasicAuth(cfg.APIID, cfg.APIPassword)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("sitoo request: %w", err)) // network — retry
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.logger.Info("Delivered to Sitoo", "resource", cfg.effectiveResource(), "status", resp.StatusCode)
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return sdk.RateLimited(fmt.Errorf("sitoo 429: %s", snippet(body)), rateLimitWait(resp.Header))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// Static Basic-auth creds — a 401/403 won't fix itself on retry.
		return sdk.Permanent(fmt.Errorf("sitoo auth %d: %s", resp.StatusCode, snippet(body)))
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// Bad request/data — poison; retrying the same payload can't help.
		return sdk.Permanent(fmt.Errorf("sitoo %d: %s", resp.StatusCode, snippet(body)))
	default: // 5xx and anything else → transient
		return sdk.Retriable(fmt.Errorf("sitoo %d: %s", resp.StatusCode, snippet(body)))
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func rateLimitWait(h http.Header) time.Duration {
	if v := h.Get("X-Rate-Limit-Reset"); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Until(time.Unix(epoch, 0)); d > 0 {
				return clampWait(d)
			}
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return clampWait(time.Duration(secs) * time.Second)
		}
	}
	return 5 * time.Second
}

func clampWait(d time.Duration) time.Duration {
	switch {
	case d < time.Second:
		return time.Second
	case d > 10*time.Minute:
		return 10 * time.Minute
	default:
		return d
	}
}

func (c *SitooProducerConfig) effectiveBaseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return defaultBaseURL
}

func (c *SitooProducerConfig) effectiveResource() string {
	if c.Resource != "" {
		return strings.Trim(c.Resource, "/")
	}
	return defaultResource
}

func (c *SitooProducerConfig) effectiveMethod() string {
	if m := strings.ToUpper(strings.TrimSpace(c.Method)); m == http.MethodPut || m == http.MethodPost {
		return m
	}
	return defaultMethod
}

// getSitooConfig loads the connection, resolves any *_secret_id references, and
// extracts its Sitoo producer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (p *sitooProducer) getSitooConfig(ctx context.Context, connectionID, tenantID string) (*SitooProducerConfig, error) {
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
		if nc.Type == "sitoo" && nc.Sitoo != nil {
			return nc.Sitoo, nil
		}
	}
	return nil, errors.New("no sitoo producer node found")
}

// ServesConnection reports whether this connection has a Sitoo destination —
// mirroring Deliver's own "no config -> not ours" semantics — so the SDK can
// ack foreign connections before rehydrating large payloads (sdk.ConnectionScoped).
func (p *sitooProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	_, err := p.getSitooConfig(ctx, connectionID, tenantID)
	return err == nil
}
