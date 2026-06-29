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

// refetchBackoff is the pause after a transient fetch/publish error before the
// loop tries again, so a broker hiccup doesn't spin hot.
const refetchBackoff = 2 * time.Second

// kafkaConsumer subscribes to a Kafka topic per active connection and publishes
// each message into the pipeline, committing the group offset only after a
// successful publish. SDK Consumer: Configure wires deps, Run subscribes to
// command subjects and blocks, Stop cancels the per-connection loops.
type kafkaConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	// newReader opens a consumer-group reader. Defaulted to realReader in
	// Configure; tests inject a fake so the loop runs without a broker.
	newReader readerFactory
	// ping checks broker reachability for the connection-test endpoint.
	// Defaulted to realKafkaPing in Configure; tests inject a stub.
	ping func(ctx context.Context, cfg *KafkaConfig) (int, error)
	// sample peeks the earliest available message on the topic for the
	// schema-preview endpoint (#144). Defaulted to realKafkaSample; tests stub.
	sample func(ctx context.Context, cfg *KafkaConfig) ([]byte, error)

	active map[string]context.CancelFunc
	mu     sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// KafkaConfig is the per-node configuration (config.kafka). Password and
// ClientKey are resolved from *_secret_id references before use.
type KafkaConfig struct {
	Brokers       []string `json:"brokers"`
	Topic         string   `json:"topic"`
	ConsumerGroup string   `json:"consumer_group"`
	AuthType      string   `json:"auth_type"` // none|sasl-plain|sasl-scram-256|sasl-scram-512|mtls
	Username      string   `json:"username"`
	Password      string   `json:"password"`    // resolved from password_secret_id
	CACert        string   `json:"ca_cert"`     // PEM (public)
	ClientCert    string   `json:"client_cert"` // PEM (public)
	ClientKey     string   `json:"client_key"`  // resolved from client_key_secret_id
}

type nodeConfig struct {
	Type  string       `json:"type"`
	Kafka *KafkaConfig `json:"kafka"`
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
func (k *kafkaConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("kafka-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	k.db = res.DB
	k.nc = res.NATS
	k.logger = res.Logger
	k.active = make(map[string]context.CancelFunc)
	if k.newReader == nil {
		k.newReader = realReader
	}
	if k.ping == nil {
		k.ping = realKafkaPing
	}
	if k.sample == nil {
		k.sample = realKafkaSample
	}
	k.RegisterHTTPHandler("/test-connection/", k.handleTestConnection())
	k.RegisterHTTPHandler("/sample-data/", k.handleSampleData())
	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled. Per-connection consume loops are driven from the handlers.
func (k *kafkaConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
	k.publish = publish

	startSub, err := k.nc.Subscribe("vrsky.commands.*.connection.start", k.handleStartCommand)
	if err != nil {
		return fmt.Errorf("subscribe start commands: %w", err)
	}
	k.startSub = startSub

	stopSub, err := k.nc.Subscribe("vrsky.commands.*.connection.stop", k.handleStopCommand)
	if err != nil {
		return fmt.Errorf("subscribe stop commands: %w", err)
	}
	k.stopSub = stopSub

	k.logger.Info("Subscribed to NATS command topics")
	<-ctx.Done()
	return nil
}

// Stop cancels all consume loops. The SDK runner calls this after Run returns.
func (k *kafkaConsumer) Stop(ctx context.Context) error {
	if k.startSub != nil {
		_ = k.startSub.Unsubscribe()
	}
	if k.stopSub != nil {
		_ = k.stopSub.Unsubscribe()
	}
	k.mu.Lock()
	for id, cancel := range k.active {
		k.logger.Info("Stopping Kafka consumer", "connection_id", id)
		cancel()
	}
	k.active = make(map[string]context.CancelFunc)
	k.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (k *kafkaConsumer) handleStartCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		k.logger.Error("parse start command", "error", err)
		return
	}
	logger := k.logger.With("connection_id", cmd.ConnectionID, "tenant_id", cmd.TenantID)

	k.mu.RLock()
	_, exists := k.active[cmd.ConnectionID]
	k.mu.RUnlock()
	if exists {
		logger.Warn("Kafka consumer already running")
		return
	}

	cfg, ok := k.getKafkaConfig(cmd.ConnectionID, cmd.TenantID)
	if !ok {
		logger.Debug("Not a Kafka consumer for this connection, ignoring")
		return
	}
	if len(cfg.Brokers) == 0 || cfg.Topic == "" || cfg.ConsumerGroup == "" {
		logger.Error("Kafka config incomplete (need brokers, topic, consumer_group)")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	k.mu.Lock()
	k.active[cmd.ConnectionID] = cancel
	k.mu.Unlock()
	_ = k.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running")

	logger.Info("Starting Kafka consumer", "brokers", cfg.Brokers, "topic", cfg.Topic, "group", cfg.ConsumerGroup, "auth", cfg.AuthType)
	go k.consumeLoop(ctx, cmd.ConnectionID, cmd.TenantID, cfg, logger)
}

func (k *kafkaConsumer) handleStopCommand(msg *nats.Msg) {
	var cmd commandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		k.logger.Error("parse stop command", "error", err)
		return
	}
	k.mu.Lock()
	cancel, exists := k.active[cmd.ConnectionID]
	if exists {
		cancel()
		delete(k.active, cmd.ConnectionID)
	}
	k.mu.Unlock()
	if exists {
		_ = k.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "stopped")
		k.logger.Info("Kafka consumer stopped", "connection_id", cmd.ConnectionID)
	}
}

// consumeLoop fetches messages and publishes each into the pipeline, committing
// the offset only after a successful publish (acceptance criterion #1).
func (k *kafkaConsumer) consumeLoop(ctx context.Context, connID, tenantID string, cfg *KafkaConfig, logger *slog.Logger) {
	reader, err := k.newReader(cfg)
	if err != nil {
		logger.Error("Kafka reader init failed", "error", err)
		return
	}
	defer reader.Close()

	for {
		if ctx.Err() != nil {
			return
		}
		msg, err := reader.Fetch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // shutdown
			}
			logger.Error("Kafka fetch failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(refetchBackoff):
			}
			continue
		}

		if err := k.publishMessage(ctx, connID, tenantID, msg); err != nil {
			// Do NOT commit — the message will be re-fetched and retried.
			logger.Error("publish failed; offset not committed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(refetchBackoff):
			}
			continue
		}

		if err := msg.commit(ctx); err != nil && ctx.Err() == nil {
			// Publish succeeded but commit failed — the message may be
			// re-delivered (at-least-once); downstream dedup handles it.
			logger.Warn("offset commit failed after publish", "error", err)
		}
	}
}

func (k *kafkaConsumer) publishMessage(ctx context.Context, connID, tenantID string, msg *fetchedMessage) error {
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = detectContentType(msg.Value)
	env.Source = "kafka-consumer"
	env.Payload = msg.Value
	env.PayloadSize = int64(len(msg.Value))
	env.StepHistory = []string{"kafka-consumer"}
	env.Metadata = map[string]interface{}{}
	if len(msg.Key) > 0 {
		env.Metadata["kafka_key"] = string(msg.Key)
	}
	return k.publish(ctx, env)
}

// --- DB helpers ---

// getKafkaConfig loads the connection, resolves *_secret_id references, and
// extracts the Kafka consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (k *kafkaConsumer) getKafkaConfig(connectionID, tenantID string) (*KafkaConfig, bool) {
	var nodesJSON json.RawMessage
	err := k.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON)
	if err != nil {
		k.logger.Debug("connection not found", "error", err, "connection_id", connectionID)
		return nil, false
	}

	resolved, err := crypto.ResolveSecretsInJSON(context.Background(), crypto.NewSQLSecretReader(k.db), tenantID, nodesJSON)
	if err != nil {
		k.logger.Error("resolve secrets", "error", err, "connection_id", connectionID)
		return nil, false
	}

	var nodes []node
	if err := json.Unmarshal(resolved, &nodes); err != nil {
		k.logger.Warn("parse nodes", "error", err, "connection_id", connectionID)
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
		if nc.Type == "kafka" && nc.Kafka != nil {
			return nc.Kafka, true
		}
	}
	return nil, false
}

func (k *kafkaConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := k.db.Exec(query, status, connectionID, tenantID)
	return err
}

// detectContentType is a light sniff for the envelope content type.
func detectContentType(data []byte) string {
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}
	return "application/octet-stream"
}
