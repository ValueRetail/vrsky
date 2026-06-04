package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"text/template"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	dialMaxAttempts = 3
	dialBaseBackoff = time.Second
	// timestampLayout is filename-safe (no colons): 20060102T150405Z.
	timestampLayout = "20060102T150405Z"
)

// sftpProducer uploads pipeline messages as files to a remote SFTP directory.
// SDK Producer: Configure wires deps, Deliver uploads one envelope to every
// matching SFTP producer node for the connection.
type sftpProducer struct {
	sdk.BaseProducer

	db     *sql.DB
	logger *slog.Logger

	// dial opens an SFTP connection. Defaulted to realDial in Configure; tests
	// inject a fake. now returns the timestamp used in filename templates
	// (overridable in tests for determinism).
	dial dialFunc
	now  func() time.Time

	cache     map[string][]*sftpTarget
	cacheTime map[string]time.Time
	cacheTTL  time.Duration
	cacheMu   sync.RWMutex
}

// SFTPConfig is the per-node configuration (config.sftp). Password and
// PrivateKey are resolved from *_secret_id references before use.
type SFTPConfig struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username"`
	Password         string `json:"password"`          // resolved from password_secret_id
	PrivateKey       string `json:"private_key"`       // resolved from private_key_secret_id
	HostKey          string `json:"host_key"`          // optional pinned host key (authorized_keys line)
	RemoteDir        string `json:"remote_dir"`        // directory to upload into
	FilenameTemplate string `json:"filename_template"` // e.g. order_{{.id}}_{{.timestamp}}.json
}

type sftpTarget struct {
	cfg            SFTPConfig
	predecessorID  string
	predIsConsumer bool
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// Configure wires dependencies. Called once by the runner before Deliver.
func (p *sftpProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("sftp-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.dial == nil {
		p.dial = realDial
	}
	if p.now == nil {
		p.now = time.Now
	}
	p.cache = make(map[string][]*sftpTarget)
	p.cacheTime = make(map[string]time.Time)
	if p.cacheTTL == 0 {
		p.cacheTTL = 5 * time.Minute
	}
	p.logger.Info("sftp-producer configured")
	return nil
}

// Deliver uploads the envelope to every matching SFTP target. Transient
// failures (connect/write) → sdk.Retriable. A missing producer config for the
// connection is not an error — this binary just isn't the producer for it.
func (p *sftpProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connID := env.IntegrationID
	if connID == "" {
		return nil
	}
	targets, err := p.getTargets(ctx, connID, env.TenantID)
	if err != nil {
		p.logger.Debug("No SFTP producer config for connection", "connection_id", connID, "error", err)
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
		if err := p.upload(ctx, &t.cfg, env); err != nil && transient == nil {
			transient = err
		}
	}
	if transient != nil {
		return sdk.Retriable(transient)
	}
	return nil
}

// upload renders the filename and writes the payload to the remote dir.
func (p *sftpProducer) upload(ctx context.Context, cfg *SFTPConfig, env *envelope.Envelope) error {
	if cfg.Host == "" || cfg.Username == "" || cfg.RemoteDir == "" {
		p.logger.Error("SFTP producer config incomplete (need host, username, remote_dir); skipping")
		return nil
	}
	if cfg.Password == "" && cfg.PrivateKey == "" {
		p.logger.Error("SFTP producer has no credentials (set a password or a private key); skipping")
		return nil
	}

	filename, err := p.renderFilename(cfg.FilenameTemplate, env)
	if err != nil {
		// A bad template won't improve on retry — log and drop (poison).
		p.logger.Error("invalid filename template; dropping message", "template", cfg.FilenameTemplate, "error", err)
		return nil
	}

	conn, err := p.dialWithBackoff(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	if err := conn.MkdirAll(cfg.RemoteDir); err != nil {
		return fmt.Errorf("mkdir remote_dir: %w", err)
	}
	remotePath := path.Join(cfg.RemoteDir, filename)
	if err := conn.Write(remotePath, env.Payload); err != nil {
		return fmt.Errorf("write %s: %w", remotePath, err)
	}
	p.logger.Info("SFTP upload complete", "remote_path", remotePath, "size", len(env.Payload))
	return nil
}

// renderFilename executes the filename template against the payload (when it is
// a JSON object) plus the built-ins {timestamp, uuid}. The result is reduced to
// a single path element to prevent traversal outside remote_dir.
func (p *sftpProducer) renderFilename(tmpl string, env *envelope.Envelope) (string, error) {
	data := map[string]any{}
	var obj map[string]any
	if json.Unmarshal(env.Payload, &obj) == nil {
		data = obj
	}
	if _, ok := data["timestamp"]; !ok {
		data["timestamp"] = p.now().UTC().Format(timestampLayout)
	}
	if _, ok := data["uuid"]; !ok {
		data["uuid"] = env.ID
	}

	if strings.TrimSpace(tmpl) == "" {
		tmpl = "{{.uuid}}.json"
	}
	t, err := template.New("filename").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	// Collapse to a single safe path element — never let a rendered value escape
	// the remote dir via "/" or "..".
	name := path.Base(strings.TrimSpace(buf.String()))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("rendered filename %q is not a safe single path element", buf.String())
	}
	return name, nil
}

// dialWithBackoff tries to connect up to dialMaxAttempts times with exponential
// backoff, honouring ctx cancellation.
func (p *sftpProducer) dialWithBackoff(ctx context.Context, cfg *SFTPConfig) (sftpConn, error) {
	var lastErr error
	backoff := dialBaseBackoff
	for attempt := 1; attempt <= dialMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, err := p.dial(cfg)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		p.logger.Warn("SFTP dial attempt failed", "attempt", attempt, "error", err)
		if attempt < dialMaxAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}
	return nil, lastErr
}

// getTargets loads the connection, resolves *_secret_id references, and returns
// every SFTP producer node with its predecessor (for multi-node routing).
// lint:tenant-ok — connection lookup by PK; tenant scoping enforced on deploy.
func (p *sftpProducer) getTargets(ctx context.Context, connID, tenantID string) ([]*sftpTarget, error) {
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

	var targets []*sftpTarget
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		var nc struct {
			Type string      `json:"type"`
			SFTP *SFTPConfig `json:"sftp"`
		}
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type != "sftp" || nc.SFTP == nil {
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
		if nc.SFTP.HostKey == "" {
			p.logger.Warn("SFTP host_key not pinned; server identity will not be verified (set host_key in production)",
				"connection_id", connID)
		}
		targets = append(targets, &sftpTarget{cfg: *nc.SFTP, predecessorID: predID, predIsConsumer: predIsConsumer})
	}
	if len(targets) == 0 {
		return nil, errors.New("no sftp producer node found")
	}

	p.cacheMu.Lock()
	p.cache[connID] = targets
	p.cacheTime[connID] = time.Now()
	p.cacheMu.Unlock()
	return targets, nil
}
