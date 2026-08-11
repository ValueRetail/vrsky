package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	authBasic  = "basic"
	authOAuth2 = "oauth2"
)

// sapProducer delivers pipeline envelopes into SAP S/4HANA via its OData REST
// API — e.g. creating sales orders (deep insert with to_Item) or posting goods
// movements. It is an SDK Producer: Configure wires deps, Deliver writes one
// envelope. Writes require an SAP CSRF token, so the shared HTTP client carries
// a cookie jar to hold the session cookie the token is bound to.
type sapProducer struct {
	sdk.BaseProducer

	db         *sql.DB
	logger     *slog.Logger
	httpClient *http.Client
}

// SAPProducerConfig is the per-node configuration (config.sap_s4hana).
type SAPProducerConfig struct {
	Host         string `json:"host"`
	APIBaseURL   string `json:"api_base_url"`
	Service      string `json:"service"`     // e.g. API_SALES_ORDER_SRV
	EntitySet    string `json:"entity_set"`  // write target, e.g. A_SalesOrder
	SAPClient    string `json:"sap_client"`  // optional mandt
	Method       string `json:"method"`      // POST (default) or PATCH

	AuthType     string `json:"auth_type"`     // basic (default) | oauth2
	Username     string `json:"username"`      // basic
	Password     string `json:"password"`      // basic: from password_secret_id
	ClientID     string `json:"client_id"`     // oauth2
	ClientSecret string `json:"client_secret"` // oauth2: from client_secret_secret_id
	TokenURL     string `json:"token_url"`     // oauth2 override
	Scope        string `json:"scope"`         // oauth2 optional
}

type nodeConfig struct {
	Type string             `json:"type"`
	SAP  *SAPProducerConfig `json:"sap_s4hana"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (p *sapProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("sap-s4hana-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.httpClient == nil {
		// A cookie jar is required: the SAP CSRF token is bound to the session
		// cookie returned by the Fetch request and must be echoed on the write.
		jar, _ := cookiejar.New(nil)
		p.httpClient = &http.Client{Timeout: 60 * time.Second, Jar: jar}
	}
	p.logger.Info("sap-s4hana-producer configured")
	return nil
}

func (p *sapProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	if env.IntegrationID == "" {
		return nil
	}
	cfg, err := p.getConfig(ctx, env.IntegrationID, env.TenantID)
	if err != nil {
		p.logger.Debug("No SAP S/4HANA producer config", "connection_id", env.IntegrationID, "error", err)
		return nil
	}
	if err := cfg.validate(); err != nil {
		return sdk.Permanent(fmt.Errorf("sap s/4hana producer config incomplete for connection %s: %w", env.IntegrationID, err))
	}
	if !json.Valid(env.Payload) {
		p.logger.Error("dropping: payload is not valid JSON", "envelope_id", env.ID)
		return sdk.Permanent(errors.New("payload is not valid JSON"))
	}
	auth := newAuthorizer(cfg, p.httpClient)
	return p.write(ctx, cfg, auth, env.Payload)
}

// write fetches a CSRF token then POSTs/PATCHes the payload. A 403 with
// "x-csrf-token: Required" means the token/session expired — re-fetch once.
func (p *sapProducer) write(ctx context.Context, cfg *SAPProducerConfig, auth authorizer, payload []byte) error {
	token, err := p.fetchCSRF(ctx, cfg, auth)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("fetch csrf token: %w", err))
	}
	resp, body, err := p.send(ctx, cfg, auth, token, payload)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("sap s/4hana request: %w", err))
	}
	if resp.StatusCode == http.StatusForbidden && strings.EqualFold(resp.Header.Get("X-CSRF-Token"), "Required") {
		token, err = p.fetchCSRF(ctx, cfg, auth)
		if err != nil {
			return sdk.Retriable(fmt.Errorf("re-fetch csrf token: %w", err))
		}
		resp, body, err = p.send(ctx, cfg, auth, token, payload)
		if err != nil {
			return sdk.Retriable(fmt.Errorf("sap s/4hana request: %w", err))
		}
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		p.logger.Info("Delivered to SAP S/4HANA", "service", cfg.Service, "entity_set", cfg.EntitySet, "status", resp.StatusCode)
		return nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable:
		return sdk.RateLimited(fmt.Errorf("sap s/4hana %d: %s", resp.StatusCode, snippet(body)), 5*time.Second)
	case resp.StatusCode == http.StatusUnauthorized:
		return sdk.Permanent(fmt.Errorf("sap s/4hana auth %d: %s", resp.StatusCode, snippet(body)))
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return sdk.Permanent(fmt.Errorf("sap s/4hana %d: %s", resp.StatusCode, snippet(body)))
	default:
		return sdk.Retriable(fmt.Errorf("sap s/4hana %d: %s", resp.StatusCode, snippet(body)))
	}
}

// fetchCSRF does a GET with "X-CSRF-Token: Fetch"; the response header carries
// the token and Set-Cookie carries the session cookie (stored by the jar).
func (p *sapProducer) fetchCSRF(ctx context.Context, cfg *SAPProducerConfig, auth authorizer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.csrfURL(), nil)
	if err != nil {
		return "", err
	}
	if err := auth.apply(ctx, req); err != nil {
		return "", err
	}
	req.Header.Set("X-CSRF-Token", "Fetch")
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("csrf fetch %s", resp.Status)
	}
	token := resp.Header.Get("X-CSRF-Token")
	if token == "" {
		return "", errors.New("no X-CSRF-Token in response")
	}
	return token, nil
}

func (p *sapProducer) send(ctx context.Context, cfg *SAPProducerConfig, auth authorizer, token string, payload []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, cfg.effectiveMethod(), cfg.entityURL(), bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	if err := auth.apply(ctx, req); err != nil {
		return nil, nil, err
	}
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if cfg.effectiveMethod() == http.MethodPatch {
		req.Header.Set("If-Match", "*")
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	return resp, body, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// --- auth (basic or OAuth2 client-credentials) ---

type authorizer interface {
	apply(ctx context.Context, req *http.Request) error
}

type basicAuth struct{ header string }

func (b basicAuth) apply(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", b.header)
	return nil
}

type bearerAuth struct{ tok *oauthcc.Client }

func (o bearerAuth) apply(ctx context.Context, req *http.Request) error {
	access, err := o.tok.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	return nil
}

func newAuthorizer(cfg *SAPProducerConfig, hc *http.Client) authorizer {
	if cfg.effectiveAuthType() == authOAuth2 {
		tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(hc)
		return bearerAuth{tok: tok}
	}
	creds := base64.StdEncoding.EncodeToString([]byte(cfg.Username + ":" + cfg.Password))
	return basicAuth{header: "Basic " + creds}
}

// --- config helpers ---

func (cfg *SAPProducerConfig) effectiveAuthType() string {
	if strings.ToLower(strings.TrimSpace(cfg.AuthType)) == authOAuth2 {
		return authOAuth2
	}
	return authBasic
}

func (cfg *SAPProducerConfig) effectiveMethod() string {
	if m := strings.ToUpper(strings.TrimSpace(cfg.Method)); m == http.MethodPatch || m == http.MethodPost {
		return m
	}
	return http.MethodPost
}

func (cfg *SAPProducerConfig) effectiveTokenURL() string {
	if cfg.TokenURL != "" {
		return cfg.TokenURL
	}
	return fmt.Sprintf("https://%s/sap/bc/sec/oauth2/token", cfg.Host)
}

func (cfg *SAPProducerConfig) serviceRoot() string {
	if cfg.APIBaseURL != "" {
		return strings.TrimRight(cfg.APIBaseURL, "/")
	}
	return fmt.Sprintf("https://%s/sap/opu/odata/sap/%s", cfg.Host, cfg.Service)
}

func (cfg *SAPProducerConfig) csrfURL() string {
	u := cfg.serviceRoot() + "/"
	if cfg.SAPClient != "" {
		u += "?sap-client=" + url.QueryEscape(cfg.SAPClient)
	}
	return u
}

func (cfg *SAPProducerConfig) entityURL() string {
	u := cfg.serviceRoot() + "/" + strings.Trim(cfg.EntitySet, "/")
	if cfg.SAPClient != "" {
		u += "?sap-client=" + url.QueryEscape(cfg.SAPClient)
	}
	return u
}

func (cfg *SAPProducerConfig) validate() error {
	if cfg.APIBaseURL == "" && (cfg.Host == "" || cfg.Service == "") {
		return errors.New("need host + service (or api_base_url)")
	}
	if cfg.EntitySet == "" {
		return errors.New("need entity_set")
	}
	if cfg.effectiveAuthType() == authOAuth2 {
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			return errors.New("oauth2 auth needs client_id + client_secret_secret_id")
		}
	} else if cfg.Username == "" || cfg.Password == "" {
		return errors.New("basic auth needs username + password_secret_id")
	}
	return nil
}

// --- DB ---

// getConfig loads the connection, resolves *_secret_id references, and extracts
// its SAP S/4HANA producer node config.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (p *sapProducer) getConfig(ctx context.Context, connectionID, tenantID string) (*SAPProducerConfig, error) {
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
		if nc.Type == "sap_s4hana" && nc.SAP != nil {
			return nc.SAP, nil
		}
	}
	return nil, errors.New("no sap_s4hana producer node found")
}
