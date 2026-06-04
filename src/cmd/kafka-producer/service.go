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

// kafkaProducer publishes pipeline messages to a Kafka topic. SDK Producer:
// Configure wires deps, Deliver writes one envelope to every matching Kafka
// producer node for the connection (acks=all).
type kafkaProducer struct {
	sdk.BaseProducer

	db     *sql.DB
	logger *slog.Logger

	// newWriter opens a writer. Defaulted to realWriter in Configure; tests
	// inject a fake so Deliver runs without a broker.
	newWriter writerFactory

	cache     map[string][]*kafkaTarget
	cacheTime map[string]time.Time
	cacheTTL  time.Duration
	cacheMu   sync.RWMutex
}

// KafkaConfig is the per-node configuration (config.kafka). Password and
// ClientKey are resolved from *_secret_id references before use.
type KafkaConfig struct {
	Brokers    []string `json:"brokers"`
	Topic      string   `json:"topic"`
	AuthType   string   `json:"auth_type"` // none|sasl-plain|sasl-scram-256|sasl-scram-512|mtls
	Username   string   `json:"username"`
	Password   string   `json:"password"`    // resolved from password_secret_id
	CACert     string   `json:"ca_cert"`     // PEM (public)
	ClientCert string   `json:"client_cert"` // PEM (public)
	ClientKey  string   `json:"client_key"`  // resolved from client_key_secret_id
}

type kafkaTarget struct {
	cfg            KafkaConfig
	predecessorID  string
	predIsConsumer bool
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// Configure wires dependencies. Called once by the runner before Deliver.
func (p *kafkaProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("kafka-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.newWriter == nil {
		p.newWriter = realWriter
	}
	p.cache = make(map[string][]*kafkaTarget)
	p.cacheTime = make(map[string]time.Time)
	if p.cacheTTL == 0 {
		p.cacheTTL = 5 * time.Minute
	}
	p.logger.Info("kafka-producer configured")
	return nil
}

// Deliver publishes the envelope to every matching Kafka target. Transient
// failures (connect/write) → sdk.Retriable. A missing producer config for the
// connection is not an error — this binary just isn't the producer for it.
func (p *kafkaProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connID := env.IntegrationID
	if connID == "" {
		return nil
	}
	targets, err := p.getTargets(ctx, connID, env.TenantID)
	if err != nil {
		p.logger.Debug("No Kafka producer config for connection", "connection_id", connID, "error", err)
		return nil
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	var key []byte
	if env.Metadata != nil {
		if v, ok := env.Metadata["kafka_key"].(string); ok && v != "" {
			key = []byte(v)
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
		if err := p.produce(ctx, &t.cfg, key, env.Payload); err != nil && transient == nil {
			transient = err
		}
	}
	if transient != nil {
		return sdk.Retriable(transient)
	}
	return nil
}

func (p *kafkaProducer) produce(ctx context.Context, cfg *KafkaConfig, key, value []byte) error {
	if len(cfg.Brokers) == 0 || cfg.Topic == "" {
		p.logger.Error("Kafka producer config incomplete (need brokers, topic); skipping")
		return nil
	}
	w, err := p.newWriter(cfg)
	if err != nil {
		return fmt.Errorf("kafka writer: %w", err)
	}
	defer w.Close()
	if err := w.Write(ctx, key, value); err != nil {
		return fmt.Errorf("write to %s: %w", cfg.Topic, err)
	}
	p.logger.Info("Kafka message produced", "topic", cfg.Topic, "size", len(value))
	return nil
}

// getTargets loads the connection, resolves *_secret_id references, and returns
// every Kafka producer node with its predecessor (for multi-node routing).
// lint:tenant-ok — connection lookup by PK; tenant scoping enforced on deploy.
func (p *kafkaProducer) getTargets(ctx context.Context, connID, tenantID string) ([]*kafkaTarget, error) {
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

	var targets []*kafkaTarget
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		var nc struct {
			Type  string       `json:"type"`
			Kafka *KafkaConfig `json:"kafka"`
		}
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type != "kafka" || nc.Kafka == nil {
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
		targets = append(targets, &kafkaTarget{cfg: *nc.Kafka, predecessorID: predID, predIsConsumer: predIsConsumer})
	}
	if len(targets) == 0 {
		return nil, errors.New("no kafka producer node found")
	}

	p.cacheMu.Lock()
	p.cache[connID] = targets
	p.cacheTime[connID] = time.Now()
	p.cacheMu.Unlock()
	return targets, nil
}
