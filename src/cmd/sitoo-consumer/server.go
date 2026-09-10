package main

import (
	"context"
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

		// An SPI event is a NOTIFICATION, not a resource: it carries
		// {eventid, eventtype, <resource>id} and nothing else. Forwarding it
		// verbatim is what made a webhook-fed pipeline send a downstream Sitoo
		// destination an event object where the poll path sends an array of
		// transactions — two materially different bodies to the same collection
		// endpoint, only one of which is a collection write.
		//
		// So dereference it: fetch the resource the event names and publish it
		// in the poll path's shape. Poll and webhook then produce the same
		// envelope, which is what everything downstream already assumes.
		payload, meta, status, err := s.dereferenceEvent(r.Context(), connID, tenantID, body, r.Header.Get("X-Sitoo-Event"))
		if err != nil {
			// 5xx asks Sitoo to redeliver; 202 acks an event there is nothing
			// to fetch for, so it is not retried forever.
			s.logger.Warn("Sitoo webhook not dereferenced",
				"connection_id", connID, "status", status, "error", err)
			http.Error(w, err.Error(), status)
			return
		}

		env := envelope.New()
		env.TenantID = tenantID
		env.IntegrationID = connID
		env.ContentType = firstNonEmpty(r.Header.Get("Content-Type"), "application/json")
		env.Source = "sitoo-consumer"
		env.Payload = payload
		env.PayloadSize = int64(len(payload))
		env.StepHistory = []string{"sitoo-consumer"}
		env.Metadata = meta

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

// dereferenceEvent turns an SPI event notification into the same payload shape
// the poller publishes: a JSON array of resource objects.
//
// Returns (payload, metadata, httpStatus, error). On error the status is what
// to answer Sitoo with — 5xx to have the event redelivered, 202 to ack an event
// there is nothing useful to fetch for.
//
// Set sitoo.webhook_raw_event to forward the event body untouched instead. That
// is the pre-#243 behaviour and it is only correct when the destination expects
// events rather than resources — a filter or an HTTP producer pointed at
// something event-shaped, not a Sitoo collection endpoint.
func (s *sitooConsumer) dereferenceEvent(
	ctx context.Context, connID, tenantID string, body []byte, eventHeader string,
) ([]byte, map[string]interface{}, int, error) {
	rawMeta := func(evType string) map[string]interface{} {
		return map[string]interface{}{"mode": "webhook", "event_type": evType, "dereferenced": false}
	}

	var ev map[string]json.RawMessage
	if err := json.Unmarshal(body, &ev); err != nil {
		// Not an object: nothing to dereference, and nothing gained by asking
		// Sitoo to send it again.
		return body, rawMeta(eventHeader), http.StatusAccepted, nil
	}
	eventType := eventHeader
	if eventType == "" {
		if raw, ok := ev["eventtype"]; ok {
			_ = json.Unmarshal(raw, &eventType)
		}
	}

	load := s.loadConfig
	if load == nil {
		load = s.getSitooConfig
	}
	cfg, err := load(ctx, connID, tenantID)
	if err != nil {
		// The connection exists (the tenant resolved) but its config does not
		// load — transient far more often than not, so let Sitoo redeliver.
		return nil, nil, http.StatusServiceUnavailable, fmt.Errorf("load config: %w", err)
	}
	if cfg.WebhookRawEvent {
		return body, rawMeta(eventType), 0, nil
	}

	idField := cfg.eventIDField()
	raw, ok := ev[idField]
	if !ok {
		// An event kind this connection's resource has no id for — e.g. a
		// heartbeat. Ack it; retrying will not make the field appear.
		return nil, nil, http.StatusAccepted, fmt.Errorf("event has no %q field to dereference", idField)
	}
	id := strings.Trim(string(raw), `"`)
	if id == "" {
		return nil, nil, http.StatusAccepted, fmt.Errorf("event %q field is empty", idField)
	}

	resourceBody, err := s.get(ctx, cfg, cfg.resourceURL(id))
	if err != nil {
		// The fetch failed — rate limit, network, a resource not yet readable.
		// All worth a redelivery.
		return nil, nil, http.StatusBadGateway, fmt.Errorf("fetch %s %s: %w", cfg.effectiveResource(), id, err)
	}
	if !json.Valid(resourceBody) {
		return nil, nil, http.StatusBadGateway, fmt.Errorf("fetch %s %s: response is not JSON", cfg.effectiveResource(), id)
	}

	// One element, so the shape matches a poll page of one.
	payload, err := json.Marshal([]json.RawMessage{json.RawMessage(resourceBody)})
	if err != nil {
		return nil, nil, http.StatusInternalServerError, fmt.Errorf("wrap resource: %w", err)
	}
	return payload, map[string]interface{}{
		"mode":         "webhook",
		"event_type":   eventType,
		"resource":     cfg.effectiveResource(),
		"record_count": 1,
		"dereferenced": true,
	}, 0, nil
}
