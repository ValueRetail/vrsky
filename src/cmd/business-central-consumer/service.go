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

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
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

	// BC's own last-modified field on the v2.0 API entities. Overridable
	// because a custom API page may expose a differently named one.
	defaultCursorField = "lastModifiedDateTime"
)

// bcConsumer polls a Business Central OData entity per active connection and
// publishes the results. It is an SDK Consumer: Configure wires deps, Run
// subscribes to command subjects and blocks, Stop cancels pollers.
type bcConsumer struct {
	sdk.BaseConsumer

	db          *sql.DB
	nc          *nats.Conn
	publish     sdk.PublishFunc
	logger      *slog.Logger
	checkpoints checkpoint.Store

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

	// Incremental polling: remember the newest CursorField value published and
	// ask only for what changed since. Off by default — turning it on changes
	// what a running pipeline delivers, which is the connection owner's call.
	Incremental bool   `json:"incremental"`
	CursorField string `json:"cursor_field"`

	// PageSize asks Business Central to page the response, via
	// `Prefer: odata.maxpagesize`. Server-driven paging is the only thing that
	// makes BC emit @odata.nextLink — $top caps the result set instead of
	// paging it. Unset leaves BC on its own default, which returns most
	// entities whole.
	PageSize int `json:"page_size"`

	// NodeID keys the watermark. It comes from the connection's node, not from
	// the node's own config, so it is never carried in the stored JSON.
	NodeID string `json:"-"`

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
	if c.checkpoints == nil {
		c.checkpoints = checkpoint.NewPostgresStore(res.DB)
	}
	c.active = make(map[string]context.CancelFunc)
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	// Aux endpoint for the UI's pre-deploy "show data structure" preview.
	c.RegisterHTTPHandler("/sample-data/", c.handleSampleData())
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
	cursor := ""
	if cfg.Incremental {
		cursor = c.loadCursor(ctx, tenantID, connID, cfg.NodeID, logger)
	}

	next := cfg.entityURL(cursor)
	page, total := 0, 0
	var watermark cursorTracker
	for next != "" {
		page++
		body, err := c.get(ctx, tok, next, cfg.PageSize)
		if err != nil {
			return err
		}
		var p odataPage
		if err := json.Unmarshal(body, &p); err != nil {
			return fmt.Errorf("parse OData page: %w", err)
		}
		if len(p.Value) > 0 {
			if cfg.Incremental {
				watermark.observe(p.Value, cfg.effectiveCursorField())
			}
			if err := c.publishRecords(ctx, connID, tenantID, cfg.effectiveEntity(), p.Value); err != nil {
				return fmt.Errorf("publish records: %w", err)
			}
			total += len(p.Value)
		}
		next = p.NextLink
	}

	// Advance only once every page has landed. A fetch that dies halfway
	// resumes from the old watermark — the records it already published arrive
	// twice, which the pipeline is built for, rather than being skipped, which
	// it is not.
	if cfg.Incremental && watermark.raw != "" {
		c.saveCursor(ctx, tenantID, connID, cfg.NodeID, watermark.raw, int64(total), logger)
	}

	logger.Info("Business Central fetch complete",
		"entity", cfg.effectiveEntity(), "records", total, "pages", page,
		"incremental", cfg.Incremental, "since", cursor)
	return nil
}

// cursorTracker keeps the newest cursor-field value seen in a fetch.
//
// The raw string is kept beside the parsed time because that string came from
// Business Central, so it is by construction a datetime literal BC accepts
// back in a $filter. Comparison goes through the parsed time: "…:00.5Z" sorts
// before "…:00Z" as text while being the later instant.
type cursorTracker struct {
	newest time.Time
	raw    string
}

func (t *cursorTracker) observe(records []json.RawMessage, field string) {
	for _, rec := range records {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rec, &fields); err != nil {
			continue
		}
		raw, ok := fields[field]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			continue
		}
		if parsed.After(t.newest) {
			t.newest, t.raw = parsed, s
		}
	}
}

// loadCursor returns the stored watermark, or "" for a first run. A store that
// cannot be read is logged and treated as a first run: a full re-read is
// noisy, whereas refusing to poll is an outage.
func (c *bcConsumer) loadCursor(ctx context.Context, tenantID, connID, nodeID string, logger *slog.Logger) string {
	if c.checkpoints == nil || nodeID == "" {
		return ""
	}
	cp, err := c.checkpoints.Get(ctx, tenantID, connID, nodeID)
	if err != nil {
		logger.Error("read Business Central watermark; polling from the start", "error", err)
		return ""
	}
	if cp == nil {
		return ""
	}
	return cp.LastProcessedMessageID
}

func (c *bcConsumer) saveCursor(ctx context.Context, tenantID, connID, nodeID, cursor string, count int64, logger *slog.Logger) {
	if c.checkpoints == nil || nodeID == "" {
		return
	}
	err := c.checkpoints.Save(ctx, &checkpoint.Checkpoint{
		TenantID:               tenantID,
		ConnectionID:           connID,
		NodeID:                 nodeID,
		LastProcessedMessageID: cursor,
		LastProcessedAt:        time.Now().UTC(),
		MessageCount:           count,
	})
	if err != nil {
		// The records are already published; failing to remember how far we got
		// costs a repeat next poll, not data.
		logger.Error("save Business Central watermark", "error", err, "cursor", cursor)
	}
}

func (c *bcConsumer) get(ctx context.Context, tok *oauthcc.Client, fullURL string, pageSize int) ([]byte, error) {
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
	if pageSize > 0 {
		// A request, not a command: BC may cap it, and says what it applied in
		// the Preference-Applied response header. Whatever it settles on, a
		// page smaller than the result set is what produces @odata.nextLink.
		req.Header.Set("Prefer", fmt.Sprintf("odata.maxpagesize=%d", pageSize))
	}
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

func (cfg *BCConfig) effectiveCursorField() string {
	if cfg.CursorField != "" {
		return cfg.CursorField
	}
	return defaultCursorField
}

// filterWithCursor composes the configured $filter with the incremental
// watermark. The configured filter is parenthesised so an `or` inside it
// cannot swallow the cursor clause: `a or b and cursor` binds as
// `a or (b and cursor)`, which would re-read everything matching `a`.
//
// `gt` rather than `ge`: the watermark is the newest value already published,
// so `ge` would redeliver it on every poll forever. The cost is a record
// written in the same instant as the watermark but after the fetch read it —
// BC timestamps to the millisecond, so that is a narrow window, and it is the
// standard trade for a timestamp watermark.
func (cfg *BCConfig) filterWithCursor(cursor string) string {
	if cursor == "" {
		return cfg.Filter
	}
	clause := fmt.Sprintf("%s gt %s", cfg.effectiveCursorField(), cursor)
	if cfg.Filter == "" {
		return clause
	}
	return fmt.Sprintf("(%s) and %s", cfg.Filter, clause)
}

// entityURL builds the first-page API v2.0 URL, scoped to the company. A
// non-empty cursor narrows it to what changed since the last complete fetch.
func (cfg *BCConfig) entityURL(cursor string) string {
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
	if filter := cfg.filterWithCursor(cursor); filter != "" {
		u += "?$filter=" + url.QueryEscape(filter)
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
			nc.BusinessCentral.NodeID = n.ID
			return nc.BusinessCentral, nil
		}
	}
	return nil, errors.New("no business_central consumer node found")
}
