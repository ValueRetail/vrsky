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
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// frontSystemsConsumer receives Front Systems webhooks and (optionally)
// registers its callback URL for the configured event types. It is an SDK
// Consumer: Configure wires deps + the webhook handler, Run subscribes to the
// command subjects and blocks.
type frontSystemsConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	httpClient *http.Client

	// resolveTenant maps a connection id to its owning tenant (webhook routing).
	// Defaulted in Configure to the DB lookup; tests inject a stub.
	resolveTenant func(connID string) (string, error)

	// active holds the connections this process has started (and registered
	// webhooks for, when a callback URL is set), by connection ID. starts
	// keeps two starts of one connection (boot restore and a start command,
	// say) from both registering its webhooks, and lets a stop that arrives
	// mid-start win (#397, #429). Both under mu.
	mu     sync.Mutex
	active map[string]struct{}
	starts sdk.StartGuard

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// FrontSystemsConfig is the per-node configuration (config.front_systems). The
// two API keys are stored as `*_secret_id` references and resolved to plaintext
// at connection-start time.
type FrontSystemsConfig struct {
	BaseURL         string   `json:"base_url"`         // per-partner Azure APIM host (required)
	SubscriptionKey string   `json:"subscription_key"` // from subscription_key_secret_id
	APIKey          string   `json:"api_key"`          // from api_key_secret_id
	Events          []string `json:"events"`           // event types to register (e.g. SaleCreated)
	CallbackURL     string   `json:"callback_url"`     // our public callback; if set → auto-register
}

type nodeConfig struct {
	Type         string              `json:"type"`
	FrontSystems *FrontSystemsConfig `json:"front_systems"`
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
func (c *frontSystemsConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("front-systems-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	c.db = res.DB
	c.nc = res.NATS
	c.logger = res.Logger
	if c.httpClient == nil {
		c.httpClient = &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if c.resolveTenant == nil {
		c.resolveTenant = c.getConnectionTenant
	}

	// Real-time events: Front Systems POSTs to /frontsystems/events/{connectionID}.
	c.RegisterHTTPHandler("/frontsystems/events/", c.handleWebhook())

	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx done.
func (c *frontSystemsConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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
	// Bring back what the database says is running: start/stop are NATS commands
	// and are not persisted, so without this a restart silently stopped every
	// pipeline on this service until someone redeployed it.
	sdk.RestoreRunning(ctx, c.db, c.logger, "front_systems", c.startConnection)

	<-ctx.Done()
	return nil
}

// Stop unsubscribes. Webhook registrations in Front Systems persist across
// restarts (they're server-side), so we don't tear them down here.
func (c *frontSystemsConsumer) Stop(ctx context.Context) error {
	if c.startSub != nil {
		_ = c.startSub.Unsubscribe()
	}
	if c.stopSub != nil {
		_ = c.stopSub.Unsubscribe()
	}
	c.mu.Lock()
	c.active = nil
	// A start still in flight gives up instead of counting as started.
	c.starts.StopAllLocked()
	c.mu.Unlock()
	return nil
}

func (c *frontSystemsConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse start command", "error", err)
		return
	}
	c.startConnection(context.Background(), cmd.ConnectionID, cmd.TenantID)
}

// startConnection is the start command's work, also what RestoreRunning calls on
// boot for every pipeline the database says is running (plans/stable-connections.md).
func (c *frontSystemsConsumer) startConnection(ctx context.Context, connectionID, tenantID string) {
	logger := c.logger.With("connection_id", connectionID, "tenant_id", tenantID)

	// Restore and the start-command handler call this on different goroutines.
	// The check and the claim are one critical section, so a second start for
	// the same connection stops here instead of registering the webhooks again
	// (#397, #429). A start refused while another is in flight runs again if
	// that one gives up without activating (#424).
	claim, ok := c.starts.ReserveOrRetry(&c.mu, tenantID, connectionID,
		func() bool { _, ok := c.active[connectionID]; return ok },
		func() { c.startConnection(ctx, connectionID, tenantID) })
	if !ok {
		logger.Warn("Front Systems connection already started or starting")
		return
	}
	// Frees the claim on every early return below; a no-op once active.
	defer claim.Release()

	cfg, err := c.getConfig(context.Background(), connectionID, tenantID)
	if err != nil {
		logger.Debug("Not a Front Systems consumer for this connection", "error", err)
		return
	}
	// Auto-register our callback for the configured events, if a callback URL is
	// set. If not, the webhook is expected to be registered out of band and this
	// connector just receives on /frontsystems/events/{id}. A failed
	// registration leaves the connection not started, so the next start (or a
	// start refused meanwhile) tries again.
	if cfg.CallbackURL != "" && len(cfg.Events) > 0 {
		if err := c.registerWebhooks(context.Background(), cfg, logger); err != nil {
			logger.Error("Front Systems webhook registration failed", "error", err)
			return
		}
	}
	// A stop that arrived during the work above has taken the claim back and
	// wins. Registrations are server-side and a stop never removes them, so
	// there is nothing to undo here.
	if !claim.Activate(func() {
		if c.active == nil {
			c.active = make(map[string]struct{})
		}
		c.active[connectionID] = struct{}{}
	}) {
		logger.Info("Start abandoned: a stop arrived while it was in progress")
	}
}

// handleStopCommand forgets the connection, so its next start registers the
// webhooks again, and makes a start in progress give up. There are no
// per-connection resources to tear down (registrations are server-side).
func (c *frontSystemsConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse stop command", "error", err)
		return
	}
	c.mu.Lock()
	delete(c.active, cmd.ConnectionID)
	starting := c.starts.StopLocked(cmd.ConnectionID)
	c.mu.Unlock()
	if starting {
		c.logger.Info("Stop arrived while the connection was starting; the start is abandoned", "connection_id", cmd.ConnectionID)
	}
}

// registerWebhooks subscribes our callback URL for each configured event type
// via POST {base}/api/webhooks {"event":..., "url":...}.
func (c *frontSystemsConsumer) registerWebhooks(ctx context.Context, cfg *FrontSystemsConfig, logger *slog.Logger) error {
	for _, event := range cfg.Events {
		body, _ := json.Marshal(map[string]string{"event": event, "url": strings.TrimRight(cfg.CallbackURL, "/")})
		status, resp, err := c.do(ctx, cfg, http.MethodPost, "/api/webhooks", body)
		if err != nil {
			return fmt.Errorf("register %s: %w", event, err)
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("register %s: HTTP %d: %s", event, status, snippet(resp))
		}
		logger.Info("Registered Front Systems webhook", "event", event)
	}
	return nil
}

// do performs a Front Systems API request with the two required auth headers.
func (c *frontSystemsConsumer) do(ctx context.Context, cfg *FrontSystemsConfig, method, path string, body []byte) (int, []byte, error) {
	if cfg.BaseURL == "" {
		return 0, nil, errors.New("front_systems base_url is required (per-partner Azure APIM host)")
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.BaseURL, "/")+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", cfg.SubscriptionKey)
	req.Header.Set("x-api-key", cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, out, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// --- DB helpers ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Front Systems consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *frontSystemsConsumer) getConfig(ctx context.Context, connectionID, tenantID string) (*FrontSystemsConfig, error) {
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
		if nc.Type == "front_systems" && nc.FrontSystems != nil {
			return nc.FrontSystems, nil
		}
	}
	return nil, errors.New("no front_systems consumer node found")
}

// getConnectionTenant resolves the owning tenant for a connection id (webhook
// routing). lint:tenant-ok — resolves a connection's own tenant by PK.
func (c *frontSystemsConsumer) getConnectionTenant(connectionID string) (string, error) {
	var tenantID string
	err := c.db.QueryRow(`SELECT tenant_id FROM connections WHERE id = $1`, connectionID).Scan(&tenantID)
	return tenantID, err
}
