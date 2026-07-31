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
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// brightpearlConsumer polls a Brightpearl resource per active connection and/or
// receives webhooks, and publishes the results. It is an SDK Consumer.
type brightpearlConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	httpClient *http.Client

	// resolveTenant maps a connection id to its tenant (webhook routing).
	resolveTenant func(connID string) (string, error)

	active map[string]context.CancelFunc
	mu     sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// BrightpearlConfig is the per-node configuration (config.brightpearl). The
// staff token is stored as staff_token_secret_id and resolved at start.
type BrightpearlConfig struct {
	Datacenter  string `json:"datacenter"`   // e.g. eu1, use1 (for the base URL)
	AccountCode string `json:"account_code"` // Brightpearl account code
	BaseURL     string `json:"base_url"`     // optional override; else derived
	AppRef      string `json:"app_ref"`      // brightpearl-app-ref header
	StaffToken  string `json:"staff_token"`  // from staff_token_secret_id

	Resource string `json:"resource"` // e.g. /order-service/order-search
	Query    string `json:"query"`    // optional query string (paging/filter)

	PollIntervalSeconds int `json:"poll_interval_seconds"`
}

type nodeConfig struct {
	Type        string             `json:"type"`
	Brightpearl *BrightpearlConfig `json:"brightpearl"`
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

func (c *brightpearlConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("brightpearl-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	c.db = res.DB
	c.nc = res.NATS
	c.logger = res.Logger
	c.active = make(map[string]context.CancelFunc)
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if c.resolveTenant == nil {
		c.resolveTenant = c.getConnectionTenant
	}
	c.RegisterHTTPHandler("/brightpearl/events/", c.handleWebhook())
	res.Health.SetReady(true)
	return nil
}

func (c *brightpearlConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	c.publish = publish
	startSub, err := c.nc.Subscribe("vrsky.commands.*.connection.start", c.handleStartCommand)
	if err != nil {
		return fmt.Errorf("subscribe start commands: %w", err)
	}
	c.startSub = startSub
	stopSub, err := c.nc.Subscribe("vrsky.commands.*.connection.stop", c.handleStopCommand)
	if err != nil {
		return fmt.Errorf("subscribe stop commands: %w", err)
	}
	c.stopSub = stopSub
	c.logger.Info("Subscribed to NATS command topics")
	<-ctx.Done()
	return nil
}

func (c *brightpearlConsumer) Stop(ctx context.Context) error {
	if c.startSub != nil {
		_ = c.startSub.Unsubscribe()
	}
	if c.stopSub != nil {
		_ = c.stopSub.Unsubscribe()
	}
	c.mu.Lock()
	for _, cancel := range c.active {
		cancel()
	}
	c.active = make(map[string]context.CancelFunc)
	c.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (c *brightpearlConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse start command", "error", err)
		return
	}
	logger := c.logger.With("connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	c.mu.RLock()
	_, exists := c.active[cmd.ConnectionID]
	c.mu.RUnlock()
	if exists {
		return
	}
	cfg, err := c.getConfig(context.Background(), cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		logger.Debug("Not a Brightpearl consumer for this connection", "error", err)
		return
	}
	if cfg.AppRef == "" || cfg.StaffToken == "" || cfg.baseURL() == "" {
		logger.Error("Brightpearl config incomplete (need account_code+datacenter or base_url, app_ref, staff_token_secret_id)")
		return
	}
	// poll_interval <= 0 → webhook-only (valid).
	if cfg.PollIntervalSeconds <= 0 || cfg.Resource == "" {
		logger.Info("Brightpearl connection is webhook-only (no poll resource/interval)")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.active[cmd.ConnectionID] = cancel
	c.mu.Unlock()

	logger.Info("Starting Brightpearl poller", "resource", cfg.Resource, "interval", cfg.PollIntervalSeconds)
	go c.runPoller(ctx, cmd.ConnectionID, cmd.TenantID, cfg)
}

func (c *brightpearlConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		return
	}
	c.mu.Lock()
	if cancel, ok := c.active[cmd.ConnectionID]; ok {
		cancel()
		delete(c.active, cmd.ConnectionID)
	}
	c.mu.Unlock()
}

func (c *brightpearlConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *BrightpearlConfig) {
	logger := c.logger.With("connection_id", connID)
	poll := func() {
		if err := c.fetchAndPublish(ctx, connID, tenantID, cfg, logger); err != nil && ctx.Err() == nil {
			logger.Error("Brightpearl fetch failed", "error", err)
		}
	}
	poll()
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

// fetchAndPublish GETs the resource and publishes the Brightpearl `response`
// payload (Brightpearl wraps most bodies in {"response": …}). Per-endpoint
// paging is driven by the optional `query` string.
func (c *brightpearlConsumer) fetchAndPublish(ctx context.Context, connID, tenantID string, cfg *BrightpearlConfig, logger *slog.Logger) error {
	body, err := c.get(ctx, cfg)
	if err != nil {
		return err
	}
	// Unwrap the {"response": …} envelope when present.
	var wrapper struct {
		Response json.RawMessage `json:"response"`
	}
	payload := body
	if json.Unmarshal(body, &wrapper) == nil && len(wrapper.Response) > 0 {
		payload = wrapper.Response
	}

	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = "application/json"
	env.Source = "brightpearl-consumer"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.StepHistory = []string{"brightpearl-consumer"}
	env.Metadata = map[string]interface{}{"resource": cfg.Resource}
	if err := c.publish(ctx, env); err != nil {
		return err
	}
	logger.Info("Brightpearl fetch complete", "resource", cfg.Resource, "bytes", len(payload))
	return nil
}

func (c *brightpearlConsumer) get(ctx context.Context, cfg *BrightpearlConfig) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.resourceURL(), nil)
	if err != nil {
		return nil, err
	}
	setAuth(req, cfg)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("brightpearl %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// setAuth applies the Brightpearl staff-app headers.
func setAuth(req *http.Request, cfg *BrightpearlConfig) {
	req.Header.Set("brightpearl-app-ref", cfg.AppRef)
	req.Header.Set("brightpearl-staff-token", cfg.StaffToken)
}

func (cfg *BrightpearlConfig) baseURL() string {
	if cfg.BaseURL != "" {
		return strings.TrimRight(cfg.BaseURL, "/")
	}
	if cfg.Datacenter == "" || cfg.AccountCode == "" {
		return ""
	}
	return fmt.Sprintf("https://ws-%s.brightpearl.com/public-api/%s", cfg.Datacenter, cfg.AccountCode)
}

func (cfg *BrightpearlConfig) resourceURL() string {
	u := cfg.baseURL() + "/" + strings.TrimLeft(cfg.Resource, "/")
	if cfg.Query != "" {
		u += "?" + strings.TrimPrefix(cfg.Query, "?")
	}
	return u
}

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Brightpearl consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *brightpearlConsumer) getConfig(ctx context.Context, connectionID, tenantID string) (*BrightpearlConfig, error) {
	var nodesJSON json.RawMessage
	if err := c.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON); err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
	}
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	reader := crypto.NewSQLSecretReader(c.db)
	for _, n := range nodes {
		if n.Type != "consumer" {
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
		if nc.Type == "brightpearl" && nc.Brightpearl != nil {
			return nc.Brightpearl, nil
		}
	}
	return nil, errors.New("no brightpearl consumer node found")
}

// getConnectionTenant resolves the owning tenant for a connection id (webhook
// routing). lint:tenant-ok — resolves a connection's own tenant by PK.
func (c *brightpearlConsumer) getConnectionTenant(connectionID string) (string, error) {
	var tenantID string
	err := c.db.QueryRow(`SELECT tenant_id FROM connections WHERE id = $1`, connectionID).Scan(&tenantID)
	return tenantID, err
}
