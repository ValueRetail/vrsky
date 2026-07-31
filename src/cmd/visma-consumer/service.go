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
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const defaultTokenURL = "https://connect.visma.com/connect/token"

// vismaConsumer polls a Visma.net resource per active connection and publishes
// the results. It is an SDK Consumer: Configure wires deps, Run subscribes to
// command subjects and blocks, Stop cancels pollers.
type vismaConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	httpClient *http.Client

	active map[string]context.CancelFunc
	mu     sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// VismaConfig is the per-node configuration (config.visma). client_secret is
// stored as client_secret_secret_id and resolved to plaintext at start.
type VismaConfig struct {
	BaseURL      string `json:"base_url"`  // per-service host+version (required), e.g. https://salesorder.visma.net/api/v3
	TokenURL     string `json:"token_url"` // default https://connect.visma.com/connect/token
	Scope        string `json:"scope"`     // per-service scope (required)
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` // from client_secret_secret_id
	CompanyID    string `json:"company_id"`    // sent as ipp-company-id header (Financials context)
	Resource     string `json:"resource"`      // e.g. SalesOrders, customer
	Query        string `json:"query"`         // optional query string appended (paging/filter, per service)

	PollIntervalSeconds int `json:"poll_interval_seconds"`
}

type nodeConfig struct {
	Type  string       `json:"type"`
	Visma *VismaConfig `json:"visma"`
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

func (c *vismaConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("visma-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	c.db = res.DB
	c.nc = res.NATS
	c.logger = res.Logger
	c.active = make(map[string]context.CancelFunc)
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	res.Health.SetReady(true)
	return nil
}

func (c *vismaConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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

func (c *vismaConsumer) Stop(ctx context.Context) error {
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

func (c *vismaConsumer) handleStartCommand(msg *nats.Msg) {
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
		logger.Debug("Not a Visma consumer for this connection", "error", err)
		return
	}
	if cfg.BaseURL == "" || cfg.Scope == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.Resource == "" {
		logger.Error("Visma config incomplete (need base_url, scope, resource, client_id, client_secret_secret_id)")
		return
	}
	if cfg.PollIntervalSeconds <= 0 {
		logger.Error("Visma consumer needs poll_interval_seconds > 0")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.active[cmd.ConnectionID] = cancel
	c.mu.Unlock()

	logger.Info("Starting Visma poller", "resource", cfg.Resource, "interval", cfg.PollIntervalSeconds)
	go c.runPoller(ctx, cmd.ConnectionID, cmd.TenantID, cfg)
}

func (c *vismaConsumer) handleStopCommand(msg *nats.Msg) {
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

func (c *vismaConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *VismaConfig) {
	logger := c.logger.With("connection_id", connID)
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(c.httpClient)

	poll := func() {
		if err := c.fetchAndPublish(ctx, connID, tenantID, cfg, tok, logger); err != nil && ctx.Err() == nil {
			logger.Error("Visma fetch failed", "error", err)
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

// fetchAndPublish GETs the resource and publishes the response. Visma paging
// varies by service, so this publishes one envelope per poll; use `query` for
// service-specific paging/filtering. A JSON array body is published as-is; a
// single object body is wrapped in a one-element array for a consistent shape.
func (c *vismaConsumer) fetchAndPublish(ctx context.Context, connID, tenantID string, cfg *VismaConfig, tok *oauthcc.Client, logger *slog.Logger) error {
	body, err := c.get(ctx, cfg, tok)
	if err != nil {
		return err
	}
	var payload []byte
	var count int
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err != nil {
			return fmt.Errorf("parse Visma array: %w", err)
		}
		if len(arr) == 0 {
			logger.Info("Visma fetch complete", "resource", cfg.Resource, "records", 0)
			return nil
		}
		payload, count = body, len(arr)
	} else {
		// Wrap a single object so downstream always sees an array.
		payload = append(append([]byte("["), body...), ']')
		count = 1
	}

	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = "application/json"
	env.Source = "visma-consumer"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.StepHistory = []string{"visma-consumer"}
	env.Metadata = map[string]interface{}{"resource": cfg.Resource, "record_count": count}
	if err := c.publish(ctx, env); err != nil {
		return err
	}
	logger.Info("Visma fetch complete", "resource", cfg.Resource, "records", count)
	return nil
}

func (c *vismaConsumer) get(ctx context.Context, cfg *VismaConfig, tok *oauthcc.Client) ([]byte, error) {
	access, err := tok.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.resourceURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	if cfg.CompanyID != "" {
		req.Header.Set("ipp-company-id", cfg.CompanyID)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("visma %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (cfg *VismaConfig) effectiveTokenURL() string {
	if cfg.TokenURL != "" {
		return cfg.TokenURL
	}
	return defaultTokenURL
}

func (cfg *VismaConfig) resourceURL() string {
	u := strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.TrimLeft(cfg.Resource, "/")
	if cfg.Query != "" {
		u += "?" + strings.TrimPrefix(cfg.Query, "?")
	}
	return u
}

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Visma consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *vismaConsumer) getConfig(ctx context.Context, connectionID, tenantID string) (*VismaConfig, error) {
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
		if nc.Type == "visma" && nc.Visma != nil {
			return nc.Visma, nil
		}
	}
	return nil, errors.New("no visma consumer node found")
}
