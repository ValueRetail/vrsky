package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	defaultScope       = "https://api.businesscentral.dynamics.com/.default"
	defaultAPIHost     = "https://api.businesscentral.dynamics.com"
	defaultEnvironment = "Production"
	defaultEntity      = "items"
	defaultMethod      = http.MethodPost
)

// bcProducer delivers pipeline envelopes into Business Central. It is an SDK
// Producer: Configure wires deps, Deliver writes one envelope.
type bcProducer struct {
	sdk.BaseProducer

	db         *sql.DB
	logger     *slog.Logger
	httpClient *http.Client
}

// BCProducerConfig is the per-node configuration (config.business_central).
type BCProducerConfig struct {
	AADTenantID  string `json:"aad_tenant_id"`
	Environment  string `json:"environment"`
	CompanyID    string `json:"company_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` // from client_secret_secret_id
	Entity       string `json:"entity"`        // write target, e.g. items, salesOrders
	Method       string `json:"method"`        // POST (default) or PATCH
	// DedupeFields (plans/webhook-idempotency.md): before a POST, GET the
	// entity filtered on these payload fields (also the BC property names)
	// and skip the write when a record already matches — a redelivery
	// inside VRSky must not create a second invoice. POST only.
	DedupeFields []string `json:"dedupe_fields"`

	APIBaseURL string `json:"api_base_url"` // optional override (on-prem/tests)
	TokenURL   string `json:"token_url"`
	Scope      string `json:"scope"`
}

type nodeConfig struct {
	Type            string            `json:"type"`
	BusinessCentral *BCProducerConfig `json:"business_central"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (p *bcProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("business-central-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	p.logger.Info("business-central-producer configured")
	return nil
}

func (p *bcProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	if env.IntegrationID == "" {
		return nil
	}
	cfg, err := p.getConfig(ctx, env.IntegrationID, env.TenantID)
	if err != nil {
		p.logger.Debug("No Business Central producer config", "connection_id", env.IntegrationID, "error", err)
		return nil
	}
	if cfg.AADTenantID == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.CompanyID == "" {
		return sdk.Permanent(fmt.Errorf("business central producer config incomplete for connection %s", env.IntegrationID))
	}
	if !json.Valid(env.Payload) {
		p.logger.Error("dropping: payload is not valid JSON", "envelope_id", env.ID)
		return sdk.Permanent(errors.New("payload is not valid JSON"))
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(p.httpClient)
	return p.write(ctx, cfg, tok, env.IntegrationID, env.Payload)
}

func (p *bcProducer) write(ctx context.Context, cfg *BCProducerConfig, tok *oauthcc.Client, connectionID string, payload []byte) error {
	access, err := tok.Token(ctx)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("acquire token: %w", err)) // token endpoint hiccup → retry
	}
	if cfg.effectiveMethod() == http.MethodPost && len(cfg.DedupeFields) > 0 {
		existing, err := p.alreadyExists(ctx, cfg, access, payload)
		if err != nil {
			return err
		}
		if existing != "" {
			bcSkippedExisting.WithLabelValues(connectionID).Inc()
			p.logger.Info("Already exists in Business Central; not writing again",
				"entity", cfg.effectiveEntity(), "connection_id", connectionID, "bc_id", existing)
			return nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, cfg.effectiveMethod(), cfg.entityURL(), bytes.NewReader(payload))
	if err != nil {
		return sdk.Permanent(err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if cfg.effectiveMethod() == http.MethodPatch {
		// BC requires If-Match for updates; "*" applies the update unconditionally.
		req.Header.Set("If-Match", "*")
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("business central request: %w", err))
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.logger.Info("Delivered to Business Central", "entity", cfg.effectiveEntity(), "status", resp.StatusCode)
		return nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable:
		return sdk.RateLimited(fmt.Errorf("business central %d: %s", resp.StatusCode, snippet(body)), 5*time.Second)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return sdk.Permanent(fmt.Errorf("business central auth %d: %s", resp.StatusCode, snippet(body)))
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return sdk.Permanent(fmt.Errorf("business central %d: %s", resp.StatusCode, snippet(body)))
	default:
		return sdk.Retriable(fmt.Errorf("business central %d: %s", resp.StatusCode, snippet(body)))
	}
}

// alreadyExists asks BC for a record matching the payload's DedupeFields and
// returns its id, or "" when there is none. A field missing or empty in the
// payload means "cannot tell": the write goes ahead as if the setting were
// off, with a log line. When BC cannot be asked the message waits (retriable)
// rather than risk a duplicate; auth failures are poison as for the write.
func (p *bcProducer) alreadyExists(ctx context.Context, cfg *BCProducerConfig, access string, payload []byte) (string, error) {
	var record map[string]any
	if err := json.Unmarshal(payload, &record); err != nil {
		return "", sdk.Permanent(fmt.Errorf("dedupe: payload is not a JSON object: %w", err))
	}
	filter, missing := odataEqualityFilter(cfg.DedupeFields, record)
	if missing != "" {
		p.logger.Warn("dedupe: payload field absent, writing without the existence check", "field", missing, "entity", cfg.effectiveEntity())
		return "", nil
	}
	q := url.Values{"$filter": {filter}, "$top": {"1"}, "$select": {"id"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.entityURL()+"?"+q.Encode(), nil)
	if err != nil {
		return "", sdk.Permanent(err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", sdk.Retriable(fmt.Errorf("dedupe lookup: %w", err))
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable:
		return "", sdk.RateLimited(fmt.Errorf("dedupe lookup %d: %s", resp.StatusCode, snippet(body)), 5*time.Second)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", sdk.Permanent(fmt.Errorf("dedupe lookup auth %d: %s", resp.StatusCode, snippet(body)))
	default:
		return "", sdk.Retriable(fmt.Errorf("dedupe lookup %d: %s", resp.StatusCode, snippet(body)))
	}
	var page struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return "", sdk.Retriable(fmt.Errorf("dedupe lookup: unreadable answer: %w", err))
	}
	if len(page.Value) == 0 {
		return "", nil
	}
	if page.Value[0].ID == "" {
		return "(id not returned)", nil
	}
	return page.Value[0].ID, nil
}

// odataEqualityFilter builds `a eq 'x' and b eq 2` from the payload's values
// of the named fields. Strings are quoted with single quotes doubled (OData's
// escape); numbers and booleans go as they are. The first field that is
// missing, empty, or of another type is returned as missing.
func odataEqualityFilter(fields []string, record map[string]any) (filter, missing string) {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		switch v := record[f].(type) {
		case string:
			if v == "" {
				return "", f
			}
			parts = append(parts, fmt.Sprintf("%s eq '%s'", f, strings.ReplaceAll(v, "'", "''")))
		case float64:
			parts = append(parts, fmt.Sprintf("%s eq %s", f, strconv.FormatFloat(v, 'f', -1, 64)))
		case bool:
			parts = append(parts, fmt.Sprintf("%s eq %t", f, v))
		default:
			return "", f
		}
	}
	if len(parts) == 0 {
		return "", "(none configured)"
	}
	return strings.Join(parts, " and "), ""
}

// bcSkippedExisting counts writes skipped because BC already had the record.
var bcSkippedExisting = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vrsky_bc_producer_skipped_existing_total",
	Help: "Business Central writes skipped because a record matching dedupe_fields already existed.",
}, []string{"connection_id"})

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// --- config helpers ---

func (cfg *BCProducerConfig) effectiveEntity() string {
	if cfg.Entity != "" {
		return strings.Trim(cfg.Entity, "/")
	}
	return defaultEntity
}

func (cfg *BCProducerConfig) effectiveMethod() string {
	if m := strings.ToUpper(strings.TrimSpace(cfg.Method)); m == http.MethodPatch || m == http.MethodPost {
		return m
	}
	return defaultMethod
}

func (cfg *BCProducerConfig) effectiveScope() string {
	if cfg.Scope != "" {
		return cfg.Scope
	}
	return defaultScope
}

func (cfg *BCProducerConfig) effectiveTokenURL() string {
	if cfg.TokenURL != "" {
		return cfg.TokenURL
	}
	return fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", cfg.AADTenantID)
}

func (cfg *BCProducerConfig) entityURL() string {
	host := cfg.APIBaseURL
	if host == "" {
		env := cfg.Environment
		if env == "" {
			env = defaultEnvironment
		}
		host = fmt.Sprintf("%s/v2.0/%s/%s/api/v2.0", defaultAPIHost, cfg.AADTenantID, env)
	}
	host = strings.TrimRight(host, "/")
	return fmt.Sprintf("%s/companies(%s)/%s", host, cfg.CompanyID, cfg.effectiveEntity())
}

// --- DB ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Business Central producer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (p *bcProducer) getConfig(ctx context.Context, connectionID, tenantID string) (*BCProducerConfig, error) {
	var nodesJSON json.RawMessage
	if err := p.db.QueryRowContext(ctx,
		`SELECT nodes FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON); err != nil {
		return nil, fmt.Errorf("connection not found: %w", err)
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
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, n.Config)
		if rerr != nil {
			return nil, fmt.Errorf("resolve secrets: %w", rerr)
		}
		var nc nodeConfig
		if err := json.Unmarshal(resolved, &nc); err != nil {
			continue
		}
		if nc.Type == "business_central" && nc.BusinessCentral != nil {
			return nc.BusinessCentral, nil
		}
	}
	return nil, errors.New("no business_central producer node found")
}

// ServesConnection reports whether this connection has a Business Central destination —
// mirroring Deliver's own "no config -> not ours" semantics — so the SDK can
// ack foreign connections before rehydrating large payloads (sdk.ConnectionScoped).
func (p *bcProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	_, err := p.getConfig(ctx, connectionID, tenantID)
	return err == nil
}
