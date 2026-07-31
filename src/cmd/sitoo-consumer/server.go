package main

import (
	"io"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

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
