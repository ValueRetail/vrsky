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

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// rabbitProducer publishes pipeline messages to a RabbitMQ exchange/queue as
// persistent messages. SDK Producer: Configure wires deps, Deliver publishes
// one envelope to every matching RabbitMQ producer node for the connection.
type rabbitProducer struct {
	sdk.BaseProducer

	db     *sql.DB
	logger *slog.Logger

	dial dialFunc // defaulted to realDial; tests inject a fake

	cache     map[string][]*rabbitTarget
	cacheTime map[string]time.Time
	cacheTTL  time.Duration
	cacheMu   sync.RWMutex
}

// RabbitMQConfig is the per-node configuration (config.rabbitmq). Password is
// resolved from password_secret_id before use.
type RabbitMQConfig struct {
	URL          string `json:"url"`
	Username     string `json:"username"`
	Password     string `json:"password"` // resolved from password_secret_id
	Exchange     string `json:"exchange"`
	ExchangeType string `json:"exchange_type"`
	Queue        string `json:"queue"`
	RoutingKey   string `json:"routing_key"`
}

type rabbitTarget struct {
	cfg            RabbitMQConfig
	predecessorID  string
	predIsConsumer bool
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// Configure wires dependencies. Called once by the runner before Deliver.
func (p *rabbitProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("rabbitmq-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.dial == nil {
		p.dial = realDial
	}
	p.cache = make(map[string][]*rabbitTarget)
	p.cacheTime = make(map[string]time.Time)
	if p.cacheTTL == 0 {
		p.cacheTTL = 5 * time.Minute
	}
	p.logger.Info("rabbitmq-producer configured")
	return nil
}

// Deliver publishes the envelope to every matching RabbitMQ target. Transient
// failures (connect/publish) → sdk.Retriable. A missing producer config for the
// connection is not an error.
func (p *rabbitProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connID := env.IntegrationID
	if connID == "" {
		return nil
	}
	targets, err := p.getTargets(ctx, connID, env.TenantID)
	if err != nil {
		p.logger.Debug("No RabbitMQ producer config for connection", "connection_id", connID, "error", err)
		return nil
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	var transient error
	for _, t := range targets {
		if t.predIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !t.predIsConsumer && t.predecessorID != "" && lastProcessedBy != t.predecessorID {
			continue
		}
		if err := p.produce(ctx, &t.cfg, env.Payload, env.ContentType); err != nil && transient == nil {
			transient = err
		}
	}
	if transient != nil {
		return sdk.Retriable(transient)
	}
	return nil
}

func (p *rabbitProducer) produce(ctx context.Context, cfg *RabbitMQConfig, body []byte, contentType string) error {
	if cfg.URL == "" || (cfg.Exchange == "" && cfg.Queue == "") {
		p.logger.Error("RabbitMQ producer config incomplete (need url and exchange or queue); skipping")
		return nil
	}
	pub, err := p.dial(cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pub.Close()
	if err := pub.Publish(ctx, body, contentType); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	p.logger.Info("RabbitMQ message published", "exchange", cfg.Exchange, "routing_key", cfg.RoutingKey, "queue", cfg.Queue, "size", len(body))
	return nil
}

// getTargets loads the connection, resolves *_secret_id references, and returns
// every RabbitMQ producer node with its predecessor (for multi-node routing).
// lint:tenant-ok — connection lookup by PK; tenant scoping enforced on deploy.
func (p *rabbitProducer) getTargets(ctx context.Context, connID, tenantID string) ([]*rabbitTarget, error) {
	p.cacheMu.RLock()
	if ts, ok := p.cache[connID]; ok && time.Since(p.cacheTime[connID]) < p.cacheTTL {
		p.cacheMu.RUnlock()
		return ts, nil
	}
	p.cacheMu.RUnlock()

	var nodesJSON, edgesJSON []byte
	if err := p.db.QueryRowContext(ctx, `SELECT nodes, edges FROM connections WHERE id = $1`, connID).
		Scan(&nodesJSON, &edgesJSON); err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
	}

	resolved, err := crypto.ResolveSecretsInJSON(ctx, crypto.NewSQLSecretReader(p.db), tenantID, nodesJSON)
	if err != nil {
		return nil, fmt.Errorf("resolve secrets: %w", err)
	}

	var nodes []node
	if err := json.Unmarshal(resolved, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	var targets []*rabbitTarget
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		var nc struct {
			Type     string          `json:"type"`
			RabbitMQ *RabbitMQConfig `json:"rabbitmq"`
		}
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type != "rabbitmq" || nc.RabbitMQ == nil {
			continue
		}
		var predID string
		var predIsConsumer bool
		for _, e := range edges {
			if e.Target == n.ID {
				predID = e.Source
				for _, m := range nodes {
					if m.ID == predID && m.Type == "consumer" {
						predIsConsumer = true
						break
					}
				}
				break
			}
		}
		targets = append(targets, &rabbitTarget{cfg: *nc.RabbitMQ, predecessorID: predID, predIsConsumer: predIsConsumer})
	}
	if len(targets) == 0 {
		return nil, errors.New("no rabbitmq producer node found")
	}

	p.cacheMu.Lock()
	p.cache[connID] = targets
	p.cacheTime[connID] = time.Now()
	p.cacheMu.Unlock()
	return targets, nil
}

// ServesConnection reports whether this connection has a matching destination —
// mirroring Deliver's own "no config -> not ours" semantics — so the SDK can
// ack foreign connections before rehydrating large payloads (sdk.ConnectionScoped).
func (p *rabbitProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	targets, err := p.getTargets(ctx, connectionID, tenantID)
	if err != nil {
		return false // Deliver treats lookup errors as "not ours" too
	}
	return len(targets) > 0
}
