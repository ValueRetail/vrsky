package main

import (
	"context"
	"database/sql"
	"encoding/base64"
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
	defaultODataVersion = "v2"
	authBasic           = "basic"
	authOAuth2          = "oauth2"
)

// sapConsumer polls an SAP S/4HANA OData entity set per active connection and
// publishes the results. It is an SDK Consumer: Configure wires deps, Run
// subscribes to command subjects and blocks, Stop cancels pollers.
type sapConsumer struct {
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

// SAPConfig is the per-node configuration (config.sap_s4hana). Credentials are
// stored as *_secret_id and resolved to plaintext at connection-start time:
// Basic auth uses password (password_secret_id); OAuth2 client-credentials uses
// client_secret (client_secret_secret_id).
type SAPConfig struct {
	Host         string `json:"host"`          // e.g. my347623.s4hana.ondemand.com
	APIBaseURL   string `json:"api_base_url"`  // optional full service-root override (on-prem / OData v4)
	Service      string `json:"service"`       // OData service, e.g. API_SALES_ORDER_SRV
	EntitySet    string `json:"entity_set"`    // entity set, e.g. A_SalesOrder
	ODataVersion string `json:"odata_version"` // v2 (default) | v4
	SAPClient    string `json:"sap_client"`    // optional mandt (sent as sap-client)
	Filter       string `json:"filter"`        // optional OData $filter

	AuthType     string `json:"auth_type"`     // basic (default) | oauth2
	Username     string `json:"username"`      // basic: communication user
	Password     string `json:"password"`      // basic: from password_secret_id
	ClientID     string `json:"client_id"`     // oauth2
	ClientSecret string `json:"client_secret"` // oauth2: from client_secret_secret_id
	TokenURL     string `json:"token_url"`     // oauth2: default https://{host}/sap/bc/sec/oauth2/token
	Scope        string `json:"scope"`         // oauth2: optional

	PollIntervalSeconds int `json:"poll_interval_seconds"`
}

type nodeConfig struct {
	Type string     `json:"type"`
	SAP  *SAPConfig `json:"sap_s4hana"`
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

func (c *sapConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("sap-s4hana-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	c.db = res.DB
	c.nc = res.NATS
	c.logger = res.Logger
	c.active = make(map[string]context.CancelFunc)
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	// Aux endpoint for the UI's pre-deploy "show data structure" preview.
	c.RegisterHTTPHandler("/sample-data/", c.handleSampleData())
	res.Health.SetReady(true)
	return nil
}

func (c *sapConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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

func (c *sapConsumer) Stop(ctx context.Context) error {
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

func (c *sapConsumer) handleStartCommand(msg *nats.Msg) {
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
		logger.Debug("Not an SAP S/4HANA consumer for this connection", "error", err)
		return
	}
	if err := cfg.validate(); err != nil {
		logger.Error("SAP S/4HANA config incomplete", "error", err)
		return
	}
	if cfg.PollIntervalSeconds <= 0 {
		logger.Error("SAP S/4HANA consumer needs poll_interval_seconds > 0")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.active[cmd.ConnectionID] = cancel
	c.mu.Unlock()

	logger.Info("Starting SAP S/4HANA poller", "service", cfg.Service, "entity_set", cfg.EntitySet, "interval", cfg.PollIntervalSeconds)
	go c.runPoller(ctx, cmd.ConnectionID, cmd.TenantID, cfg)
}

func (c *sapConsumer) handleStopCommand(msg *nats.Msg) {
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

func (c *sapConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *SAPConfig) {
	logger := c.logger.With("connection_id", connID)
	auth := newAuthorizer(cfg, c.httpClient)

	poll := func() {
		if err := c.fetchAndPublish(ctx, connID, tenantID, cfg, auth, logger); err != nil && ctx.Err() == nil {
			logger.Error("SAP S/4HANA fetch failed", "error", err)
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

// OData response envelopes. S/4HANA APIs are predominantly OData v2 (d.results +
// d.__next); newer v4 services use value + @odata.nextLink.
type odataV2Page struct {
	D struct {
		Results []json.RawMessage `json:"results"`
		Next    string            `json:"__next"`
	} `json:"d"`
}

type odataV4Page struct {
	Value    []json.RawMessage `json:"value"`
	NextLink string            `json:"@odata.nextLink"`
}

// fetchAndPublish GETs the entity set, follows the server-driven pagination link
// (__next / @odata.nextLink, an opaque $skiptoken cursor), and publishes each
// page's records as one JSON-array envelope.
func (c *sapConsumer) fetchAndPublish(ctx context.Context, connID, tenantID string, cfg *SAPConfig, auth authorizer, logger *slog.Logger) error {
	next := cfg.entityURL()
	page, total := 0, 0
	for next != "" {
		page++
		body, reqURL, err := c.get(ctx, cfg, auth, next)
		if err != nil {
			return err
		}
		records, nextLink, perr := parsePage(cfg.effectiveVersion(), body, reqURL)
		if perr != nil {
			return perr
		}
		if len(records) > 0 {
			if err := c.publishRecords(ctx, connID, tenantID, cfg, records); err != nil {
				return fmt.Errorf("publish records: %w", err)
			}
			total += len(records)
		}
		next = nextLink
	}
	logger.Info("SAP S/4HANA fetch complete", "service", cfg.Service, "entity_set", cfg.EntitySet, "records", total, "pages", page)
	return nil
}

// parsePage decodes one OData page and resolves the (possibly relative) next
// link against the request URL.
func parsePage(version string, body []byte, reqURL *url.URL) ([]json.RawMessage, string, error) {
	if version == "v4" {
		var p odataV4Page
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, "", fmt.Errorf("parse OData v4 page: %w", err)
		}
		return p.Value, resolveNext(reqURL, p.NextLink), nil
	}
	var p odataV2Page
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "", fmt.Errorf("parse OData v2 page: %w", err)
	}
	return p.D.Results, resolveNext(reqURL, p.D.Next), nil
}

// resolveNext turns a relative __next/nextLink into an absolute URL.
func resolveNext(base *url.URL, next string) string {
	if next == "" {
		return ""
	}
	if ref, err := url.Parse(next); err == nil && base != nil {
		return base.ResolveReference(ref).String()
	}
	return next
}

func (c *sapConsumer) get(ctx context.Context, cfg *SAPConfig, auth authorizer, fullURL string) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := auth.apply(ctx, req); err != nil {
		return nil, nil, fmt.Errorf("authorize: %w", err)
	}
	// v2 defaults to Atom/XML; force JSON.
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("sap s/4hana %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, req.URL, nil
}

func (c *sapConsumer) publishRecords(ctx context.Context, connID, tenantID string, cfg *SAPConfig, records []json.RawMessage) error {
	payload, err := json.Marshal(records)
	if err != nil {
		return err
	}
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = "application/json"
	env.Source = "sap-s4hana-consumer"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.StepHistory = []string{"sap-s4hana-consumer"}
	env.Metadata = map[string]interface{}{"service": cfg.Service, "entity_set": cfg.EntitySet, "record_count": len(records)}
	// last_payload (for the UI's "show data structure" preview) is written
	// centrally by the SDK publish path now, so every connector gets it.
	return c.publish(ctx, env)
}

// --- auth ---

// authorizer sets the Authorization header on a request. Basic auth is a fixed
// header; OAuth2 client-credentials fetches (and caches) a bearer token.
type authorizer interface {
	apply(ctx context.Context, req *http.Request) error
}

type basicAuth struct{ header string }

func (b basicAuth) apply(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", b.header)
	return nil
}

type bearerAuth struct{ tok *oauthcc.Client }

func (o bearerAuth) apply(ctx context.Context, req *http.Request) error {
	access, err := o.tok.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	return nil
}

func newAuthorizer(cfg *SAPConfig, hc *http.Client) authorizer {
	if cfg.effectiveAuthType() == authOAuth2 {
		tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(hc)
		return bearerAuth{tok: tok}
	}
	creds := base64.StdEncoding.EncodeToString([]byte(cfg.Username + ":" + cfg.Password))
	return basicAuth{header: "Basic " + creds}
}

// --- config helpers ---

func (cfg *SAPConfig) effectiveVersion() string {
	if v := strings.ToLower(strings.TrimSpace(cfg.ODataVersion)); v == "v4" || v == "v2" {
		return v
	}
	return defaultODataVersion
}

func (cfg *SAPConfig) effectiveAuthType() string {
	if strings.ToLower(strings.TrimSpace(cfg.AuthType)) == authOAuth2 {
		return authOAuth2
	}
	return authBasic
}

func (cfg *SAPConfig) effectiveTokenURL() string {
	if cfg.TokenURL != "" {
		return cfg.TokenURL
	}
	return fmt.Sprintf("https://%s/sap/bc/sec/oauth2/token", cfg.Host)
}

// serviceRoot is the OData service base, e.g.
// https://{host}/sap/opu/odata/sap/{service}. api_base_url overrides it (on-prem
// or OData v4, whose path differs).
func (cfg *SAPConfig) serviceRoot() string {
	if cfg.APIBaseURL != "" {
		return strings.TrimRight(cfg.APIBaseURL, "/")
	}
	return fmt.Sprintf("https://%s/sap/opu/odata/sap/%s", cfg.Host, cfg.Service)
}

func (cfg *SAPConfig) entityURL() string {
	u := cfg.serviceRoot() + "/" + strings.Trim(cfg.EntitySet, "/")
	q := url.Values{}
	if cfg.SAPClient != "" {
		q.Set("sap-client", cfg.SAPClient)
	}
	if cfg.Filter != "" {
		q.Set("$filter", cfg.Filter)
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (cfg *SAPConfig) validate() error {
	if cfg.APIBaseURL == "" && (cfg.Host == "" || cfg.Service == "") {
		return errors.New("need host + service (or api_base_url)")
	}
	if cfg.EntitySet == "" {
		return errors.New("need entity_set")
	}
	if cfg.effectiveAuthType() == authOAuth2 {
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			return errors.New("oauth2 auth needs client_id + client_secret (from client_secret_secret_id)")
		}
	} else if cfg.Username == "" || cfg.Password == "" {
		return errors.New("basic auth needs username + password (from password_secret_id)")
	}
	return nil
}

// --- DB ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its SAP S/4HANA consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *sapConsumer) getConfig(ctx context.Context, connectionID, tenantID string) (*SAPConfig, error) {
	var nodesJSON json.RawMessage
	if err := c.db.QueryRowContext(ctx,
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
		if nc.Type == "sap_s4hana" && nc.SAP != nil {
			return nc.SAP, nil
		}
	}
	return nil, errors.New("no sap_s4hana consumer node found")
}
