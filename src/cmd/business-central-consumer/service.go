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
	"net/url"
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

const (
	defaultScope       = "https://api.businesscentral.dynamics.com/.default"
	defaultAPIHost     = "https://api.businesscentral.dynamics.com"
	defaultEnvironment = "Production"
	defaultEntity      = "items"
)

// bcConsumer polls a Business Central OData entity per active connection and
// publishes the results. It is an SDK Consumer: Configure wires deps, Run
// subscribes to command subjects and blocks, Stop cancels pollers.
type bcConsumer struct {
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

// BCConfig is the per-node configuration (config.business_central). client_secret
// is stored as client_secret_secret_id and resolved to plaintext at start.
type BCConfig struct {
	AADTenantID  string `json:"aad_tenant_id"` // Entra tenant (GUID or domain)
	Environment  string `json:"environment"`   // e.g. Production
	CompanyID    string `json:"company_id"`    // BC company GUID
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` // from client_secret_secret_id
	Entity       string `json:"entity"`        // e.g. items, customers, salesOrders
	Filter       string `json:"filter"`        // optional OData $filter

	// Optional overrides (default to the BC cloud endpoints; set for on-prem or tests).
	APIBaseURL string `json:"api_base_url"`
	TokenURL   string `json:"token_url"`
	Scope      string `json:"scope"`

	PollIntervalSeconds int `json:"poll_interval_seconds"`
}

type nodeConfig struct {
	Type            string    `json:"type"`
	BusinessCentral *BCConfig `json:"business_central"`
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

func (c *bcConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("business-central-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
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

func (c *bcConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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

func (c *bcConsumer) Stop(ctx context.Context) error {
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

func (c *bcConsumer) handleStartCommand(msg *nats.Msg) {
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
		logger.Debug("Not a Business Central consumer for this connection", "error", err)
		return
	}
	if cfg.AADTenantID == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.CompanyID == "" {
		logger.Error("Business Central config incomplete (need aad_tenant_id, company_id, client_id, client_secret_secret_id)")
		return
	}
	if cfg.PollIntervalSeconds <= 0 {
		logger.Error("Business Central consumer needs poll_interval_seconds > 0")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.active[cmd.ConnectionID] = cancel
	c.mu.Unlock()

	logger.Info("Starting Business Central poller", "entity", cfg.effectiveEntity(), "interval", cfg.PollIntervalSeconds)
	go c.runPoller(ctx, cmd.ConnectionID, cmd.TenantID, cfg)
}

func (c *bcConsumer) handleStopCommand(msg *nats.Msg) {
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

func (c *bcConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *BCConfig) {
	logger := c.logger.With("connection_id", connID)
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(c.httpClient)

	poll := func() {
		if err := c.fetchAndPublish(ctx, connID, tenantID, cfg, tok, logger); err != nil && ctx.Err() == nil {
			logger.Error("Business Central fetch failed", "error", err)
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

// odataPage is the OData v4 collection envelope.
type odataPage struct {
	Value    []json.RawMessage `json:"value"`
	NextLink string            `json:"@odata.nextLink"`
}

// fetchAndPublish GETs the OData entity, follows @odata.nextLink, and publishes
// each page's records as one JSON-array envelope.
func (c *bcConsumer) fetchAndPublish(ctx context.Context, connID, tenantID string, cfg *BCConfig, tok *oauthcc.Client, logger *slog.Logger) error {
	next := cfg.entityURL()
	page, total := 0, 0
	for next != "" {
		page++
		body, err := c.get(ctx, tok, next)
		if err != nil {
			return err
		}
		var p odataPage
		if err := json.Unmarshal(body, &p); err != nil {
			return fmt.Errorf("parse OData page: %w", err)
		}
		if len(p.Value) > 0 {
			if err := c.publishRecords(ctx, connID, tenantID, cfg.effectiveEntity(), p.Value); err != nil {
				return fmt.Errorf("publish records: %w", err)
			}
			total += len(p.Value)
		}
		next = p.NextLink
	}
	logger.Info("Business Central fetch complete", "entity", cfg.effectiveEntity(), "records", total, "pages", page)
	return nil
}

func (c *bcConsumer) get(ctx context.Context, tok *oauthcc.Client, fullURL string) ([]byte, error) {
	access, err := tok.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("business central %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (c *bcConsumer) publishRecords(ctx context.Context, connID, tenantID, entity string, records []json.RawMessage) error {
	payload, err := json.Marshal(records)
	if err != nil {
		return err
	}
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = "application/json"
	env.Source = "business-central-consumer"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.StepHistory = []string{"business-central-consumer"}
	env.Metadata = map[string]interface{}{"entity": entity, "record_count": len(records)}
	return c.publish(ctx, env)
}

// --- config helpers ---

func (cfg *BCConfig) effectiveEntity() string {
	if cfg.Entity != "" {
		return strings.Trim(cfg.Entity, "/")
	}
	return defaultEntity
}

func (cfg *BCConfig) effectiveScope() string {
	if cfg.Scope != "" {
		return cfg.Scope
	}
	return defaultScope
}

func (cfg *BCConfig) effectiveTokenURL() string {
	if cfg.TokenURL != "" {
		return cfg.TokenURL
	}
	return fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", cfg.AADTenantID)
}

// entityURL builds the first-page API v2.0 URL, scoped to the company.
func (cfg *BCConfig) entityURL() string {
	host := cfg.APIBaseURL
	if host == "" {
		env := cfg.Environment
		if env == "" {
			env = defaultEnvironment
		}
		host = fmt.Sprintf("%s/v2.0/%s/%s/api/v2.0", defaultAPIHost, cfg.AADTenantID, env)
	}
	host = strings.TrimRight(host, "/")
	u := fmt.Sprintf("%s/companies(%s)/%s", host, cfg.CompanyID, cfg.effectiveEntity())
	if cfg.Filter != "" {
		u += "?$filter=" + url.QueryEscape(cfg.Filter)
	}
	return u
}

// --- DB ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Business Central consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *bcConsumer) getConfig(ctx context.Context, connectionID, tenantID string) (*BCConfig, error) {
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
		if nc.Type == "business_central" && nc.BusinessCentral != nil {
			return nc.BusinessCentral, nil
		}
	}
	return nil, errors.New("no business_central consumer node found")
}
