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

	// written remembers records that went in during a delivery that then
	// failed, so the redelivery does not send them again (#735).
	written *writtenSet
	// jobs remembers the Bulk API job submitted for a message and target
	// until it is resolved, so a retry finds it instead of submitting a
	// second one (#760).
	jobs *jobSet

	// Bulk API job polling and result reading (bulk.go); zero means the
	// default.
	bulkPollTimeout time.Duration
	bulkPollInitial time.Duration
	bulkPollMax     time.Duration
	bulkResultsMax  int64
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
	nodeID         string
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
		p.httpClient = &http.Client{
			Timeout: 60 * time.Second,
			// Never follow redirects: requests carry the live Salesforce OAuth
			// token in the Authorization header, and Go only strips it on a
			// cross-host redirect. A redirect to a different subdomain of the
			// same registered domain would forward the token. Surface redirects
			// instead of silently following them.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	if p.bulkThreshold == 0 {
		p.bulkThreshold = defaultBulkThreshold
	}
	p.cache = make(map[string][]*sfTarget)
	p.cacheTime = make(map[string]time.Time)
	if p.cacheTTL == 0 {
		p.cacheTTL = 5 * time.Minute
	}
	if p.written == nil {
		p.written = newWrittenSet(writtenTTL, writtenMax)
	}
	if p.jobs == nil {
		p.jobs = newJobSet(writtenTTL, bulkJobsMax)
	}
	if p.bulkPollTimeout == 0 {
		p.bulkPollTimeout = defaultBulkPollTimeout
	}
	if p.bulkPollInitial == 0 {
		p.bulkPollInitial = defaultBulkPollInitial
	}
	if p.bulkPollMax == 0 {
		p.bulkPollMax = defaultBulkPollMax
	}
	if p.bulkResultsMax == 0 {
		p.bulkResultsMax = defaultBulkResultsMax
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
//
// Retryable (NAK, then the DLQ after five attempts): a record Salesforce
// rejects (a 4xx, named by its index with Salesforce's error code), a
// transient failure (network, token, 401/403 after the token refresh, 408,
// 429, 5xx), and the same answers while creating, filling or closing a Bulk
// API job (#735); a record a Bulk job refuses or leaves unprocessed, and a job
// that is not done within the poll bound (#760). Records that went in are
// remembered, so the redelivery sends only the rest. A non-JSON payload → sdk.Permanent. A missing
// producer config for the connection is not an error.
func (p *salesforceProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connID := env.IntegrationID
	if connID == "" {
		return nil
	}
	targets, err := p.getTargets(ctx, connID, env.TenantID)
	if err != nil {
		if sdk.IsConfigUnavailable(err) {
			return err // not acked: retried, then the DLQ
		}
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

	var res writeResult
	var errs []error
	for _, t := range targets {
		if t.predIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !t.predIsConsumer && t.predecessorID != "" && lastProcessedBy != t.predecessorID {
			continue
		}
		if err := p.writeRecords(ctx, env, t, records, &res); err != nil {
			errs = append(errs, fmt.Errorf("node %s: %w", t.nodeID, err))
		}
	}
	if len(errs) > 0 {
		// Remember what went in, so the redelivery skips it.
		p.written.add(time.Now(), res.fresh...)
		return sdk.Retriable(errors.Join(errs...))
	}
	p.written.forget(res.done...)
	return nil
}

// writeResult collects, across a delivery's targets, the records that are in
// Salesforce: fresh ones went in during this attempt, done ones are fresh plus
// those an earlier attempt wrote.
type writeResult struct {
	fresh []writtenKey
	done  []writtenKey
}

func (r *writeResult) wrote(k writtenKey) {
	r.fresh = append(r.fresh, k)
	r.done = append(r.done, k)
}

// writeRecords takes cfg by value: the target is cached and shared between
// deliveries, and the API version default is filled in here.
func (p *salesforceProducer) writeRecords(ctx context.Context, env *envelope.Envelope, t *sfTarget, records []map[string]any, res *writeResult) error {
	cfg := t.cfg
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
	key := func(i int) writtenKey {
		return writtenKey{
			connID:      env.IntegrationID,
			envelopeID:  env.ID,
			nodeID:      t.nodeID,
			instanceURL: cfg.InstanceURL,
			object:      cfg.Object,
			operation:   op,
			externalID:  cfg.ExternalIDField,
			record:      i,
		}
	}

	if len(records) >= p.bulkThreshold {
		return p.bulkWrite(ctx, env.TenantID, &cfg, op, records, key, res)
	}
	return p.restWrite(ctx, env.TenantID, &cfg, op, records, key, res)
}

// answer is how a Salesforce response is treated. REST and Bulk share it.
type answer int

const (
	answerOK        answer = iota // 2xx
	answerRejected                // the record (or job) is refused: retried, then the DLQ
	answerTransient               // not about the record: stop, retry the rest later
)

// classify maps a status code. 401/403 are transient (after doRequest's one
// token refresh): a fixed grant lets the retry or a DLQ replay go through.
// 300 on an upsert means the external id matches several records, which is
// about the record; other 3xx (redirects are never followed) are about the
// instance URL.
func classify(status int) answer {
	switch {
	case status >= 200 && status < 300:
		return answerOK
	case status == http.StatusMultipleChoices:
		return answerRejected
	case status < 400,
		status == http.StatusUnauthorized,
		status == http.StatusForbidden,
		status == http.StatusRequestTimeout,
		status == http.StatusTooManyRequests,
		status >= 500:
		return answerTransient
	default:
		return answerRejected
	}
}

// sfError is one entry of Salesforce's error body:
// [{"message":"...","errorCode":"REQUIRED_FIELD_MISSING","fields":["Name"]}].
type sfError struct {
	Message   string `json:"message"`
	ErrorCode string `json:"errorCode"`
}

func parseSFErrors(body []byte) []sfError {
	var list []sfError
	if err := json.Unmarshal(body, &list); err == nil {
		var out []sfError
		for _, e := range list {
			if e.ErrorCode != "" || e.Message != "" {
				out = append(out, e)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	var one sfError
	if err := json.Unmarshal(body, &one); err == nil && (one.ErrorCode != "" || one.Message != "") {
		return []sfError{one}
	}
	return nil
}

// describe renders a response for an error: the status and Salesforce's error
// codes and messages, or a short piece of the body when it has none. It never
// includes request headers.
func describe(status int, body []byte) string {
	errs := parseSFErrors(body)
	if len(errs) == 0 {
		if s := truncateN(body, 200); s != "" {
			return fmt.Sprintf("%d: %s", status, s)
		}
		return fmt.Sprintf("%d", status)
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		code := e.ErrorCode
		if code == "" {
			code = "UNKNOWN"
		}
		parts = append(parts, code+": "+truncateN([]byte(e.Message), 200))
	}
	return fmt.Sprintf("%d %s", status, strings.Join(parts, "; "))
}

// maxListed bounds how many rejected records one error names, so the DLQ's
// last-error header stays short.
const maxListed = 5

// rejectedError names the records Salesforce refused.
func rejectedError(cfg *SalesforceProducerConfig, op string, total int, rejected []string) error {
	listed := rejected
	more := ""
	if len(listed) > maxListed {
		more = fmt.Sprintf("; and %d more", len(listed)-maxListed)
		listed = listed[:maxListed]
	}
	return fmt.Errorf("salesforce rejected %d of %d records (%s %s): %s%s",
		len(rejected), total, cfg.Object, op, strings.Join(listed, "; "), more)
}

// restWrite writes records one by one via the REST sObject API. Records the
// skip set has are not sent. A record Salesforce refuses is collected and the
// others still go in; the delivery then fails with every refused record named
// by index. A transient failure stops the loop: the records after it are sent
// on the retry.
func (p *salesforceProducer) restWrite(ctx context.Context, tenantID string, cfg *SalesforceProducerConfig, op string, records []map[string]any, key func(int) writtenKey, res *writeResult) error {
	base := strings.TrimRight(cfg.InstanceURL, "/")
	now := time.Now()
	var rejected []string
	var transient error
	ok, skipped := 0, 0
	reject := func(i int, why string) {
		p.logger.Error("Salesforce rejected record", "record", i, "object", cfg.Object, "op", op, "error", why)
		rejected = append(rejected, fmt.Sprintf("record[%d]: %s", i, why))
	}
	for i, rec := range records {
		k := key(i)
		if p.written.has(k, now) {
			res.done = append(res.done, k)
			skipped++
			continue
		}
		var method, u string
		if op == "upsert" {
			extVal, _ := rec[cfg.ExternalIDField].(string)
			if extVal == "" {
				if v, present := rec[cfg.ExternalIDField]; present && v != nil {
					extVal = fmt.Sprint(v)
				}
			}
			if extVal == "" {
				reject(i, fmt.Sprintf("no value for external id field %s", cfg.ExternalIDField))
				continue
			}
			method = http.MethodPatch
			u = fmt.Sprintf("%s/services/data/%s/sobjects/%s/%s/%s", base, cfg.APIVersion, cfg.Object, cfg.ExternalIDField, url.PathEscape(extVal))
		} else {
			method = http.MethodPost
			u = fmt.Sprintf("%s/services/data/%s/sobjects/%s", base, cfg.APIVersion, cfg.Object)
		}
		body, err := json.Marshal(rec)
		if err != nil {
			reject(i, fmt.Sprintf("cannot encode record: %v", err))
			continue
		}
		status, respBody, err := p.doRequest(ctx, tenantID, cfg.OAuthGrantID, method, u, "application/json", body)
		if err != nil {
			transient = fmt.Errorf("record[%d]: %w", i, err)
			break
		}
		switch classify(status) {
		case answerOK:
			res.wrote(k)
			ok++
		case answerTransient:
			transient = fmt.Errorf("record[%d]: salesforce %s", i, describe(status, respBody))
		default:
			reject(i, describe(status, respBody))
		}
		if transient != nil {
			break
		}
	}
	p.logger.Info("Salesforce REST write", "object", cfg.Object, "op", op,
		"ok", ok, "skipped_already_written", skipped, "rejected", len(rejected), "total", len(records))

	var errs []error
	if len(rejected) > 0 {
		errs = append(errs, rejectedError(cfg, op, len(records), rejected))
	}
	if transient != nil {
		errs = append(errs, fmt.Errorf("stopped before the remaining records: %w", transient))
	}
	return errors.Join(errs...)
}

// bulkStep checks one Bulk API answer with the REST path's classification.
// Every failure is retryable; the text says whether Salesforce refused the
// job or the call failed for another reason.
func bulkStep(step string, status int, body []byte) error {
	switch classify(status) {
	case answerOK:
		return nil
	case answerTransient:
		return fmt.Errorf("bulk %s: salesforce %s", step, describe(status, body))
	default:
		return fmt.Errorf("salesforce rejected the bulk job (%s): %s", step, describe(status, body))
	}
}

// doRequest performs an authenticated request, refreshing the token once on
// 401. The answer is read up to maxResponseBody; a body that cannot be read
// whole is returned as far as it was read (a 2xx is still a 2xx).
func (p *salesforceProducer) doRequest(ctx context.Context, tenantID, grantID, method, fullURL, contentType string, body []byte) (int, []byte, error) {
	status, respBody, _, err := p.request(ctx, tenantID, grantID, method, fullURL, contentType, "application/json", body, maxResponseBody)
	if err != nil && status != 0 {
		return status, respBody, nil
	}
	return status, respBody, err
}

// request is doRequest with the Accept header and the size cap chosen by the
// caller. truncated reports a body longer than limit; a body that could not
// be read whole comes back with the status and the read error.
func (p *salesforceProducer) request(ctx context.Context, tenantID, grantID, method, fullURL, contentType, accept string, body []byte, limit int64) (status int, respBody []byte, truncated bool, err error) {
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
		req.Header.Set("Accept", accept)
		return p.httpClient.Do(req)
	}
	resp, err := send(false)
	if err != nil {
		return 0, nil, false, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		p.logger.Info("Salesforce returned 401; refreshing token and retrying once", "grant_id", grantID)
		resp, err = send(true)
		if err != nil {
			return 0, nil, false, fmt.Errorf("request after token refresh: %w", err)
		}
	}
	defer resp.Body.Close()
	respBody, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(respBody)) > limit {
		respBody, truncated = respBody[:limit], true
	}
	if err != nil {
		return resp.StatusCode, respBody, truncated, fmt.Errorf("read answer: %w", err)
	}
	return resp.StatusCode, respBody, truncated, nil
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
	header, rows := csvTable(records)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	if err := w.WriteAll(rows); err != nil {
		return nil, err
	}
	return buf.Bytes(), w.Error()
}

// csvTable is the header (the sorted union of keys) and the rows
// recordsToCSV writes.
func csvTable(records []map[string]any) ([]string, [][]string) {
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

	rows := make([][]string, len(records))
	for j, r := range records {
		row := make([]string, len(keys))
		for i, k := range keys {
			row[i] = csvValue(r[k])
		}
		rows[j] = row
	}
	return keys, rows
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

func truncate(b []byte) string { return truncateN(b, 300) }

func truncateN(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "") + "…"
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
		return nil, sdk.ConfigReadError(err)
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
		targets = append(targets, &sfTarget{nodeID: n.ID, cfg: *nc.Salesforce, predecessorID: predID, predIsConsumer: predIsConsumer})
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
