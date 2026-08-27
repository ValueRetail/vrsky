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
	"path"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// timestampLayout is filename-safe (no colons): 20060102T150405Z.
const timestampLayout = "20060102T150405Z"

// cloudProducer uploads pipeline messages as objects to a cloud object store.
// SDK Producer: Configure wires deps, Deliver uploads one envelope to every
// matching cloud-storage producer node for the connection.
type cloudProducer struct {
	sdk.BaseProducer

	db     *sql.DB
	logger *slog.Logger
	nc     *nats.Conn
	cmdSub *nats.Subscription

	// newStore opens an ObjectStore. Defaulted to objectstore.New in Configure;
	// tests inject a fake. now returns the timestamp used in key templates
	// (overridable in tests for determinism).
	newStore storeFactory
	now      func() time.Time

	cache     map[string][]*cloudTarget
	cacheTime map[string]time.Time
	cacheTTL  time.Duration
	cacheMu   sync.RWMutex
}

// cloudConfig is the per-node configuration (config.cloud_storage). It embeds
// the provider-agnostic objectstore.Config and adds the producer-only key
// template. Credential fields are resolved from *_secret_id references first.
type cloudConfig struct {
	objectstore.Config

	KeyTemplate string `json:"key_template"` // e.g. orders/{{.id}}_{{.timestamp}}.json
}

type cloudTarget struct {
	cfg            cloudConfig
	predecessorID  string
	predIsConsumer bool
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// Configure wires dependencies. Called once by the runner before Deliver.
func (p *cloudProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("cloud-storage-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.newStore == nil {
		p.newStore = objectstore.New
	}
	if p.now == nil {
		p.now = time.Now
	}
	p.cache = make(map[string][]*cloudTarget)
	p.cacheTime = make(map[string]time.Time)
	if p.cacheTTL == 0 {
		p.cacheTTL = 5 * time.Minute
	}
	// Evict a connection's cached config on redeploy (#141).
	p.nc = res.NATS
	if p.nc != nil {
		sub, err := p.nc.Subscribe("vrsky.commands.*.connection.*", p.handleConnectionCommand)
		if err != nil {
			return fmt.Errorf("subscribe to connection commands: %w", err)
		}
		p.cmdSub = sub
	}

	p.logger.Info("cloud-storage-producer configured")
	return nil
}

// handleConnectionCommand evicts the cached config for the connection named in
// a start/stop command so a redeploy's config edits take effect immediately
// instead of after the cache TTL (#141).
func (p *cloudProducer) handleConnectionCommand(msg *nats.Msg) {
	var cmd struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal(msg.Data, &cmd); err != nil || cmd.ConnectionID == "" {
		return
	}
	p.cacheMu.Lock()
	delete(p.cache, cmd.ConnectionID)
	delete(p.cacheTime, cmd.ConnectionID)
	p.cacheMu.Unlock()
	p.logger.Info("Evicted producer config cache on connection command", "connection_id", cmd.ConnectionID)
}

// Stop unsubscribes the command listener (the SDK runner closes NATS/DB).
func (p *cloudProducer) Stop(ctx context.Context) error {
	if p.cmdSub != nil {
		_ = p.cmdSub.Unsubscribe()
	}
	return nil
}

// Deliver uploads the envelope to every matching cloud-storage target. Transient
// failures (connect/write) → sdk.Retriable. A missing producer config for the
// connection is not an error — this binary just isn't the producer for it.
func (p *cloudProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	targets := p.eligibleTargets(ctx, env)

	var transient error
	for _, t := range targets {
		if err := p.upload(ctx, &t.cfg, env, nil); err != nil && transient == nil {
			transient = err
		}
	}
	if transient != nil {
		return sdk.Retriable(transient)
	}
	return nil
}

// DeliverStream writes a large, offloaded payload straight from the pipeline's
// object stream to the destination bucket, so a multi-GB object transfers with
// a bounded buffer instead of being held in memory (ADR 0001).
//
// A stream can only be read once, so fan-out to several targets is declined
// (sdk.ErrStreamUnsupported) and the SDK falls back to buffered delivery —
// identical behaviour to before, subject to the rehydrate cap.
//
// Note: the key template is rendered without payload-derived fields (the
// content hasn't been read, and reading it to template a key would defeat the
// purpose). A template referencing payload fields falls back to a generated key
// with a warning, the same as any other template miss.
func (p *cloudProducer) DeliverStream(ctx context.Context, env *envelope.Envelope, body io.Reader) error {
	targets := p.eligibleTargets(ctx, env)
	if len(targets) == 0 {
		return nil
	}
	if len(targets) > 1 {
		return sdk.ErrStreamUnsupported
	}
	if err := p.upload(ctx, &targets[0].cfg, env, body); err != nil {
		return sdk.Retriable(err)
	}
	return nil
}

// eligibleTargets resolves the configured targets for this connection and drops
// the ones whose predecessor predicate doesn't match this envelope.
func (p *cloudProducer) eligibleTargets(ctx context.Context, env *envelope.Envelope) []*cloudTarget {
	connID := env.IntegrationID
	if connID == "" {
		return nil
	}
	targets, err := p.getTargets(ctx, connID, env.TenantID)
	if err != nil {
		p.logger.Debug("No cloud-storage producer config for connection", "connection_id", connID, "error", err)
		return nil
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	out := make([]*cloudTarget, 0, len(targets))
	for _, t := range targets {
		if t.predIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !t.predIsConsumer && t.predecessorID != "" && lastProcessedBy != t.predecessorID {
			continue
		}
		out = append(out, t)
	}
	return out
}

// upload renders the object key and writes the payload to the bucket. When body
// is non-nil the payload is streamed from it (never buffered); otherwise
// env.Payload is written.
func (p *cloudProducer) upload(ctx context.Context, cfg *cloudConfig, env *envelope.Envelope, body io.Reader) error {
	if cfg.Bucket == "" {
		p.logger.Error("cloud-storage producer config incomplete (need bucket); skipping")
		return nil
	}

	key, err := p.renderKey(cfg.KeyTemplate, cfg.Prefix, env)
	if err != nil {
		// A bad/mismatched template (e.g. references a field the payload lacks)
		// shouldn't lose data — fall back to a generated key and warn instead of
		// dropping. The warning surfaces the misconfiguration.
		fallback, ferr := p.renderKey("", cfg.Prefix, env)
		if ferr != nil {
			p.logger.Error("key template failed and fallback key failed; dropping message",
				"template", cfg.KeyTemplate, "error", err)
			return nil
		}
		p.logger.Warn("key template failed; writing under a generated key instead",
			"template", cfg.KeyTemplate, "key", fallback, "error", err)
		key = fallback
	}

	store, err := p.newStore(ctx, &cfg.Config)
	if err != nil {
		return fmt.Errorf("open backend: %w", err)
	}
	// Close the store after this upload — newStore opens a fresh client per
	// message (the GCS backend builds a real storage.Client with its own
	// connection pool + goroutines), so without this they leak unboundedly
	// under sustained throughput.
	defer func() { _ = store.Close() }()

	contentType := env.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if body != nil {
		if err := store.PutStream(ctx, key, body, contentType); err != nil {
			return fmt.Errorf("put-stream %s: %w", key, err)
		}
		p.logger.Info("cloud-storage streamed upload complete", "bucket", cfg.Bucket, "key", key, "size", env.PayloadSize)
		return nil
	}
	if err := store.Put(ctx, key, env.Payload, contentType); err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	p.logger.Info("cloud-storage upload complete", "bucket", cfg.Bucket, "key", key, "size", len(env.Payload))
	return nil
}

// renderKey executes the key template against the payload (when it is a JSON
// object) plus the built-ins {timestamp, uuid}, prepends the configured prefix,
// and normalises the result to a safe object key (no ".." traversal).
func (p *cloudProducer) renderKey(tmpl, prefix string, env *envelope.Envelope) (string, error) {
	data := map[string]any{}
	var obj map[string]any
	if json.Unmarshal(env.Payload, &obj) == nil {
		data = obj
	}
	if _, ok := data["timestamp"]; !ok {
		data["timestamp"] = p.now().UTC().Format(timestampLayout)
	}
	if _, ok := data["uuid"]; !ok {
		// Envelope IDs are normally a UUID, but some consumers (e.g. api-consumer)
		// leave it empty; generate one so the default {{.uuid}} key is never blank.
		id := env.ID
		if id == "" {
			id = uuid.NewString()
		}
		data["uuid"] = id
	}

	if strings.TrimSpace(tmpl) == "" {
		tmpl = "{{.uuid}}"
	}
	t, err := template.New("key").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	key := strings.TrimSpace(buf.String())
	if prefix != "" {
		key = strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(key, "/")
	}
	// Normalise and clamp: path.Clean resolves any ".." so the key can never
	// escape the bucket root.
	key = strings.TrimPrefix(path.Clean("/"+key), "/")
	if key == "" || key == "." {
		return "", fmt.Errorf("rendered key %q is empty after normalisation", buf.String())
	}
	return key, nil
}

// getTargets loads the connection, resolves *_secret_id references, and returns
// every cloud-storage producer node with its predecessor (for multi-node routing).
// lint:tenant-ok — connection lookup by PK; tenant scoping enforced on deploy.
func (p *cloudProducer) getTargets(ctx context.Context, connID, tenantID string) ([]*cloudTarget, error) {
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

	var targets []*cloudTarget
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		var nc struct {
			Type  string       `json:"type"`
			Cloud *cloudConfig `json:"cloud_storage"`
		}
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type != "cloud_storage" || nc.Cloud == nil {
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
		targets = append(targets, &cloudTarget{cfg: *nc.Cloud, predecessorID: predID, predIsConsumer: predIsConsumer})
	}
	if len(targets) == 0 {
		return nil, errors.New("no cloud-storage producer node found")
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
func (p *cloudProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	targets, err := p.getTargets(ctx, connectionID, tenantID)
	if err != nil {
		return false // Deliver treats lookup errors as "not ours" too
	}
	return len(targets) > 0
}
