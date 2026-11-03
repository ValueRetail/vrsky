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
	"regexp"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const defaultMethod = http.MethodPost

// brightpearlProducer delivers pipeline envelopes into Brightpearl. It is an SDK
// Producer: Configure wires deps, Deliver writes one envelope.
type brightpearlProducer struct {
	sdk.BaseProducer

	db         *sql.DB
	logger     *slog.Logger
	httpClient *http.Client
	// allowPrivate is the operator's dev switch (ALLOW_PRIVATE_UPSTREAMS): it
	// allows http and private addresses in base_url, for a mock upstream.
	allowPrivate bool
}

// BrightpearlProducerConfig is the per-node configuration (config.brightpearl).
type BrightpearlProducerConfig struct {
	Datacenter  string `json:"datacenter"`
	AccountCode string `json:"account_code"`
	BaseURL     string `json:"base_url"`
	AppRef      string `json:"app_ref"`
	StaffToken  string `json:"staff_token"` // from staff_token_secret_id

	Resource string `json:"resource"` // write target, e.g. /order-service/order
	Method   string `json:"method"`   // POST (default), PUT, or PATCH
}

type nodeConfig struct {
	Type        string                     `json:"type"`
	Brightpearl *BrightpearlProducerConfig `json:"brightpearl"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (p *brightpearlProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("brightpearl-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	p.allowPrivate = safeurl.AllowPrivateFromEnv()
	if p.httpClient == nil {
		// base_url is tenant config and every request carries the staff
		// token, so the client refuses private, loopback and link-local
		// addresses at dial time and follows no redirects (#667).
		p.httpClient = safeurl.NewClient(60*time.Second, p.allowPrivate)
	}
	p.logger.Info("brightpearl-producer configured")
	return nil
}

func (p *brightpearlProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	if env.IntegrationID == "" {
		return nil
	}
	cfg, err := p.getConfig(ctx, env.IntegrationID, env.TenantID)
	if err != nil {
		if sdk.IsConfigUnavailable(err) {
			return err // not acked: retried, then the DLQ
		}
		p.logger.Debug("No Brightpearl producer config", "connection_id", env.IntegrationID, "error", err)
		return nil
	}
	if cfg.AppRef == "" || cfg.StaffToken == "" || cfg.baseURL() == "" || cfg.Resource == "" {
		return sdk.Permanent(fmt.Errorf("brightpearl producer config incomplete for connection %s", env.IntegrationID))
	}
	// Retriable, not Permanent: Permanent acks and drops the message. This
	// way it is redelivered and ends in the DLQ, where it waits until the
	// node's base URL is fixed.
	if err := cfg.checkBaseURL(p.allowPrivate); err != nil {
		return sdk.Retriable(fmt.Errorf("brightpearl base URL refused for connection %s: %w", env.IntegrationID, err))
	}
	if !json.Valid(env.Payload) {
		p.logger.Error("dropping: payload is not valid JSON", "envelope_id", env.ID)
		return sdk.Permanent(errors.New("payload is not valid JSON"))
	}
	return p.write(ctx, cfg, env.Payload)
}

func (p *brightpearlProducer) write(ctx context.Context, cfg *BrightpearlProducerConfig, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, cfg.effectiveMethod(), cfg.resourceURL(), bytes.NewReader(payload))
	if err != nil {
		return sdk.Permanent(err)
	}
	req.Header.Set("brightpearl-app-ref", cfg.AppRef)
	req.Header.Set("brightpearl-staff-token", cfg.StaffToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		// A dial refused by safeurl (ErrBlockedAddress) is retriable too: a
		// transient DNS answer must not drop the message.
		return sdk.Retriable(fmt.Errorf("brightpearl request: %w", err))
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.logger.Info("Delivered to Brightpearl", "resource", cfg.Resource, "status", resp.StatusCode)
		return nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable:
		return sdk.RateLimited(fmt.Errorf("brightpearl %d: %s", resp.StatusCode, snippet(body)), 5*time.Second)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return sdk.Permanent(fmt.Errorf("brightpearl auth %d: %s", resp.StatusCode, snippet(body)))
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return sdk.Permanent(fmt.Errorf("brightpearl %d: %s", resp.StatusCode, snippet(body)))
	default:
		return sdk.Retriable(fmt.Errorf("brightpearl %d: %s", resp.StatusCode, snippet(body)))
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func (cfg *BrightpearlProducerConfig) baseURL() string {
	if cfg.BaseURL != "" {
		return strings.TrimRight(cfg.BaseURL, "/")
	}
	if cfg.Datacenter == "" || cfg.AccountCode == "" {
		return ""
	}
	return fmt.Sprintf("https://ws-%s.brightpearl.com/public-api/%s", cfg.Datacenter, cfg.AccountCode)
}

// checkBaseURL refuses a URL the connector must not call (#667): it comes from
// tenant config and every request carries the staff token. The dial check in
// the HTTP client is the enforcement; this gives an early, readable error.
func (cfg *BrightpearlProducerConfig) checkBaseURL(allowPrivate bool) error {
	if cfg.BaseURL != "" {
		return safeurl.CheckBaseURL(cfg.BaseURL, allowPrivate)
	}
	return checkDerivedBaseURL(cfg.Datacenter, cfg.AccountCode)
}

var (
	datacenterRE  = regexp.MustCompile(`(?i)^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	accountCodeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// checkDerivedBaseURL keeps the derived URL on Brightpearl: datacenter goes
// into the host name and account_code into the path, so each must be one
// label or segment. Otherwise "x@10.0.0.1/" as a datacenter moves the host.
func checkDerivedBaseURL(datacenter, accountCode string) error {
	if !datacenterRE.MatchString(datacenter) {
		return fmt.Errorf("datacenter %q must be a single host label such as eu1 or use1", datacenter)
	}
	if !accountCodeRE.MatchString(accountCode) {
		return fmt.Errorf("account_code %q may only hold letters, digits, - and _", accountCode)
	}
	return nil
}

func (cfg *BrightpearlProducerConfig) resourceURL() string {
	return cfg.baseURL() + "/" + strings.TrimLeft(cfg.Resource, "/")
}

func (cfg *BrightpearlProducerConfig) effectiveMethod() string {
	switch strings.ToUpper(strings.TrimSpace(cfg.Method)) {
	case http.MethodPut:
		return http.MethodPut
	case http.MethodPatch:
		return http.MethodPatch
	case http.MethodPost:
		return http.MethodPost
	}
	return defaultMethod
}

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Brightpearl producer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (p *brightpearlProducer) getConfig(ctx context.Context, connectionID, tenantID string) (*BrightpearlProducerConfig, error) {
	var nodesJSON json.RawMessage
	if err := p.db.QueryRowContext(ctx,
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON); err != nil {
		return nil, sdk.ConfigReadError(err)
	}
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	reader := crypto.NewSQLSecretReader(p.db)
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		// Find our node before resolving secrets: an unreadable secret on
		// another connector's node is not a reason to hold this message.
		var nc nodeConfig
		if err := json.Unmarshal(n.Config, &nc); err != nil || nc.Type != "brightpearl" || nc.Brightpearl == nil {
			continue
		}
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, n.Config)
		if rerr != nil {
			return nil, sdk.ConfigUnavailable(fmt.Errorf("resolve secrets: %w", rerr))
		}
		nc = nodeConfig{}
		if err := json.Unmarshal(resolved, &nc); err != nil || nc.Brightpearl == nil {
			continue
		}
		return nc.Brightpearl, nil
	}
	return nil, errors.New("no brightpearl producer node found")
}

// ServesConnection reports whether this connection has a Brightpearl destination —
// mirroring Deliver's own "no config -> not ours" semantics — so the SDK can
// ack foreign connections before rehydrating large payloads (sdk.ConnectionScoped).
func (p *brightpearlProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	_, err := p.getConfig(ctx, connectionID, tenantID)
	// A failed read answers true so Deliver runs and returns it (#704).
	return err == nil || sdk.IsConfigUnavailable(err)
}
