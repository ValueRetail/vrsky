package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// Aux HTTP handler served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT).
// Registered in Configure. Lets the UI's filter/converter "show data structure"
// preview fetch a live sample from Business Central before the pipeline is
// deployed — no data has to flow through first.

const bcSampleLimit = 5

// handleSampleData GETs the first OData page for the posted BC config and
// returns up to bcSampleLimit records, reusing the poller's auth + fetch path.
func (c *bcConsumer) handleSampleData() http.HandlerFunc {
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
		// tenant_id (optional) resolves <field>_secret_id references for a saved
		// connection; a freshly-typed config carries plaintext and resolves to
		// itself. Best-effort: plaintext still works when resolution is skipped.
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

		var cfg BCConfig
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			writeErr("invalid Business Central config: " + err.Error())
			return
		}
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			writeErr("set the client ID and client secret first")
			return
		}
		if cfg.APIBaseURL == "" && (cfg.AADTenantID == "" || cfg.CompanyID == "") {
			writeErr("set the AAD tenant ID and company ID (or api_base_url) first")
			return
		}

		tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(c.httpClient)
		body, err := c.get(r.Context(), tok, cfg.entityURL())
		if err != nil {
			writeErr(err.Error())
			return
		}
		var p odataPage
		if err := json.Unmarshal(body, &p); err != nil {
			writeErr("parse OData page: " + err.Error())
			return
		}
		records := p.Value
		if len(records) > bcSampleLimit {
			records = records[:bcSampleLimit]
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
