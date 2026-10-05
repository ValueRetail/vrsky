package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// maxToolResult bounds what one tool call puts into the model's context.
const maxToolResult = 12 << 10

// apiClient calls the management API as a workspace, with that workspace's
// own API key. Tenant isolation is therefore the API's: a key only ever
// reaches its own workspace, whatever id the model passes.
type apiClient struct {
	baseURL string
	http    *http.Client
	keys    map[string]string // tenant id → API key
}

func (c *apiClient) hasTenant(tenantID string) bool { _, ok := c.keys[tenantID]; return ok }

// do performs one request and returns the body of a 2xx; anything else is an
// error carrying the status and the API's message.
func (c *apiClient) do(ctx context.Context, tenantID, method, path string) ([]byte, error) {
	key, ok := c.keys[tenantID]
	if !ok {
		return nil, fmt.Errorf("no API key configured for workspace %s", tenantID)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Tenant-ID", tenantID)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, clip(string(body), 500))
	}
	return body, nil
}

var secretKey = regexp.MustCompile(`(?i)(secret|password|passwd|token|api[_-]?key|credential|private[_-]?key|authorization)`)

// redact walks decoded JSON and blanks the values of keys that look like
// credentials. The API stores secrets by reference already; this is the
// second lock, because whatever a tool returns is sent to the model.
// *_secret_id references are kept: they are ids, and useful when diagnosing.
func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if secretKey.MatchString(k) && !strings.HasSuffix(strings.ToLower(k), "_secret_id") {
				if s, ok := val.(string); ok && s != "" {
					t[k] = "[redacted]"
					continue
				}
			}
			t[k] = redact(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = redact(t[i])
		}
		return t
	case string:
		// A dead-lettered message carries its payload; a sample is enough
		// to diagnose with, and customer data does not belong in a prompt.
		return clip(t, maxStringValue)
	}
	return v
}

// maxStringValue bounds any single string in a tool result (payload samples).
const maxStringValue = 2 << 10

// present turns an API response into a tool result: credentials blanked,
// compact JSON, clipped to maxToolResult.
func present(body []byte) string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return clip(string(body), maxToolResult)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(redact(v))
	return clip(strings.TrimSpace(buf.String()), maxToolResult)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("… [truncated, %d bytes in total]", len(s))
}

type connInput struct {
	ConnectionID string `json:"connection_id"`
	Limit        int    `json:"limit"`
	Seq          uint64 `json:"seq"`
	Reason       string `json:"reason"`
}

const (
	schemaNone   = `{"type":"object","properties":{},"additionalProperties":false}`
	schemaConn   = `{"type":"object","properties":{"connection_id":{"type":"string","description":"The pipeline (connection) id"}},"required":["connection_id"],"additionalProperties":false}`
	schemaEvents = `{"type":"object","properties":{"connection_id":{"type":"string"},"limit":{"type":"integer","description":"How many of the newest events (default 30, max 100)"}},"required":["connection_id"],"additionalProperties":false}`
	schemaSeq    = `{"type":"object","properties":{"connection_id":{"type":"string"},"seq":{"type":"integer","description":"The dead-letter message's sequence number"}},"required":["connection_id","seq"],"additionalProperties":false}`
	schemaAct    = `{"type":"object","properties":{"connection_id":{"type":"string"},"reason":{"type":"string","description":"One sentence: the evidence that makes this action the right one"}},"required":["connection_id","reason"],"additionalProperties":false}`
	schemaActSeq = `{"type":"object","properties":{"connection_id":{"type":"string"},"seq":{"type":"integer"},"reason":{"type":"string"}},"required":["connection_id","seq","reason"],"additionalProperties":false}`
)

// run is the per-run state the tools share: whose workspace, the call budget,
// and what was done.
type run struct {
	tenantID  string
	api       *apiClient
	guard     *guard
	maxCalls  int
	calls     int
	actions   []string // human-readable, for the log and the resolved card
	logAction func(action, connID, reason string)
}

// counted enforces the per-run tool-call cap in front of every tool.
func (r *run) counted(call func(ctx context.Context, in connInput) (string, error)) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		r.calls++
		if r.calls > r.maxCalls {
			return "", fmt.Errorf("refused: this run's tool-call budget (%d) is used up; write the report with what you have", r.maxCalls)
		}
		var in connInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("invalid input: %w", err)
			}
		}
		return call(ctx, in)
	}
}

func connPath(id, suffix string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("connection_id is required")
	}
	return "/api/v1/connections/" + url.PathEscape(id) + suffix, nil
}

// tools builds the tool set for one run. Read tools are always there; act
// tools exist only when act is true — in observe mode the model cannot call
// what it is not given — and each is wrapped by the guard.
func (r *run) tools(act bool) []Tool {
	get := func(suffix string) func(context.Context, connInput) (string, error) {
		return func(ctx context.Context, in connInput) (string, error) {
			p, err := connPath(in.ConnectionID, suffix)
			if err != nil {
				return "", err
			}
			body, err := r.api.do(ctx, r.tenantID, http.MethodGet, p)
			if err != nil {
				return "", err
			}
			return present(body), nil
		}
	}
	ts := []Tool{
		{Name: "list_pipelines", Schema: schemaNone,
			Description: "List this workspace's pipelines: id, name, status (running, stopped, error), last_error. Start here when the alert does not name a pipeline.",
			Call: r.counted(func(ctx context.Context, _ connInput) (string, error) {
				body, err := r.api.do(ctx, r.tenantID, http.MethodGet, "/api/v1/connections")
				if err != nil {
					return "", err
				}
				return present(summarisePipelines(body)), nil
			})},
		{Name: "get_pipeline", Schema: schemaConn,
			Description: "One pipeline in full: status, last_error, its nodes (source, transforms, destinations) with their configuration. Credentials are redacted.",
			Call:        r.counted(get(""))},
		{Name: "list_pipeline_events", Schema: schemaEvents,
			Description: "A pipeline's newest lifecycle events (started, stopped, error with its message, config_changed), newest first.",
			Call: r.counted(func(ctx context.Context, in connInput) (string, error) {
				limit := in.Limit
				if limit <= 0 {
					limit = 30
				}
				return get(fmt.Sprintf("/events?limit=%d", min(limit, 100)))(ctx, in)
			})},
		{Name: "get_pipeline_metrics", Schema: schemaConn,
			Description: "Message counters for a pipeline (published, processed, failed, dead-lettered).",
			Call:        r.counted(get("/metrics"))},
		{Name: "list_dlq", Schema: schemaConn,
			Description: "Dead-lettered messages of a pipeline: sequence number, failure reason, when. Use the reasons to tell a transient failure from a data or mapping problem.",
			Call:        r.counted(get("/dlq"))},
		{Name: "get_dlq_message", Schema: schemaSeq,
			Description: "One dead-lettered message with its failure reason and a clipped payload sample.",
			Call: r.counted(func(ctx context.Context, in connInput) (string, error) {
				return get(fmt.Sprintf("/dlq/%d", in.Seq))(ctx, in)
			})},
		{Name: "list_agents", Schema: schemaNone,
			Description: "This workspace's remote agents (tills): name, online or offline, last seen, groups, folders.",
			Call: r.counted(func(ctx context.Context, _ connInput) (string, error) {
				body, err := r.api.do(ctx, r.tenantID, http.MethodGet, "/api/v1/agents")
				if err != nil {
					return "", err
				}
				return present(body), nil
			})},
	}
	if !act {
		return ts
	}
	post := func(action, suffix string, limit int) func(context.Context, connInput) (string, error) {
		return func(ctx context.Context, in connInput) (string, error) {
			p, err := connPath(in.ConnectionID, suffix)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(in.Reason) == "" {
				return "", fmt.Errorf("refused: a reason is required for every action")
			}
			if err := r.guard.admitAction(r.tenantID, in.ConnectionID, action, limit); err != nil {
				return "", err
			}
			if _, err := r.api.do(ctx, r.tenantID, http.MethodPost, p); err != nil {
				return "", err
			}
			return "ok", nil
		}
	}
	record := func(action string, call func(context.Context, connInput) (string, error)) func(context.Context, connInput) (string, error) {
		return func(ctx context.Context, in connInput) (string, error) {
			out, err := call(ctx, in)
			if err == nil {
				r.actions = append(r.actions, action+" on "+in.ConnectionID)
				if r.logAction != nil {
					r.logAction(action, in.ConnectionID, in.Reason)
				}
			}
			return out, err
		}
	}
	return append(ts,
		Tool{Name: "redeploy_pipeline", Schema: schemaAct, Act: true,
			Description: "Stop and start a pipeline. Only for a transient failure (upstream timeouts or 5xx, a poller that died). Never for a configuration or credential error: a redeploy cannot fix those. At most once per pipeline per hour.",
			Call: r.counted(record("redeploy_pipeline", func(ctx context.Context, in connInput) (string, error) {
				p, err := connPath(in.ConnectionID, "")
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(in.Reason) == "" {
					return "", fmt.Errorf("refused: a reason is required for every action")
				}
				if err := r.guard.admitAction(r.tenantID, in.ConnectionID, "redeploy_pipeline", 1); err != nil {
					return "", err
				}
				// A pipeline in error may already be stopped; only the start has to succeed.
				_, _ = r.api.do(ctx, r.tenantID, http.MethodPost, p+"/stop")
				if _, err := r.api.do(ctx, r.tenantID, http.MethodPost, p+"/start"); err != nil {
					return "", err
				}
				return "redeployed; check its status and events again before reporting it as recovered", nil
			}))},
		Tool{Name: "retry_dlq_message", Schema: schemaActSeq, Act: true,
			Description: "Send one dead-lettered message through its pipeline again. Only when the failure reason is transient and its cause is gone. Never for a data, schema or mapping error: it will fail the same way. At most 20 per pipeline per hour.",
			Call: r.counted(record("retry_dlq_message", func(ctx context.Context, in connInput) (string, error) {
				return post("retry_dlq_message", fmt.Sprintf("/dlq/%d/retry", in.Seq), maxDLQRetriesPerHour)(ctx, in)
			}))},
		Tool{Name: "resend_everything", Schema: schemaAct, Act: true,
			Description: "Ask a running pipeline's source to send all its data again on the next poll (Business Central: every record and picture). For a destination that lost data, e.g. a till that was offline longer than VRSky holds messages. At most once per pipeline per hour.",
			Call:        r.counted(record("resend_everything", post("resend_everything", "/resend", 1)))},
	)
}

// summarisePipelines keeps what a diagnosis needs from the pipeline list —
// the full node configurations are fetched per pipeline with get_pipeline.
func summarisePipelines(body []byte) []byte {
	var list struct {
		Connections []map[string]any `json:"connections"`
		Data        []map[string]any `json:"data"`
		Items       []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return body
	}
	all := append(append(list.Connections, list.Data...), list.Items...)
	if all == nil {
		return body
	}
	out := make([]map[string]any, 0, len(all))
	for _, c := range all {
		out = append(out, map[string]any{
			"id": c["id"], "name": c["name"], "status": c["status"], "last_error": c["last_error"],
			"started_at": c["started_at"], "stopped_at": c["stopped_at"],
		})
	}
	b, _ := json.Marshal(map[string]any{"pipelines": out})
	return b
}
