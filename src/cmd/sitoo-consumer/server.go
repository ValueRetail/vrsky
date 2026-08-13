package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

const sitooSampleLimit = 5

// handleSampleData GETs the first page of the configured Sitoo resource and
// returns up to sitooSampleLimit items, reusing the poller's Basic-auth fetch.
// Registered in Configure on the SDK aux HTTP port for the UI's pre-deploy
// "show data structure" preview.
func (s *sitooConsumer) handleSampleData() http.HandlerFunc {
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
		if meta.TenantID != "" && s.db != nil {
			if resolved, rerr := crypto.ResolveSecretsInJSON(r.Context(), crypto.NewSQLSecretReader(s.db), meta.TenantID, cfgJSON); rerr == nil {
				cfgJSON = resolved
			}
		}

		var cfg SitooConfig
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			writeErr("invalid Sitoo config: " + err.Error())
			return
		}
		if cfg.APIID == "" || cfg.APIPassword == "" {
			writeErr("set the API ID and API password first")
			return
		}
		if cfg.AccountID == 0 || cfg.SiteID == 0 {
			writeErr("set the account ID and site ID first")
			return
		}

		reqURL := fmt.Sprintf("%s/accounts/%d/sites/%d/%s?start=0&num=%d",
			cfg.effectiveBaseURL(), cfg.AccountID, cfg.SiteID, cfg.effectiveResource(), sitooSampleLimit)
		body, err := s.get(r.Context(), &cfg, reqURL)
		if err != nil {
			writeErr(err.Error())
			return
		}
		var coll sitooCollection
		if err := json.Unmarshal(body, &coll); err != nil {
			writeErr("parse Sitoo collection: " + err.Error())
			return
		}
		items := coll.Items
		if len(items) > sitooSampleLimit {
			items = items[:sitooSampleLimit]
		}
		out := make([]interface{}, 0, len(items))
		for _, rec := range items {
			var v interface{}
			if json.Unmarshal(rec, &v) == nil {
				out = append(out, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "data": out})
	}
}

// handleWebhook serves Sitoo SPI Event notifications (real-time mode). Sitoo is
// configured to POST events to /sitoo/events/{connectionID}; each request body
// is wrapped in an envelope and published into the pipeline. Served on the SDK
// auxiliary HTTP port (WORKER_HTTP_PORT).
func (s *sitooConsumer) handleWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		connID := strings.TrimPrefix(r.URL.Path, "/sitoo/events/")
		connID = strings.Trim(connID, "/")
		if connID == "" {
			http.Error(w, "missing connection id in path (/sitoo/events/{connectionID})", http.StatusBadRequest)
			return
		}

		// Resolve the owning tenant so the envelope is routable. An unknown
		// connection id is a 404 rather than a silent drop.
		tenantID, err := s.resolveTenant(connID)
		if err != nil {
			s.logger.Warn("Sitoo webhook for unknown connection", "connection_id", connID, "error", err)
			http.Error(w, "unknown connection", http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20)) // 8 MiB cap
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		if s.publish == nil { // Run hasn't wired the publisher yet
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}

		env := envelope.New()
		env.TenantID = tenantID
		env.IntegrationID = connID
		env.ContentType = firstNonEmpty(r.Header.Get("Content-Type"), "application/json")
		env.Source = "sitoo-consumer"
		env.Payload = body
		env.PayloadSize = int64(len(body))
		env.StepHistory = []string{"sitoo-consumer"}
		env.Metadata = map[string]interface{}{"mode": "webhook", "event_type": r.Header.Get("X-Sitoo-Event")}

		if err := s.publish(r.Context(), env); err != nil {
			s.logger.Error("publish Sitoo webhook failed", "connection_id", connID, "error", err)
			http.Error(w, "publish failed", http.StatusInternalServerError)
			return
		}
		s.logger.Info("Sitoo webhook received and published", "connection_id", connID, "bytes", len(body))
		w.WriteHeader(http.StatusAccepted)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
