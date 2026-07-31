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

// Default write target: upsert products by extId. Bulk payload cap is 2 MB in
// Front Systems, so an upstream should batch accordingly.
const (
	defaultResource = "/api/Products/bulk-upsert"
	defaultMethod   = http.MethodPost
)

// frontSystemsProducer delivers pipeline envelopes into Front Systems master
// data. It is an SDK Producer: Configure wires deps, Deliver writes one envelope.
type frontSystemsProducer struct {
	sdk.BaseProducer

	db         *sql.DB
	logger     *slog.Logger
	httpClient *http.Client
}

// FrontSystemsProducerConfig is the per-node configuration (config.front_systems).
// The two API keys are stored as `*_secret_id` references and resolved at
// delivery time.
type FrontSystemsProducerConfig struct {
	BaseURL         string `json:"base_url"`         // per-partner Azure APIM host (required)
	SubscriptionKey string `json:"subscription_key"` // from subscription_key_secret_id
	APIKey          string `json:"api_key"`          // from api_key_secret_id
	Resource        string `json:"resource"`         // e.g. /api/Products, /api/PriceListV2
	Method          string `json:"method"`           // POST (default) or PUT
}

type nodeConfig struct {
	Type         string                      `json:"type"`
	FrontSystems *FrontSystemsProducerConfig `json:"front_systems"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// Configure wires dependencies. Called once before Deliver.
func (p *frontSystemsProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("front-systems-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.httpClient == nil {
		p.httpClient = &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	p.logger.Info("front-systems-producer configured")
	return nil
}

// Deliver writes the envelope payload into Front Systems. Missing producer
// config is not an error; a non-JSON payload is poison; transient HTTP/network
// failures are Retriable.
func (p *frontSystemsProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	if env.IntegrationID == "" {
		return nil
	}
	cfg, err := p.getConfig(ctx, env.IntegrationID, env.TenantID)
	if err != nil {
		p.logger.Debug("No Front Systems producer config", "connection_id", env.IntegrationID, "error", err)
		return nil
	}
	if cfg.BaseURL == "" || cfg.SubscriptionKey == "" || cfg.APIKey == "" {
		return sdk.Permanent(fmt.Errorf("front systems producer config incomplete for connection %s", env.IntegrationID))
	}
	if !json.Valid(env.Payload) {
		p.logger.Error("dropping: payload is not valid JSON", "envelope_id", env.ID)
		return sdk.Permanent(errors.New("payload is not valid JSON"))
	}
	return p.write(ctx, cfg, env.Payload)
}

func (p *frontSystemsProducer) write(ctx context.Context, cfg *FrontSystemsProducerConfig, payload []byte) error {
	reqURL := strings.TrimRight(cfg.BaseURL, "/") + cfg.effectiveResource()
	req, err := http.NewRequestWithContext(ctx, cfg.effectiveMethod(), reqURL, bytes.NewReader(payload))
	if err != nil {
		return sdk.Permanent(err)
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", cfg.SubscriptionKey)
	req.Header.Set("x-api-key", cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("front systems request: %w", err))
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.logger.Info("Delivered to Front Systems", "resource", cfg.effectiveResource(), "status", resp.StatusCode)
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return sdk.RateLimited(fmt.Errorf("front systems 429: %s", snippet(body)), rateLimitWait(resp.Header))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return sdk.Permanent(fmt.Errorf("front systems auth %d: %s", resp.StatusCode, snippet(body)))
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return sdk.Permanent(fmt.Errorf("front systems %d: %s", resp.StatusCode, snippet(body)))
	default:
		return sdk.Retriable(fmt.Errorf("front systems %d: %s", resp.StatusCode, snippet(body)))
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

func (c *FrontSystemsProducerConfig) effectiveResource() string {
	if c.Resource != "" {
		if !strings.HasPrefix(c.Resource, "/") {
			return "/" + c.Resource
		}
		return c.Resource
	}
	return defaultResource
}

func (c *FrontSystemsProducerConfig) effectiveMethod() string {
	if m := strings.ToUpper(strings.TrimSpace(c.Method)); m == http.MethodPut || m == http.MethodPost {
		return m
	}
	return defaultMethod
}

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Front Systems producer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (p *frontSystemsProducer) getConfig(ctx context.Context, connectionID, tenantID string) (*FrontSystemsProducerConfig, error) {
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
		if nc.Type == "front_systems" && nc.FrontSystems != nil {
			return nc.FrontSystems, nil
		}
	}
	return nil, errors.New("no front_systems producer node found")
}
