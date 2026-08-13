package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// Aux HTTP handler served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT).
// Registered in Configure. Lets the UI's filter/converter "show data structure"
// preview fetch a live sample from Visma.net before the pipeline is deployed.

const vismaSampleLimit = 5

// handleSampleData GETs the configured resource and returns up to
// vismaSampleLimit records, reusing the poller's auth + fetch path. Visma
// returns either a JSON array or a single object; both normalise to a list.
func (c *vismaConsumer) handleSampleData() http.HandlerFunc {
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

		var cfg VismaConfig
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			writeErr("invalid Visma config: " + err.Error())
			return
		}
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			writeErr("set the client ID and client secret first")
			return
		}
		if cfg.BaseURL == "" || cfg.Resource == "" {
			writeErr("set the base URL and resource first")
			return
		}

		tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.Scope).WithHTTPClient(c.httpClient)
		body, err := c.get(r.Context(), &cfg, tok)
		if err != nil {
			writeErr(err.Error())
			return
		}

		out := make([]interface{}, 0, vismaSampleLimit)
		if strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
			var arr []json.RawMessage
			if err := json.Unmarshal(body, &arr); err != nil {
				writeErr("parse Visma array: " + err.Error())
				return
			}
			for i, rec := range arr {
				if i >= vismaSampleLimit {
					break
				}
				var v interface{}
				if json.Unmarshal(rec, &v) == nil {
					out = append(out, v)
				}
			}
		} else {
			var v interface{}
			if err := json.Unmarshal(body, &v); err != nil {
				writeErr("parse Visma object: " + err.Error())
				return
			}
			out = append(out, v)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "data": out})
	}
}
