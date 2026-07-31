package main

import (
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
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	defaultBaseURL  = "https://api.mysitoo.com/v2"
	defaultResource = "transactions" // Sitoo orders/receipts
	defaultPageSize = 1000           // Sitoo `num`; batch 1000–5000 is optimal
	maxRateRetries  = 5              // bounded retries on HTTP 429
)

// sitooConsumer polls the Sitoo REST API per active connection (and/or receives
// SPI Event webhooks) and publishes the results into the pipeline. It is an SDK
// Consumer: Configure wires deps, Run subscribes to command subjects and blocks,
// Stop cancels pollers.
type sitooConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	httpClient *http.Client

	// resolveTenant maps a connection id to its owning tenant (webhook routing).
	// Defaulted in Configure to the DB lookup; tests inject a stub.
	resolveTenant func(connID string) (string, error)

	active map[string]context.CancelFunc
	mu     sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// SitooConfig is the per-node configuration (config.sitoo). The API password is
// stored as `api_password_secret_id` (a secrets-vault reference); the resolver
// replaces it with the plaintext `api_password` at connection-start time.
type SitooConfig struct {
	AccountID           int64  `json:"account_id"`
	SiteID              int64  `json:"site_id"`
	APIID               string `json:"api_id"`
	APIPassword         string `json:"api_password"` // resolved from api_password_secret_id
	BaseURL             string `json:"base_url"`     // optional; default https://api.mysitoo.com/v2
	Resource            string `json:"resource"`     // e.g. transactions, warehouseitems, products
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	PageSize            int    `json:"page_size"` // Sitoo `num`; default 1000
}

type nodeConfig struct {
	Type  string       `json:"type"`
	Sitoo *SitooConfig `json:"sitoo"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

type commandMessage struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
}

// Configure wires dependencies. Called once before Run.
func (s *sitooConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("sitoo-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.active = make(map[string]context.CancelFunc)
	if s.httpClient == nil {
		// No redirect following: requests carry the Sitoo Basic-auth credential,
		// which Go would forward on a same-host redirect. We only ever call the
		// configured Sitoo host, so a redirect is unexpected — surface it.
		s.httpClient = &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}

	if s.resolveTenant == nil {
		s.resolveTenant = s.getConnectionTenant
	}

	// Real-time SPI Events (webhook mode): Sitoo POSTs event notifications to
	// /sitoo/events/{connectionID} on the SDK auxiliary HTTP port.
	s.RegisterHTTPHandler("/sitoo/events/", s.handleWebhook())

	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled. Per-connection polling is driven from the command handlers.
func (s *sitooConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	s.publish = publish

	startSub, err := s.nc.Subscribe("vrsky.commands.*.connection.start", s.handleStartCommand)
	if err != nil {
		return fmt.Errorf("subscribe start commands: %w", err)
	}
	s.startSub = startSub

	stopSub, err := s.nc.Subscribe("vrsky.commands.*.connection.stop", s.handleStopCommand)
	if err != nil {
		return fmt.Errorf("subscribe stop commands: %w", err)
	}
	s.stopSub = stopSub

	s.logger.Info("Subscribed to NATS command topics")
	<-ctx.Done()
	return nil
}

// Stop cancels all pollers. The SDK runner calls this after Run returns.
func (s *sitooConsumer) Stop(ctx context.Context) error {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}
	s.mu.Lock()
	for id, cancel := range s.active {
		s.logger.Info("Stopping Sitoo poller", "connection_id", id)
		cancel()
	}
	s.active = make(map[string]context.CancelFunc)
	s.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (s *sitooConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("parse start command", "error", err)
		return
	}
	logger := s.logger.With("connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	s.mu.RLock()
	_, exists := s.active[cmd.ConnectionID]
	s.mu.RUnlock()
	if exists {
		logger.Warn("Sitoo poller already running")
		return
	}

	cfg, err := s.getSitooConfig(context.Background(), cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		logger.Debug("Not a Sitoo consumer for this connection", "error", err)
		return
	}
	if cfg.AccountID == 0 || cfg.SiteID == 0 || cfg.APIID == "" || cfg.APIPassword == "" {
		logger.Error("Sitoo config incomplete (need account_id, site_id, api_id, api_password_secret_id)")
		return
	}
	// Poll interval <= 0 means webhook-only (no polling) — a valid mode.
	if cfg.PollIntervalSeconds <= 0 {
		logger.Info("Sitoo connection is webhook-only (no poll interval configured)")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.active[cmd.ConnectionID] = cancel
	s.mu.Unlock()

	logger.Info("Starting Sitoo poller", "resource", cfg.effectiveResource(), "interval", cfg.PollIntervalSeconds)
	go s.runPoller(ctx, cmd.ConnectionID, cmd.TenantID, cfg)
}

func (s *sitooConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		s.logger.Error("parse stop command", "error", err)
		return
	}
	s.mu.Lock()
	cancel, exists := s.active[cmd.ConnectionID]
	if exists {
		cancel()
		delete(s.active, cmd.ConnectionID)
	}
	s.mu.Unlock()
	if exists {
		s.logger.Info("Sitoo poller stopped", "connection_id", cmd.ConnectionID)
	}
}

func (s *sitooConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *SitooConfig) {
	logger := s.logger.With("connection_id", connID)
	if err := s.fetchAndPublish(ctx, connID, tenantID, cfg, logger); err != nil && ctx.Err() == nil {
		logger.Error("Sitoo fetch failed", "error", err)
	}
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.fetchAndPublish(ctx, connID, tenantID, cfg, logger); err != nil && ctx.Err() == nil {
				logger.Error("Sitoo fetch failed", "error", err)
			}
		}
	}
}

// sitooCollection is the envelope-based list response: a totalcount plus the
// page of items. (Exact item field names vary per resource; we pass items
// through as raw JSON.)
type sitooCollection struct {
	TotalCount int               `json:"totalcount"`
	Items      []json.RawMessage `json:"items"`
}

// fetchAndPublish pages through the configured Sitoo collection (start/num) and
// publishes each non-empty page as one JSON-array envelope.
func (s *sitooConsumer) fetchAndPublish(ctx context.Context, connID, tenantID string, cfg *SitooConfig, logger *slog.Logger) error {
	pageSize := cfg.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	start, page, total := 0, 0, 0
	for {
		page++
		reqURL := fmt.Sprintf("%s/accounts/%d/sites/%d/%s?start=%d&num=%d",
			cfg.effectiveBaseURL(), cfg.AccountID, cfg.SiteID, cfg.effectiveResource(), start, pageSize)

		body, err := s.get(ctx, cfg, reqURL)
		if err != nil {
			return err
		}
		var coll sitooCollection
		if err := json.Unmarshal(body, &coll); err != nil {
			return fmt.Errorf("parse Sitoo collection: %w", err)
		}
		if len(coll.Items) > 0 {
			if err := s.publishItems(ctx, connID, tenantID, cfg.effectiveResource(), coll.Items); err != nil {
				return fmt.Errorf("publish items: %w", err)
			}
			total += len(coll.Items)
		}
		// Last page when we received fewer than a full page, or reached totalcount.
		if len(coll.Items) < pageSize || (coll.TotalCount > 0 && start+len(coll.Items) >= coll.TotalCount) {
			break
		}
		start += len(coll.Items)
	}
	logger.Info("Sitoo fetch complete", "resource", cfg.effectiveResource(), "items", total, "pages", page)
	return nil
}

// get performs an authenticated (HTTP Basic) GET, honouring Sitoo's 429
// time-slot rate limit: on 429 it waits for X-Rate-Limit-Reset / Retry-After
// and retries, up to maxRateRetries.
func (s *sitooConsumer) get(ctx context.Context, cfg *SitooConfig, fullURL string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(cfg.APIID, cfg.APIPassword)
		req.Header.Set("Accept", "application/json")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			_ = resp.Body.Close()
			if attempt >= maxRateRetries {
				return nil, fmt.Errorf("sitoo rate limit: still 429 after %d retries", maxRateRetries)
			}
			wait := rateLimitWait(resp.Header)
			s.logger.Warn("Sitoo 429; backing off", "wait", wait, "attempt", attempt+1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32 MiB cap
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("sitoo %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		return body, nil
	}
}

// rateLimitWait reads Sitoo's reset hint (X-Rate-Limit-Reset is epoch seconds;
// Retry-After is delta seconds), clamped to a sane range.
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

func (s *sitooConsumer) publishItems(ctx context.Context, connID, tenantID, resource string, items []json.RawMessage) error {
	payload, err := json.Marshal(items)
	if err != nil {
		return err
	}
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = "application/json"
	env.Source = "sitoo-consumer"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.StepHistory = []string{"sitoo-consumer"}
	env.Metadata = map[string]interface{}{"resource": resource, "record_count": len(items)}
	return s.publish(ctx, env)
}

func (c *SitooConfig) effectiveBaseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return defaultBaseURL
}

func (c *SitooConfig) effectiveResource() string {
	if c.Resource != "" {
		return strings.Trim(c.Resource, "/")
	}
	return defaultResource
}

// --- DB helpers ---

// getSitooConfig loads the connection, resolves any *_secret_id references
// against the secrets vault, and extracts its Sitoo consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (s *sitooConsumer) getSitooConfig(ctx context.Context, connectionID, tenantID string) (*SitooConfig, error) {
	var nodesJSON json.RawMessage
	err := s.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON)
	if err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
	}

	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	reader := crypto.NewSQLSecretReader(s.db)
	for _, n := range nodes {
		if n.Type != "consumer" {
			continue
		}
		// Replace api_password_secret_id → api_password (decrypted) before parse.
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
	return nil, errors.New("no sitoo consumer node found")
}

// getConnectionTenant resolves the owning tenant for a connection id (used by
// the webhook path, where Sitoo posts without a tenant context).
// lint:tenant-ok — resolves a connection's own tenant by PK for routing.
func (s *sitooConsumer) getConnectionTenant(connectionID string) (string, error) {
	var tenantID string
	err := s.db.QueryRow(`SELECT tenant_id FROM connections WHERE id = $1`, connectionID).Scan(&tenantID)
	return tenantID, err
}
