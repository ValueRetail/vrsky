package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const reconnectBackoff = 2 * time.Second

// rabbitConsumer consumes from a RabbitMQ queue per active connection and
// publishes each message into the pipeline, acking only after a successful
// publish. SDK Consumer: Configure wires deps, Run subscribes to command
// subjects and blocks, Stop cancels the per-connection loops.
type rabbitConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	// dial opens an AMQP source. Defaulted to realDial in Configure; tests
	// inject a fake so the loop runs without a broker.
	dial dialFunc
	// sample peeks one message off the queue for the schema-preview endpoint
	// (#144). Defaulted to realRabbitSample in Configure; tests stub it.
	sample rabbitSampleFunc

	active map[string]context.CancelFunc
	mu     sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// RabbitMQConfig is the per-node configuration (config.rabbitmq). Password is
// resolved from password_secret_id before use.
type RabbitMQConfig struct {
	URL          string `json:"url"` // amqp://host:5672 (creds optional in URL)
	Username     string `json:"username"`
	Password     string `json:"password"` // resolved from password_secret_id
	Exchange     string `json:"exchange"`
	ExchangeType string `json:"exchange_type"` // default topic when exchange set
	Queue        string `json:"queue"`
	RoutingKey   string `json:"routing_key"`
}

type nodeConfig struct {
	Type     string          `json:"type"`
	RabbitMQ *RabbitMQConfig `json:"rabbitmq"`
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
func (c *rabbitConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("rabbitmq-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	c.db = res.DB
	c.nc = res.NATS
	c.logger = res.Logger
	c.active = make(map[string]context.CancelFunc)
	if c.dial == nil {
		c.dial = realDial
	}
	if c.sample == nil {
		c.sample = realRabbitSample
	}
	c.RegisterHTTPHandler("/test-connection/", c.handleTestConnection())
	c.RegisterHTTPHandler("/sample-data/", c.handleSampleData())
	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled. Per-connection consume loops are driven from the handlers.
func (c *rabbitConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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

// Stop cancels all consume loops. The SDK runner calls this after Run returns.
func (c *rabbitConsumer) Stop(ctx context.Context) error {
	if c.startSub != nil {
		_ = c.startSub.Unsubscribe()
	}
	if c.stopSub != nil {
		_ = c.stopSub.Unsubscribe()
	}
	c.mu.Lock()
	for id, cancel := range c.active {
		c.logger.Info("Stopping RabbitMQ consumer", "connection_id", id)
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

func (c *rabbitConsumer) handleStartCommand(msg *nats.Msg) {
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
		logger.Warn("RabbitMQ consumer already running")
		return
	}

	cfg, ok := c.getConfig(cmd.ConnectionID, cmd.TenantID)
	if !ok {
		logger.Debug("Not a RabbitMQ consumer for this connection, ignoring")
		return
	}
	if cfg.URL == "" || cfg.Queue == "" {
		logger.Error("RabbitMQ config incomplete (need url, queue)")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.active[cmd.ConnectionID] = cancel
	c.mu.Unlock()
	_ = c.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running")

	logger.Info("Starting RabbitMQ consumer", "queue", cfg.Queue, "exchange", cfg.Exchange)
	go c.consumeLoop(ctx, cmd.ConnectionID, cmd.TenantID, cfg, logger)
}

func (c *rabbitConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		c.logger.Error("parse stop command", "error", err)
		return
	}
	c.mu.Lock()
	cancel, exists := c.active[cmd.ConnectionID]
	if exists {
		cancel()
		delete(c.active, cmd.ConnectionID)
	}
	c.mu.Unlock()
	if exists {
		_ = c.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped")
		c.logger.Info("RabbitMQ consumer stopped", "connection_id", cmd.ConnectionID)
	}
}

// consumeLoop (re)connects and forwards deliveries, acking only after a
// successful publish (acceptance criterion #1). A dropped connection reconnects
// after a short backoff.
func (c *rabbitConsumer) consumeLoop(ctx context.Context, connID, tenantID string, cfg *RabbitMQConfig, logger *slog.Logger) {
	for {
		if ctx.Err() != nil {
			return
		}
		src, err := c.dial(cfg)
		if err != nil {
			logger.Error("RabbitMQ connect failed; retrying", "error", err)
			if !sleepCtx(ctx, reconnectBackoff) {
				return
			}
			continue
		}
		c.drain(ctx, connID, tenantID, src, logger)
		src.Close()
		if ctx.Err() != nil {
			return
		}
		// drain returned without ctx cancel → connection error; reconnect.
		if !sleepCtx(ctx, reconnectBackoff) {
			return
		}
	}
}

func (c *rabbitConsumer) drain(ctx context.Context, connID, tenantID string, src amqpSource, logger *slog.Logger) {
	for {
		d, err := src.Next(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("RabbitMQ delivery error; will reconnect", "error", err)
			}
			return
		}
		if err := c.publishBody(ctx, connID, tenantID, d.Body); err != nil {
			logger.Error("publish failed; nacking for redelivery", "error", err)
			_ = d.nack()
			continue
		}
		if err := d.ack(); err != nil && ctx.Err() == nil {
			logger.Warn("ack failed after publish (message may redeliver)", "error", err)
		}
	}
}

func (c *rabbitConsumer) publishBody(ctx context.Context, connID, tenantID string, body []byte) error {
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = detectContentType(body)
	env.Source = "rabbitmq-consumer"
	env.Payload = body
	env.PayloadSize = int64(len(body))
	env.StepHistory = []string{"rabbitmq-consumer"}
	env.Metadata = map[string]interface{}{}
	return c.publish(ctx, env)
}

// --- DB helpers ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// the RabbitMQ consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (c *rabbitConsumer) getConfig(connectionID, tenantID string) (*RabbitMQConfig, bool) {
	var nodesJSON json.RawMessage
	err := c.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON)
	if err != nil {
		c.logger.Debug("connection not found", "error", err, "connection_id", connectionID)
		return nil, false
	}

	resolved, err := crypto.ResolveSecretsInJSON(context.Background(), crypto.NewSQLSecretReader(c.db), tenantID, nodesJSON)
	if err != nil {
		c.logger.Error("resolve secrets", "error", err, "connection_id", connectionID)
		return nil, false
	}

	var nodes []node
	if err := json.Unmarshal(resolved, &nodes); err != nil {
		c.logger.Warn("parse nodes", "error", err, "connection_id", connectionID)
		return nil, false
	}
	for _, n := range nodes {
		if n.Type != "consumer" {
			continue
		}
		var nc nodeConfig
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type == "rabbitmq" && nc.RabbitMQ != nil {
			return nc.RabbitMQ, true
		}
	}
	return nil, false
}

func (c *rabbitConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := c.db.Exec(query, status, connectionID, tenantID)
	return err
}

// sleepCtx waits for d or ctx cancellation; returns false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func detectContentType(data []byte) string {
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}
	return "application/octet-stream"
}
