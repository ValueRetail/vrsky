package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

const brightpearlSampleLimit = 5

// handleSampleData GETs the configured Brightpearl resource and returns up to
// brightpearlSampleLimit records, reusing the poller's auth + fetch path. It
// unwraps the {"response": …} envelope and normalises arrays/objects to a list.
// Registered in Configure on the SDK aux HTTP port for the UI's pre-deploy
// "show data structure" preview.
func (c *brightpearlConsumer) handleSampleData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeErr := func(msg string) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": msg})
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr("read request body: " + err.Error())
			return
		}
		var meta struct {
			TenantID string `json:"tenant_id"`
		}
		_ = json.Unmarshal(raw, &meta)
		cfgJSON := json.RawMessage(raw)
		if meta.TenantID != "" && c.db != nil {
			if resolved, rerr := crypto.ResolveSecretsInJSON(r.Context(), crypto.NewSQLSecretReader(c.db), meta.TenantID, cfgJSON); rerr == nil {
				cfgJSON = resolved
			}
		}

		var cfg BrightpearlConfig
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			writeErr("invalid Brightpearl config: " + err.Error())
			return
		}
		if cfg.AppRef == "" || cfg.StaffToken == "" {
			writeErr("set the app reference and staff token first")
			return
		}
		if cfg.baseURL() == "" {
			writeErr("set the datacenter and account code (or base_url) first")
			return
		}
		if cfg.Resource == "" {
			writeErr("set the resource first")
			return
		}

		body, err := c.get(r.Context(), &cfg)
		if err != nil {
			writeErr(err.Error())
			return
		}
		// Unwrap the {"response": …} envelope when present.
		var wrapper struct {
			Response json.RawMessage `json:"response"`
		}
		payload := body
		if json.Unmarshal(body, &wrapper) == nil && len(wrapper.Response) > 0 {
			payload = wrapper.Response
		}

		out := make([]interface{}, 0, brightpearlSampleLimit)
		if strings.HasPrefix(strings.TrimSpace(string(payload)), "[") {
			var arr []json.RawMessage
			if err := json.Unmarshal(payload, &arr); err != nil {
				writeErr("parse Brightpearl array: " + err.Error())
				return
			}
			for i, rec := range arr {
				if i >= brightpearlSampleLimit {
					break
				}
				var v interface{}
				if json.Unmarshal(rec, &v) == nil {
					out = append(out, v)
				}
			}
		} else {
			var v interface{}
			if err := json.Unmarshal(payload, &v); err != nil {
				writeErr("parse Brightpearl response: " + err.Error())
				return
			}
			out = append(out, v)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "data": out})
	}
}

// handleWebhook receives Brightpearl webhook notifications. Brightpearl POSTs to
// /brightpearl/events/{connectionID}; the body is published into the pipeline.
// Served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT).
func (c *brightpearlConsumer) handleWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		connID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/brightpearl/events/"), "/")
		if connID == "" {
			http.Error(w, "missing connection id (/brightpearl/events/{connectionID})", http.StatusBadRequest)
			return
		}
		tenantID, err := c.resolveTenant(connID)
		if err != nil {
			c.logger.Warn("Brightpearl webhook for unknown connection", "connection_id", connID, "error", err)
			http.Error(w, "unknown connection", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if c.publish == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}

		env := envelope.New()
		env.TenantID = tenantID
		env.IntegrationID = connID
		env.ContentType = "application/json"
		env.Source = "brightpearl-consumer"
		env.Payload = body
		env.PayloadSize = int64(len(body))
		env.StepHistory = []string{"brightpearl-consumer"}
		env.Metadata = map[string]interface{}{"mode": "webhook"}

		if err := c.publish(r.Context(), env); err != nil {
			c.logger.Error("publish Brightpearl webhook failed", "connection_id", connID, "error", err)
			http.Error(w, "publish failed", http.StatusInternalServerError)
			return
		}
		c.logger.Info("Brightpearl webhook received and published", "connection_id", connID, "bytes", len(body))
		w.WriteHeader(http.StatusAccepted)
	}
}
