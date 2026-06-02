package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthtoken"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

const (
	defaultAPIVersion = "v60.0"
	// bulkThreshold: batches at or above this use Bulk API 2.0 instead of
	// per-record REST calls (#79 acceptance criterion #3).
	defaultBulkThreshold = 200
)

// salesforceProducer writes pipeline records into Salesforce. It is an SDK
// Producer: Configure wires deps, Deliver writes one envelope's records to
// every matching Salesforce producer node for the connection.
type salesforceProducer struct {
	sdk.BaseProducer

	db     *sql.DB
	logger *slog.Logger

	httpClient    *http.Client
	tokens        *oauthtoken.Client
	resolveToken  func(ctx context.Context, tenantID, grantID string, force bool) (string, error)
	bulkThreshold int

	cache     map[string][]*sfTarget
	cacheTime map[string]time.Time
	cacheTTL  time.Duration
	cacheMu   sync.RWMutex
}

// SalesforceProducerConfig is the per-node configuration (config.salesforce).
type SalesforceProducerConfig struct {
	InstanceURL     string `json:"instance_url"`
	OAuthGrantID    string `json:"oauth_grant_id"`
	Object          string `json:"object"`            // sObject, e.g. "Account"
	Operation       string `json:"operation"`         // "insert" (default) or "upsert"
	ExternalIDField string `json:"external_id_field"` // required for upsert
	APIVersion      string `json:"api_version"`       // default v60.0
}

type sfTarget struct {
	cfg            SalesforceProducerConfig
	predecessorID  string
	predIsConsumer bool
}

// errBadPayload marks an envelope whose payload isn't JSON (poison message).
var errBadPayload = errors.New("payload is not valid JSON")

// Configure wires dependencies. Called once by the runner before Deliver.
func (p *salesforceProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("salesforce-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	if p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if p.bulkThreshold == 0 {
		p.bulkThreshold = defaultBulkThreshold
	}
	p.cache = make(map[string][]*sfTarget)
	p.cacheTime = make(map[string]time.Time)
	if p.cacheTTL == 0 {
		p.cacheTTL = 5 * time.Minute
	}
	p.tokens = oauthtoken.New(os.Getenv("MGMT_API_URL"), os.Getenv("OAUTH_TOKEN_SERVICE_SECRET"))
	if p.resolveToken == nil {
		p.resolveToken = func(ctx context.Context, tenantID, grantID string, force bool) (string, error) {
			if !p.tokens.Configured() {
				return "", errors.New("OAuth token resolution not configured (set MGMT_API_URL + OAUTH_TOKEN_SERVICE_SECRET)")
			}
			if force {
				return p.tokens.ForceToken(ctx, tenantID, grantID)
			}
			return p.tokens.Token(ctx, tenantID, grantID)
		}
	}
	p.logger.Info("salesforce-producer configured", "bulk_threshold", p.bulkThreshold)
	return nil
}

// Deliver writes the envelope's records into every matching Salesforce target.
// Transient failures (5xx, network, token, bulk-job errors) → sdk.Retriable; a
// non-JSON payload → sdk.Permanent. A missing producer config for the connection
// is not an error.
func (p *salesforceProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connID := env.IntegrationID
	if connID == "" {
		return nil
	}
	targets, err := p.getTargets(ctx, connID, env.TenantID)
	if err != nil {
		p.logger.Debug("No Salesforce producer config", "connection_id", connID, "error", err)
		return nil
	}

	records, err := parseRecords(env.Payload)
	if err != nil {
		if errors.Is(err, errBadPayload) {
			p.logger.Error("dropping: payload is not valid JSON", "error", err, "envelope_id", env.ID)
			return sdk.Permanent(err)
		}
		return sdk.Retriable(err)
	}
	if len(records) == 0 {
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
		if err := p.writeRecords(ctx, env.TenantID, &t.cfg, records); err != nil && transient == nil {
			transient = err
		}
	}
	if transient != nil {
		return sdk.Retriable(transient)
	}
	return nil
}

func (p *salesforceProducer) writeRecords(ctx context.Context, tenantID string, cfg *SalesforceProducerConfig, records []map[string]any) error {
	if cfg.Object == "" {
		p.logger.Error("Salesforce producer missing object; skipping")
		return nil
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = defaultAPIVersion
	}
	op := cfg.Operation
	if op == "" {
		op = "insert"
	}
	if op == "upsert" && cfg.ExternalIDField == "" {
		p.logger.Error("Salesforce upsert requires external_id_field; skipping", "object", cfg.Object)
		return nil
	}

	if len(records) >= p.bulkThreshold {
		return p.bulkWrite(ctx, tenantID, cfg, op, records)
	}
	return p.restWrite(ctx, tenantID, cfg, op, records)
}

// restWrite writes records one-by-one via the REST sObject API. Per-record 4xx
// errors are logged and skipped (poison record); transient errors are returned.
func (p *salesforceProducer) restWrite(ctx context.Context, tenantID string, cfg *SalesforceProducerConfig, op string, records []map[string]any) error {
	base := strings.TrimRight(cfg.InstanceURL, "/")
	var transient error
	ok := 0
	for _, rec := range records {
		var method, u string
		if op == "upsert" {
			extVal, _ := rec[cfg.ExternalIDField].(string)
			if extVal == "" {
				if v, present := rec[cfg.ExternalIDField]; present {
					extVal = fmt.Sprint(v)
				}
			}
			if extVal == "" {
				p.logger.Error("upsert record missing external id value; skipping", "field", cfg.ExternalIDField)
				continue
			}
			method = http.MethodPatch
			u = fmt.Sprintf("%s/services/data/%s/sobjects/%s/%s/%s", base, cfg.APIVersion, cfg.Object, cfg.ExternalIDField, url.PathEscape(extVal))
		} else {
			method = http.MethodPost
			u = fmt.Sprintf("%s/services/data/%s/sobjects/%s", base, cfg.APIVersion, cfg.Object)
		}
		body, _ := json.Marshal(rec)
		status, respBody, err := p.doRequest(ctx, tenantID, cfg.OAuthGrantID, method, u, "application/json", body)
		if err != nil {
			if transient == nil {
				transient = err
			}
			continue
		}
		switch {
		case status >= 200 && status < 300:
			ok++
		case status >= 500 || status == http.StatusTooManyRequests:
			if transient == nil {
				transient = fmt.Errorf("salesforce %d: %s", status, truncate(respBody))
			}
		default: // 4xx — bad record, won't improve on retry
			p.logger.Error("Salesforce rejected record", "status", status, "object", cfg.Object, "body", truncate(respBody))
		}
	}
	p.logger.Info("Salesforce REST write", "object", cfg.Object, "op", op, "ok", ok, "total", len(records))
	return transient
}

// bulkWrite ingests records via Bulk API 2.0: create job → upload CSV → close.
func (p *salesforceProducer) bulkWrite(ctx context.Context, tenantID string, cfg *SalesforceProducerConfig, op string, records []map[string]any) error {
	base := strings.TrimRight(cfg.InstanceURL, "/")

	jobReq := map[string]string{"object": cfg.Object, "operation": op, "contentType": "CSV", "lineEnding": "LF"}
	if op == "upsert" {
		jobReq["externalIdFieldName"] = cfg.ExternalIDField
	}
	jobBody, _ := json.Marshal(jobReq)
	status, respBody, err := p.doRequest(ctx, tenantID, cfg.OAuthGrantID, http.MethodPost,
		fmt.Sprintf("%s/services/data/%s/jobs/ingest", base, cfg.APIVersion), "application/json", jobBody)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("create bulk job %d: %s", status, truncate(respBody))
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &job); err != nil || job.ID == "" {
		return fmt.Errorf("bulk job response missing id: %s", truncate(respBody))
	}

	csvData, err := recordsToCSV(records)
	if err != nil {
		return fmt.Errorf("build CSV: %w", err)
	}
	status, respBody, err = p.doRequest(ctx, tenantID, cfg.OAuthGrantID, http.MethodPut,
		fmt.Sprintf("%s/services/data/%s/jobs/ingest/%s/batches", base, cfg.APIVersion, job.ID), "text/csv", csvData)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("upload bulk data %d: %s", status, truncate(respBody))
	}

	closeBody := []byte(`{"state":"UploadComplete"}`)
	status, respBody, err = p.doRequest(ctx, tenantID, cfg.OAuthGrantID, http.MethodPatch,
		fmt.Sprintf("%s/services/data/%s/jobs/ingest/%s", base, cfg.APIVersion, job.ID), "application/json", closeBody)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("close bulk job %d: %s", status, truncate(respBody))
	}
	p.logger.Info("Salesforce Bulk API job submitted", "object", cfg.Object, "op", op, "job_id", job.ID, "records", len(records))
	return nil
}

// doRequest performs an authenticated request, refreshing the token once on 401.
func (p *salesforceProducer) doRequest(ctx context.Context, tenantID, grantID, method, fullURL, contentType string, body []byte) (int, []byte, error) {
	send := func(force bool) (*http.Response, error) {
		tok, err := p.resolveToken(ctx, tenantID, grantID, force)
		if err != nil {
			return nil, fmt.Errorf("resolve OAuth token: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json")
		return p.httpClient.Do(req)
	}
	resp, err := send(false)
	if err != nil {
		return 0, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		p.logger.Info("Salesforce returned 401; refreshing token and retrying once", "grant_id", grantID)
		resp, err = send(true)
		if err != nil {
			return 0, nil, fmt.Errorf("request after token refresh: %w", err)
		}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, respBody, nil
}

// --- helpers ---

func parseRecords(payload []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, nil
	}
	switch trimmed[0] {
	case '[':
		var rows []map[string]any
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("%w: %v", errBadPayload, err)
		}
		return rows, nil
	case '{':
		var one map[string]any
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return nil, fmt.Errorf("%w: %v", errBadPayload, err)
		}
		return []map[string]any{one}, nil
	default:
		return nil, errBadPayload
	}
}

// recordsToCSV renders records to CSV with a header = the sorted union of keys.
func recordsToCSV(records []map[string]any) ([]byte, error) {
	keySet := map[string]struct{}{}
	for _, r := range records {
		for k := range r {
			keySet[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(keys); err != nil {
		return nil, err
	}
	for _, r := range records {
		row := make([]string, len(keys))
		for i, k := range keys {
			row[i] = csvValue(r[k])
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func csvValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	default:
		return fmt.Sprint(t)
	}
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

type node struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// getTargets returns the connection's Salesforce producer targets (cached).
// lint:tenant-ok — connection lookup by PK; tenant scoping enforced upstream at deploy.
func (p *salesforceProducer) getTargets(ctx context.Context, connID, tenantID string) ([]*sfTarget, error) {
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
	var nodes []node
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	var targets []*sfTarget
	for _, n := range nodes {
		if n.Type != "producer" {
			continue
		}
		var nc struct {
			Type       string                    `json:"type"`
			Salesforce *SalesforceProducerConfig `json:"salesforce"`
		}
		if err := json.Unmarshal(n.Config, &nc); err != nil {
			continue
		}
		if nc.Type != "salesforce" || nc.Salesforce == nil || nc.Salesforce.Object == "" {
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
		targets = append(targets, &sfTarget{cfg: *nc.Salesforce, predecessorID: predID, predIsConsumer: predIsConsumer})
	}
	if len(targets) == 0 {
		return nil, errors.New("no salesforce producer node found")
	}

	p.cacheMu.Lock()
	p.cache[connID] = targets
	p.cacheTime[connID] = time.Now()
	p.cacheMu.Unlock()
	return targets, nil
}
