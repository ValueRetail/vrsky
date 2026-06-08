package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// cloudConsumer watches a cloud-storage bucket per active connection, fetches
// new objects and publishes them into the pipeline. SDK Consumer: Configure
// wires deps, Run subscribes to command subjects and blocks, Stop cancels
// pollers.
type cloudConsumer struct {
	sdk.BaseConsumer

	db      *sql.DB
	nc      *nats.Conn
	publish sdk.PublishFunc
	logger  *slog.Logger

	// newStore opens an ObjectStore. Defaulted to objectstore.New in Configure;
	// tests inject a fake so the poller runs without a live bucket.
	newStore storeFactory

	active map[string]context.CancelFunc
	mu     sync.RWMutex

	startSub *nats.Subscription
	stopSub  *nats.Subscription
}

// cloudConfig is the per-node configuration (config.cloud_storage). It embeds
// the provider-agnostic objectstore.Config (provider, bucket, prefix, creds,
// SSE) and adds the consumer-only poll/after-action fields. Credential fields
// are resolved from *_secret_id references before use.
type cloudConfig struct {
	objectstore.Config

	FilePattern         string `json:"file_pattern"`          // optional glob against the object base name, e.g. *.csv
	PollIntervalSeconds int    `json:"poll_interval_seconds"` // <= 0 means run once
	AfterAction         string `json:"after_action"`          // "delete" | "move" | "none" (default none)
	MovePrefix          string `json:"move_prefix"`           // destination prefix when after_action=move
}

type nodeConfig struct {
	Type  string       `json:"type"`
	Cloud *cloudConfig `json:"cloud_storage"`
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
func (s *cloudConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("cloud-storage-consumer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	s.db = res.DB
	s.nc = res.NATS
	s.logger = res.Logger
	s.active = make(map[string]context.CancelFunc)
	if s.newStore == nil {
		s.newStore = objectstore.New
	}
	res.Health.SetReady(true)
	return nil
}

// Run subscribes to the connection command subjects and blocks until ctx is
// cancelled. Per-connection polling is driven from the command handlers.
func (s *cloudConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
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
func (s *cloudConsumer) Stop(ctx context.Context) error {
	if s.startSub != nil {
		_ = s.startSub.Unsubscribe()
	}
	if s.stopSub != nil {
		_ = s.stopSub.Unsubscribe()
	}
	s.mu.Lock()
	for id, cancel := range s.active {
		s.logger.Info("Stopping cloud-storage poller", "connection_id", id)
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

func (s *cloudConsumer) handleStartCommand(msg *nats.Msg) {
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
		logger.Warn("cloud-storage poller already running")
		return
	}

	cfg, ok := s.getConfig(cmd.ConnectionID, cmd.TenantID)
	if !ok {
		logger.Debug("Not a cloud-storage consumer for this connection, ignoring")
		return
	}
	if cfg.Bucket == "" {
		logger.Error("cloud-storage config incomplete (need bucket)")
		return
	}
	if cfg.AfterAction == "move" && cfg.MovePrefix == "" {
		logger.Error("after_action=move requires move_prefix")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.active[cmd.ConnectionID] = cancel
	s.mu.Unlock()
	_ = s.updateConnectionStatus(cmd.ConnectionID, cmd.TenantID, "running")

	logger.Info("Starting cloud-storage poller",
		"provider", cfg.providerOrDefault(), "bucket", cfg.Bucket, "prefix", cfg.Prefix, "after_action", cfg.AfterAction)
	go s.runPoller(ctx, cmd.ConnectionID, cmd.TenantID, cfg)
}

func (s *cloudConsumer) handleStopCommand(msg *nats.Msg) {
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
		s.logger.Info("cloud-storage poller stopped", "connection_id", cmd.ConnectionID)
	}
}

// runPoller opens the store once, fetches immediately, then on the configured
// interval. List/Get errors are logged and retried on the next tick.
func (s *cloudConsumer) runPoller(ctx context.Context, connID, tenantID string, cfg *cloudConfig) {
	logger := s.logger.With("connection_id", connID)

	store, err := s.newStore(ctx, &cfg.Config)
	if err != nil {
		logger.Error("open cloud-storage backend failed", "error", err)
		_ = s.updateConnectionStatus(connID, tenantID, "error")
		s.mu.Lock()
		delete(s.active, connID)
		s.mu.Unlock()
		return
	}

	// processed tracks object keys already published this session, so an
	// after_action=none bucket does not re-publish the same object every tick.
	processed := make(map[string]bool)

	s.pollOnce(ctx, connID, tenantID, store, cfg, processed, logger)

	// poll_interval_seconds <= 0 means run once (matches the UI label "0 = once").
	if cfg.PollIntervalSeconds <= 0 {
		logger.Info("cloud-storage one-shot poll complete")
		_ = s.updateConnectionStatus(connID, tenantID, "stopped")
		s.mu.Lock()
		delete(s.active, connID)
		s.mu.Unlock()
		return
	}

	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollOnce(ctx, connID, tenantID, store, cfg, processed, logger)
		}
	}
}

// pollOnce lists the bucket under the prefix and fetches+publishes+after-actions
// each new object.
func (s *cloudConsumer) pollOnce(ctx context.Context, connID, tenantID string, store objectstore.ObjectStore, cfg *cloudConfig, processed map[string]bool, logger *slog.Logger) {
	objs, err := store.List(ctx, cfg.Prefix)
	if err != nil {
		logger.Error("cloud-storage list failed; will retry next cycle", "prefix", cfg.Prefix, "error", err)
		return
	}

	for _, o := range objs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if processed[o.Key] {
			continue
		}
		base := path.Base(o.Key)
		if cfg.FilePattern != "" {
			if match, _ := path.Match(cfg.FilePattern, base); !match {
				continue
			}
		}

		data, ct, err := store.Get(ctx, o.Key)
		if err != nil {
			logger.Error("cloud-storage get failed", "key", o.Key, "error", err)
			continue
		}
		if ct == "" {
			ct = detectContentType(base, data)
		}

		if err := s.publishObject(ctx, connID, tenantID, cfg.Bucket, o.Key, ct, data); err != nil {
			logger.Error("publish failed", "key", o.Key, "error", err)
			continue // leave the object in place; retried next cycle
		}
		processed[o.Key] = true
		logger.Info("cloud-storage object ingested", "key", o.Key, "size", len(data))

		if err := s.afterAction(ctx, store, cfg, o.Key); err != nil {
			logger.Warn("after-action failed", "key", o.Key, "action", cfg.AfterAction, "error", err)
		}
	}
}

// afterAction applies the configured post-ingest action to a fetched object.
func (s *cloudConsumer) afterAction(ctx context.Context, store objectstore.ObjectStore, cfg *cloudConfig, key string) error {
	switch cfg.AfterAction {
	case "delete":
		return store.Delete(ctx, key)
	case "move":
		if cfg.MovePrefix == "" {
			return fmt.Errorf("after_action=move but move_prefix is empty")
		}
		dst := path.Join(cfg.MovePrefix, path.Base(key))
		if err := store.Copy(ctx, key, dst); err != nil {
			return fmt.Errorf("copy to move_prefix: %w", err)
		}
		return store.Delete(ctx, key)
	default: // "none" / "" / "leave"
		return nil
	}
}

func (s *cloudConsumer) publishObject(ctx context.Context, connID, tenantID, bucket, key, contentType string, data []byte) error {
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = contentType
	env.Source = "cloud-storage:" + key
	env.Payload = data
	env.PayloadSize = int64(len(data))
	env.StepHistory = []string{"cloud-storage-consumer"}
	env.Metadata = map[string]interface{}{"object_key": key, "bucket": bucket, "filename": path.Base(key)}
	return s.publish(ctx, env)
}

// --- DB helpers ---

// getConfig loads the connection, resolves *_secret_id references to plaintext,
// and extracts the cloud-storage consumer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (s *cloudConsumer) getConfig(connectionID, tenantID string) (*cloudConfig, bool) {
	var nodesJSON json.RawMessage
	err := s.db.QueryRow(
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON)
	if err != nil {
		s.logger.Debug("connection not found", "error", err, "connection_id", connectionID)
		return nil, false
	}

	resolved, err := crypto.ResolveSecretsInJSON(context.Background(), crypto.NewSQLSecretReader(s.db), tenantID, nodesJSON)
	if err != nil {
		s.logger.Error("resolve secrets", "error", err, "connection_id", connectionID)
		return nil, false
	}

	var nodes []node
	if err := json.Unmarshal(resolved, &nodes); err != nil {
		s.logger.Warn("parse nodes", "error", err, "connection_id", connectionID)
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
		if nc.Type == "cloud_storage" && nc.Cloud != nil {
			return nc.Cloud, true
		}
	}
	return nil, false
}

func (s *cloudConsumer) updateConnectionStatus(connectionID, tenantID, status string) error {
	var query string
	if status == "running" {
		query = `UPDATE connections SET status = $1, started_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	} else {
		query = `UPDATE connections SET status = $1, stopped_at = NOW(), updated_at = NOW() WHERE id = $2 AND tenant_id = $3`
	}
	_, err := s.db.Exec(query, status, connectionID, tenantID)
	return err
}

func (c *cloudConfig) providerOrDefault() string {
	if c.Provider == "" {
		return objectstore.ProviderS3
	}
	return c.Provider
}

// detectContentType picks a MIME type from the object's base name, falling back
// to a light content sniff.
func detectContentType(name string, data []byte) string {
	switch {
	case hasSuffixFold(name, ".json"):
		return "application/json"
	case hasSuffixFold(name, ".xml"):
		return "application/xml"
	case hasSuffixFold(name, ".csv"):
		return "text/csv"
	case hasSuffixFold(name, ".txt"):
		return "text/plain"
	}
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}
	return "application/octet-stream"
}

func hasSuffixFold(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return equalFold(s[len(s)-len(suffix):], suffix)
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
