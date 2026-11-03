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
	"github.com/ValueRetail/vrsky/pkg/safeurl"
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
	// allowPrivate is the operator's dev switch (ALLOW_PRIVATE_UPSTREAMS): it
	// lets api_base_url and token_url be http or private, for a mock
	// upstream. Never set by a tenant.
	allowPrivate bool
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
	p.allowPrivate = safeurl.AllowPrivateFromEnv()
	if p.httpClient == nil {
		// api_base_url and token_url are tenant config and every request
		// carries a credential (#686): refuse private, loopback, link-local
		// and metadata addresses at dial time. The token request and the
		// dedupe lookup use the same client.
		p.httpClient = safeurl.NewClient(60*time.Second, p.allowPrivate)
	}
	p.logger.Info("business-central-producer configured")
	return nil
}

func (p *bcProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	if env.IntegrationID == "" {
		return nil
	}
	cfg, rt, err := p.getConfig(ctx, env.IntegrationID, env.TenantID)
	if err != nil {
		if sdk.IsConfigUnavailable(err) {
			return err // not acked: retried, then the DLQ
		}
		p.logger.Debug("No Business Central producer config", "connection_id", env.IntegrationID, "error", err)
		return nil
	}
	if !rt.accepts(env) {
		// Not from this node's predecessor: the source's raw page in a
		// pipeline with a converter in front. The converted copy follows.
		return nil
	}
	if cfg.AADTenantID == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.CompanyID == "" {
		return sdk.Permanent(fmt.Errorf("business central producer config incomplete for connection %s", env.IntegrationID))
	}
	// A refused URL is retried and then dead-lettered with its payload, so the
	// message can be replayed once the tenant fixes the field (#686). Not
	// sdk.Permanent: that acks and drops the message.
	if err := cfg.checkURLs(p.allowPrivate); err != nil {
		p.logger.Error("Business Central address refused", "connection_id", env.IntegrationID, "error", err)
		return sdk.Retriable(err)
	}
	if !json.Valid(env.Payload) {
		p.logger.Error("dropping: payload is not valid JSON", "envelope_id", env.ID)
		return sdk.Permanent(errors.New("payload is not valid JSON"))
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(p.httpClient)
	err = p.write(ctx, cfg, tok, env.IntegrationID, env.Payload)
	if errors.Is(err, safeurl.ErrBlockedAddress) {
		// The error becomes the message's last error, which the tenant sees
		// in the DLQ view: log which address the name resolved to, return
		// the refusal without it. Retriable, like any other dial failure.
		p.logger.Warn("Business Central request refused at dial", "connection_id", env.IntegrationID, "error", err)
		return sdk.Retriable(fmt.Errorf("business central: %w", safeurl.ErrBlockedAddress))
	}
	return err
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

// apiRoot is the API v2.0 root: api_base_url, or the cloud default built from
// the Entra tenant and the environment.
func (cfg *BCProducerConfig) apiRoot() string {
	if cfg.APIBaseURL != "" {
		return cfg.APIBaseURL
	}
	env := cfg.Environment
	if env == "" {
		env = defaultEnvironment
	}
	return fmt.Sprintf("%s/v2.0/%s/%s/api/v2.0", defaultAPIHost, cfg.AADTenantID, env)
}

func (cfg *BCProducerConfig) entityURL() string {
	host := strings.TrimRight(cfg.apiRoot(), "/")
	return fmt.Sprintf("%s/companies(%s)/%s", host, cfg.CompanyID, cfg.effectiveEntity())
}

// checkURLs refuses an API root or token URL the producer must not call: both
// come from tenant config, the token request carries the client secret and
// the write the bearer token (#686). The dial-time check in the HTTP client is
// the enforcement; this gives an early, readable error. allowPrivate is the
// operator's dev switch.
func (cfg *BCProducerConfig) checkURLs(allowPrivate bool) error {
	if err := safeurl.CheckBaseURL(cfg.apiRoot(), allowPrivate); err != nil {
		return fmt.Errorf("api_base_url refused: %w", err)
	}
	if err := safeurl.CheckBaseURL(cfg.effectiveTokenURL(), allowPrivate); err != nil {
		return fmt.Errorf("token_url refused: %w", err)
	}
	return nil
}

// --- DB ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its Business Central producer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (p *bcProducer) getConfig(ctx context.Context, connectionID, tenantID string) (*BCProducerConfig, route, error) {
	var nodesJSON json.RawMessage
	var edgesJSON []byte
	if err := p.db.QueryRowContext(ctx,
		`SELECT nodes, edges FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON, &edgesJSON); err != nil {
		return nil, route{}, sdk.ConfigReadError(err)
	}
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, route{}, fmt.Errorf("parse nodes: %w", err)
	}
	reader := crypto.NewSQLSecretReader(p.db)
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		// Find our node before resolving secrets: an unreadable secret on
		// another connector's node is not a reason to hold this message.
		var nc nodeConfig
		if err := json.Unmarshal(n.Config, &nc); err != nil || nc.Type != "business_central" || nc.BusinessCentral == nil {
			continue
		}
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, n.Config)
		if rerr != nil {
			return nil, route{}, sdk.ConfigUnavailable(fmt.Errorf("resolve secrets: %w", rerr))
		}
		nc = nodeConfig{}
		if err := json.Unmarshal(resolved, &nc); err != nil || nc.BusinessCentral == nil {
			continue
		}
		return nc.BusinessCentral, routeTo(n.ID, nodes, edgesJSON), nil
	}
	return nil, route{}, errors.New("no business_central producer node found")
}

// route is where the destination node sits in its pipeline: which node's
// output it takes. Every node of a pipeline publishes on the same subject, so
// with a converter in front of this destination it receives the source's raw
// page as well as the converter's result, and must take only the latter
// (#435). Same rule as the other producers (kafka, http, db, ...).
type route struct {
	predecessorID  string
	predIsConsumer bool
}

// accepts reports whether env comes from this destination's predecessor. A
// node with no incoming edge accepts everything, as before.
func (r route) accepts(env *envelope.Envelope) bool {
	last, _ := env.Metadata["_last_processed_by"].(string)
	if r.predIsConsumer {
		return last == ""
	}
	if r.predecessorID != "" {
		return last == r.predecessorID
	}
	return true
}

// routeTo finds the node feeding nodeID through edgesJSON (connections.edges).
func routeTo(nodeID string, nodes []node, edgesJSON []byte) route {
	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if len(edgesJSON) > 0 {
		_ = json.Unmarshal(edgesJSON, &edges)
	}
	for _, e := range edges {
		if e.Target != nodeID {
			continue
		}
		r := route{predecessorID: e.Source}
		for _, m := range nodes {
			if m.ID == e.Source && m.Type == "consumer" {
				r.predIsConsumer = true
			}
		}
		return r
	}
	return route{}
}

// ServesConnection reports whether this connection has a Business Central destination —
// mirroring Deliver's own "no config -> not ours" semantics — so the SDK can
// ack foreign connections before rehydrating large payloads (sdk.ConnectionScoped).
func (p *bcProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	_, _, err := p.getConfig(ctx, connectionID, tenantID)
	// A failed read answers true so Deliver runs and returns it (#704).
	return err == nil || sdk.IsConfigUnavailable(err)
}
