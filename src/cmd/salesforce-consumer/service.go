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
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthtoken"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const defaultAPIVersion = "v60.0"

// salesforceConsumer polls Salesforce via SOQL per active connection and
// publishes the resulting records into the pipeline. It is an SDK Consumer:
// Configure wires deps, Run subscribes to command subjects and blocks, Stop
// cancels pollers.
type salesforceConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	httpClient *http.Client
	tokens     *oauthtoken.Client
	// resolveToken returns an access token for a grant. Defaulted in Configure
	// to the oauthtoken client; tests inject a stub (so the consumer can run
	// without a live management-api).
	resolveToken func(ctx context.Context, tenantID, grantID string, force bool) (string, error)

	active map[string]context.CancelFunc
	mu     sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// SalesforceConfig is the per-node configuration (config.salesforce).
type SalesforceConfig struct {
	InstanceURL         string `json:"instance_url"`   // e.g. https://myorg.my.salesforce.com
	OAuthGrantID        string `json:"oauth_grant_id"` // grant to authenticate with (#75)
	SOQL                string `json:"soql"`           // e.g. SELECT Id, Name FROM Account
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	APIVersion          string `json:"api_version"` // optional, default v60.0
}

type nodeConfig struct {
	Type       string            `json:"type"`
	Salesforce *SalesforceConfig `json:"salesforce"`
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
func (s *salesforceConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("salesforce-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.active = make(map[string]context.CancelFunc)
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	s.tokens = oauthtoken.New(os.Getenv("MGMT_API_URL"), os.Getenv("OAUTH_TOKEN_SERVICE_SECRET"))
	if s.resolveToken == nil {
		s.resolveToken = func(ctx context.Context, tenantID, grantID string, force bool) (string, error) {
			if !s.tokens.Configured() {
				return "", errors.New("OAuth token resolution not configured (set MGMT_API_URL + OAUTH_TOKEN_SERVICE_SECRET)")
			}
			if force {
				return s.tokens.ForceToken(ctx, tenantID, grantID)
			}
			return s.tokens.Token(ctx, tenantID, grantID)
		}
	}
	// Aux HTTP endpoints for the UI's schema discovery / field mapping (#79).
	s.RegisterHTTPHandler("/schema/", s.handleSchema())
	s.RegisterHTTPHandler("/sample-data/", s.handleSampleData())

	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled. Per-connection polling is driven from the command handlers.
func (s *salesforceConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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
func (s *salesforceConsumer) Stop(ctx context.Context) error {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}
	s.mu.Lock()
	for id, cancel := range s.active {
		s.logger.Info("Stopping salesforce poller", "connection_id", id)
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

func (s *salesforceConsumer) handleStartCommand(msg *nats.Msg) {
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
		logger.Warn("Salesforce poller already running")
		return
	}

	cfg, err := s.getSalesforceConfig(cmd.ConnectionID, cmd.TenantID)
	if err != nil {
		logger.Debug("Not a Salesforce consumer for this connection", "error", err)
		return
	}
	if cfg.InstanceURL == "" || cfg.OAuthGrantID == "" || cfg.SOQL == "" {
		logger.Error("Salesforce config incomplete (need instance_url, oauth_grant_id, soql)")
		return
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = defaultAPIVersion
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.active[cmd.ConnectionID] = cancel
	s.mu.Unlock()
	_ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running")

	logger.Info("Starting Salesforce poller", "instance_url", cfg.InstanceURL, "interval", cfg.PollIntervalSeconds)
	go s.runPoller(ctx, cmd.ConnectionID, cmd.TenantID, cfg)
}

func (s *salesforceConsumer) handleStopCommand(msg *nats.Msg) {
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
		_ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped")
		s.logger.Info("Salesforce poller stopped", "connection_id", cmd.ConnectionID)
	}
}

func (s *salesforceConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *SalesforceConfig) {
	logger := s.logger.With("connection_id", connID)
	if err := s.queryAndPublish(ctx, connID, tenantID, cfg, logger); err != nil && ctx.Err() == nil {
		logger.Error("Salesforce query failed", "error", err)
	}
	if cfg.PollIntervalSeconds <= 0 {
		return // one-shot
	}
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.queryAndPublish(ctx, connID, tenantID, cfg, logger); err != nil && ctx.Err() == nil {
				logger.Error("Salesforce query failed", "error", err)
			}
		}
	}
}

// queryResponse is the shape of the Salesforce REST query response.
type queryResponse struct {
	TotalSize      int               `json:"totalSize"`
	Done           bool              `json:"done"`
	NextRecordsURL string            `json:"nextRecordsUrl"`
	Records        []json.RawMessage `json:"records"`
}

// queryAndPublish runs the SOQL query (following pagination) and publishes each
// page's records as one JSON-array envelope.
func (s *salesforceConsumer) queryAndPublish(ctx context.Context, connID, tenantID string, cfg *SalesforceConfig, logger *slog.Logger) error {
	base := strings.TrimRight(cfg.InstanceURL, "/")
	next := fmt.Sprintf("/services/data/%s/query/?q=%s", cfg.APIVersion, url.QueryEscape(cfg.SOQL))

	page := 0
	total := 0
	for next != "" {
		page++
		body, err := s.get(ctx, tenantID, cfg.OAuthGrantID, base+next)
		if err != nil {
			return err
		}
		var qr queryResponse
		if err := json.Unmarshal(body, &qr); err != nil {
			return fmt.Errorf("parse query response: %w", err)
		}
		if len(qr.Records) > 0 {
			if err := s.publishRecords(ctx, connID, tenantID, qr.Records); err != nil {
				return fmt.Errorf("publish records: %w", err)
			}
			total += len(qr.Records)
		}
		if qr.Done || qr.NextRecordsURL == "" {
			break
		}
		next = qr.NextRecordsURL
	}
	logger.Info("Salesforce query complete", "records", total, "pages", page)
	return nil
}

// get performs an authenticated GET, refreshing the token once on a 401.
func (s *salesforceConsumer) get(ctx context.Context, tenantID, grantID, fullURL string) ([]byte, error) {
	do := func(force bool) (*http.Response, error) {
		tok, err := s.resolveToken(ctx, tenantID, grantID, force)
		if err != nil {
			return nil, fmt.Errorf("resolve OAuth token: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		return s.httpClient.Do(req)
	}

	resp, err := do(false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		s.logger.Info("Salesforce returned 401; refreshing token and retrying once", "grant_id", grantID)
		resp, err = do(true)
		if err != nil {
			return nil, fmt.Errorf("request after token refresh: %w", err)
		}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20)) // 16 MiB cap
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("salesforce %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (s *salesforceConsumer) publishRecords(ctx context.Context, connID, tenantID string, records []json.RawMessage) error {
	payload, err := json.Marshal(records)
	if err != nil {
		return err
	}
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = "application/json"
	env.Source = "salesforce-consumer"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.StepHistory = []string{"salesforce-consumer"}
	env.Metadata = map[string]interface{}{"record_count": len(records), "filename": "salesforce.json"}
	return s.publish(ctx, env)
}

// --- DB helpers ---

type connectionRow struct {
	Nodes json.RawMessage
}

// getSalesforceConfig loads the connection and extracts its Salesforce consumer
// node config. // lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (s *salesforceConsumer) getSalesforceConfig(connectionID, tenantID string) (*SalesforceConfig, error) {
	var row connectionRow
	err := s.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&row.Nodes)
	if err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
	}

	var nodes []node
	if err := json.Unmarshal(row.Nodes, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	for _, n := range nodes {
		if n.Type != "consumer" {
			continue
		}
		var nc nodeConfig
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type == "salesforce" && nc.Salesforce != nil {
			return nc.Salesforce, nil
		}
	}
	return nil, errors.New("no salesforce consumer node found")
}

func (s *salesforceConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	return err
}
