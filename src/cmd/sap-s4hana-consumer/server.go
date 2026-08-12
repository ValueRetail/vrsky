package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Aux HTTP handler served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT).
// Registered in Configure via RegisterHTTPHandler. It lets the UI's filter /
// converter "show data structure" preview fetch a live sample from SAP S/4HANA
// *before* the pipeline is deployed — no data has to flow through first. /health
// is served separately by the SDK on HEALTH_PORT.

const sapSampleLimit = 5

// handleSampleData GETs the first OData page for the posted SAP config and
// returns up to sapSampleLimit records as a JSON array, using the same fetch +
// parse path as the live poller.
func (c *sapConsumer) handleSampleData() http.HandlerFunc {
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
		// tenant_id (optional) lets us resolve <field>_secret_id references for a
		// saved connection; a freshly-typed config carries plaintext and resolves
		// to itself. Best-effort: plaintext still works when resolution is skipped.
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

		var cfg SAPConfig
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			writeErr("invalid SAP config: " + err.Error())
			return
		}
		if cfg.APIBaseURL == "" && cfg.Host == "" {
			writeErr("set the SAP host or API base URL first")
			return
		}
		if cfg.EntitySet == "" {
			writeErr("set the entity set first")
			return
		}

		auth := newAuthorizer(&cfg, c.httpClient)
		body, reqURL, err := c.get(r.Context(), &cfg, auth, cfg.entityURL())
		if err != nil {
			writeErr(err.Error())
			return
		}
		records, _, err := parsePage(cfg.effectiveVersion(), body, reqURL)
		if err != nil {
			writeErr(err.Error())
			return
		}
		if len(records) > sapSampleLimit {
			records = records[:sapSampleLimit]
		}
		out := make([]interface{}, 0, len(records))
		for _, rec := range records {
			var v interface{}
			if err := json.Unmarshal(rec, &v); err == nil {
				out = append(out, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "data": out})
	}
}
