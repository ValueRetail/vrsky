package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	modeFull  = "full"
	modeDelta = "delta"

	defaultBatchSize = 1000
	maxBatchSize     = 10000
	// maxBatchBytes splits a batch before its request body gets near
	// catalogue-api's 32 MiB body limit, whatever batch_size says.
	maxBatchBytes = 4 << 20

	// deliveryTimeout bounds one whole import. The runner sends in-progress
	// heartbeats during a long delivery, so this is a ceiling for a stuck
	// Bifrost, not the expected duration.
	deliveryTimeout = 20 * time.Minute
	// abortTimeout bounds the DELETE after a failure.
	abortTimeout = 10 * time.Second
	// requestTimeout bounds one request to Bifrost.
	requestTimeout = 60 * time.Second
)

// catalogueProducer delivers pipeline envelopes to Bifrost's catalogue-api.
type catalogueProducer struct {
	sdk.BaseProducer

	db         *sql.DB
	logger     *slog.Logger
	httpClient *http.Client

	// allowPrivate is the operator's dev switch (ALLOW_PRIVATE_UPSTREAMS): it
	// lets base_url be http or a private address, for a local Bifrost. Off in
	// production; a tenant cannot set it.
	allowPrivate bool
}

// CatalogueConfig is the per-node configuration (config.bifrost_catalogue).
// The token is stored as token_secret_id and resolved at delivery time.
type CatalogueConfig struct {
	// BaseURL is the catalogue-api origin, e.g. https://catalogue.example.com.
	BaseURL string `json:"base_url"`
	// Token is the tenant's bearer credential, with permission
	// catalogue.import.
	Token string `json:"token"`
	// TenantID is sent as X-Tenant-Id. Optional: Bifrost takes the tenant
	// from the credential, and rejects a header that disagrees.
	TenantID string `json:"tenant_id"`
	// Mode is "delta" (default: the items are upserted) or "full" (the items
	// are the whole catalogue and replace it).
	Mode string `json:"mode"`
	// BatchSize is the items per request, 1..10000, default 1000.
	BatchSize int `json:"batch_size"`
	// AllowShrinkReason overrides Bifrost's shrink guard for every import of
	// this node, and is recorded there with the reason. Leave it empty: the
	// guard is what stops a failed export from emptying the tills.
	AllowShrinkReason string `json:"allow_shrink_reason"`
}

type nodeConfig struct {
	Type    string           `json:"type"`
	Bifrost *CatalogueConfig `json:"bifrost_catalogue"`
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// boundNode is a destination node with its place in the pipeline.
type boundNode struct {
	id  string
	cfg *CatalogueConfig
	rt  route
}

// Configure wires dependencies. Called once before Deliver.
func (p *catalogueProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("bifrost-catalogue-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	p.allowPrivate = safeurl.AllowPrivateFromEnv()
	if p.allowPrivate {
		p.logger.Warn("ALLOW_PRIVATE_UPSTREAMS is on: private and loopback addresses are reachable. Never set this in production.")
	}
	if p.httpClient == nil {
		// base_url is tenant config and every request carries the tenant's
		// token: the client refuses private, loopback and link-local
		// addresses at dial time and never follows a redirect, which would
		// forward the token.
		p.httpClient = safeurl.NewClient(requestTimeout, p.allowPrivate)
	}
	p.logger.Info("bifrost-catalogue-producer configured")
	return nil
}

// Deliver runs one staged import per Bifrost node of the connection, with the
// envelope's items. A failure leaves nothing behind in Bifrost (the import is
// abandoned) and is returned as the SDK's retry signal, so a redelivery starts
// a new import from the same items.
//
// Redelivery is safe whatever happened to the last attempt, including a crash
// between commit and ack: a full import of the same list replaces the
// catalogue with the same list, a delta upserts the same rows.
func (p *catalogueProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	if env.IntegrationID == "" {
		return nil
	}
	nodes, err := p.getNodes(ctx, env.IntegrationID, env.TenantID)
	if err != nil {
		if sdk.IsConfigUnavailable(err) {
			return err // not acked: retried, then the DLQ
		}
		p.logger.Debug("No Bifrost catalogue producer config", "connection_id", env.IntegrationID, "error", err)
		return nil
	}

	var eligible []boundNode
	for _, n := range nodes {
		if n.rt.accepts(env) {
			eligible = append(eligible, n)
		}
	}
	if len(eligible) == 0 {
		return nil
	}

	// A node with a setting missing is node config, not a bad message: it is
	// retried and kept in the DLQ for a replay once fixed (#734).
	for _, n := range eligible {
		if err := p.validate(n.cfg); err != nil {
			return sdk.Retriable(fmt.Errorf("bifrost catalogue node %s of connection %s: %w", n.id, env.IntegrationID, err))
		}
	}

	var failed, permanent []error
	for _, n := range eligible {
		items, perr := parseItems(env.Payload, n.cfg.mode())
		if perr != nil {
			p.logger.Error("dropping: payload is not a list of catalogue items", "envelope_id", env.ID, "node_id", n.id, "error", perr)
			permanent = append(permanent, fmt.Errorf("node %s: %w", n.id, perr))
			continue
		}
		if err := p.runImport(ctx, env, n, items); err != nil {
			if sdk.IsPermanent(err) {
				permanent = append(permanent, fmt.Errorf("node %s: %w", n.id, err))
			} else {
				failed = append(failed, fmt.Errorf("node %s: %w", n.id, err))
			}
		}
	}
	switch {
	case len(failed) > 0:
		// A retry runs the nodes that took it again; that is harmless, each
		// import is idempotent. What retrying cannot fix rides along as text
		// only: a Permanent inside the join would make the SDK drop the
		// message, whatever else failed.
		for _, e := range permanent {
			failed = append(failed, errors.New(e.Error()))
		}
		return sdk.Retriable(errors.Join(failed...))
	case len(permanent) > 0:
		return sdk.Permanent(errors.Join(permanent...))
	}
	return nil
}

// validate checks the node's settings.
func (p *catalogueProducer) validate(c *CatalogueConfig) error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("no base_url")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("no token")
	}
	if err := safeurl.CheckBaseURL(c.BaseURL, p.allowPrivate); err != nil {
		return fmt.Errorf("base_url refused: %w", err)
	}
	switch m := strings.ToLower(strings.TrimSpace(c.Mode)); m {
	case "", modeFull, modeDelta:
	default:
		return fmt.Errorf("mode %q is not %q or %q", c.Mode, modeFull, modeDelta)
	}
	if c.BatchSize < 0 || c.BatchSize > maxBatchSize {
		return fmt.Errorf("batch_size %d is outside 1..%d", c.BatchSize, maxBatchSize)
	}
	if c.AllowShrinkReason != "" && strings.TrimSpace(c.AllowShrinkReason) == "" {
		return errors.New("allow_shrink_reason is blank; give a reason or leave it out")
	}
	return nil
}

func (c *CatalogueConfig) mode() string {
	if strings.EqualFold(strings.TrimSpace(c.Mode), modeFull) {
		return modeFull
	}
	return modeDelta
}

func (c *CatalogueConfig) batchSize() int {
	if c.BatchSize <= 0 {
		return defaultBatchSize
	}
	return c.BatchSize
}

// runImport is the staged import for one node: begin, items in batches,
// commit. Any failure after begin abandons the import, so Bifrost never holds
// a half-filled one for longer than it takes the DELETE to arrive (and an
// hour at most if that fails). There is no retry of a single request here: the
// whole import is retried by redelivery, from a fresh begin.
func (p *catalogueProducer) runImport(ctx context.Context, env *envelope.Envelope, n boundNode, items []json.RawMessage) (err error) {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()

	c := &bifrostClient{
		http:     p.httpClient,
		logger:   p.logger.With("tenant_id", env.TenantID, "connection_id", env.IntegrationID, "envelope_id", env.ID, "node_id", n.id),
		baseURL:  strings.TrimRight(strings.TrimSpace(n.cfg.BaseURL), "/"),
		token:    n.cfg.Token,
		tenantID: strings.TrimSpace(n.cfg.TenantID),
	}
	mode := n.cfg.mode()

	importID, err := c.begin(ctx, mode, strings.TrimSpace(n.cfg.AllowShrinkReason))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			c.abort(ctx, importID)
		}
	}()

	sent := 0
	batches := splitBatches(items, n.cfg.batchSize(), maxBatchBytes)
	for i, batch := range batches {
		// The key is stable per envelope and position, so a resend of the
		// same batch inside this import is recognised by Bifrost.
		key := fmt.Sprintf("%s-%d", env.ID, i+1)
		sent += len(batch)
		if err = c.addItems(ctx, importID, key, batch, sent); err != nil {
			return fmt.Errorf("batch %d of %d: %w", i+1, len(batches), err)
		}
	}

	rep, err := c.commit(ctx, importID)
	if err != nil {
		return err
	}
	c.logger.Info("Delivered catalogue to Bifrost", "mode", mode, "import_id", importID, "items", len(items),
		"batches", len(batches), "applied", rep.Applied, "catalogue_size", rep.CatalogueSize, "rejected", rep.Rejected)
	if rep.Rejected > 0 {
		// Bifrost accepts an import with some bad rows and says which. They
		// are not retried: the same rows are refused again.
		shown := rep.Errors
		if len(shown) > 10 {
			shown = shown[:10]
		}
		for _, e := range shown {
			c.logger.Warn("Bifrost rejected a catalogue row", "row", e.Row, "article_no", e.ArticleNo, "reason", c.reason([]byte(e.Error)))
		}
	}
	return nil
}

// parseItems reads the payload as the list of catalogue items to deliver: a
// JSON array of objects, or an object {"items": [...]}. In delta mode a single
// object is one item. In full mode it is refused: the payload is the whole
// catalogue, and a lone object is a source that emits one message per article,
// where every message would replace the catalogue with one article. The items
// are passed on as they are, so prices stay the strings the source wrote.
// An empty list is refused too: nothing is worth an import.
func parseItems(payload []byte, mode string) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, errors.New("payload is not valid JSON")
	}
	var items []json.RawMessage
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, fmt.Errorf("payload is not a list of items: %w", err)
		}
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, fmt.Errorf("payload is not an item: %w", err)
		}
		if raw, ok := obj["items"]; ok {
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, errors.New(`"items" is not a list`)
			}
		} else if mode == modeFull {
			return nil, errors.New(`full mode needs the whole catalogue as a list or {"items": [...]}; a single item would replace the catalogue with one article`)
		} else {
			items = []json.RawMessage{trimmed}
		}
	default:
		return nil, errors.New("payload is neither a list nor an object")
	}
	if len(items) == 0 {
		return nil, errors.New("payload holds no items")
	}
	for i, it := range items {
		if t := bytes.TrimSpace(it); len(t) == 0 || t[0] != '{' {
			return nil, fmt.Errorf("item %d is not an object", i+1)
		}
	}
	return items, nil
}

// splitBatches cuts items into runs of at most size items and, roughly,
// maxBytes of JSON. An item bigger than maxBytes is a batch of its own.
func splitBatches(items []json.RawMessage, size, maxBytes int) [][]json.RawMessage {
	var out [][]json.RawMessage
	var cur []json.RawMessage
	curBytes := 0
	for _, it := range items {
		if len(cur) > 0 && (len(cur) >= size || curBytes+len(it) > maxBytes) {
			out = append(out, cur)
			cur, curBytes = nil, 0
		}
		cur = append(cur, it)
		curBytes += len(it) + 1
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// getNodes loads the connection and returns its Bifrost catalogue nodes, with
// their token resolved. No such node is an error that is not
// ErrConfigUnavailable: the envelope is not for us.
// lint:tenant-ok — lookup is scoped by (id, tenant_id).
func (p *catalogueProducer) getNodes(ctx context.Context, connectionID, tenantID string) ([]boundNode, error) {
	var nodesJSON json.RawMessage
	var edgesJSON []byte
	if err := p.db.QueryRowContext(ctx,
		`SELECT nodes, edges FROM connections WHERE id = $1 AND tenant_id = $2`,
		connectionID, tenantID,
	).Scan(&nodesJSON, &edgesJSON); err != nil {
		return nil, sdk.ConfigReadError(err)
	}
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	reader := crypto.NewSQLSecretReader(p.db)
	var out []boundNode
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		// Find our nodes before resolving secrets: an unreadable secret on
		// another connector's node is not a reason to hold this message.
		var nc nodeConfig
		if err := json.Unmarshal(n.Config, &nc); err != nil || nc.Type != "bifrost_catalogue" {
			continue
		}
		resolved, rerr := crypto.ResolveSecretsInJSON(ctx, reader, tenantID, n.Config)
		if rerr != nil {
			return nil, sdk.ConfigUnavailable(fmt.Errorf("resolve secrets: %w", rerr))
		}
		nc = nodeConfig{}
		if err := json.Unmarshal(resolved, &nc); err != nil {
			continue
		}
		cfg := nc.Bifrost
		if cfg == nil {
			// Still ours; validate reports the missing settings.
			cfg = &CatalogueConfig{}
		}
		out = append(out, boundNode{id: n.ID, cfg: cfg, rt: routeTo(n.ID, nodes, edgesJSON)})
	}
	if len(out) == 0 {
		return nil, errors.New("no bifrost catalogue producer node found")
	}
	return out, nil
}

// ServesConnection reports whether this connection has a Bifrost catalogue
// destination, mirroring Deliver's own "no config -> not ours" semantics so
// the SDK can ack foreign connections before rehydrating large payloads
// (sdk.ConnectionScoped).
func (p *catalogueProducer) ServesConnection(ctx context.Context, tenantID, connectionID string) bool {
	if connectionID == "" {
		return false
	}
	_, err := p.getNodes(ctx, connectionID, tenantID)
	// A failed read answers true so Deliver runs and returns it (#704).
	return err == nil || sdk.IsConfigUnavailable(err)
}

// route is where the destination node sits in its pipeline: which node's
// output it takes. Every node of a pipeline publishes on the same subject, so
// with a converter in front of this destination it receives the source's raw
// page as well as the converter's result, and must take only the latter
// (#435). Same rule as the other producers.
type route struct {
	predecessorID  string
	predIsConsumer bool
}

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
