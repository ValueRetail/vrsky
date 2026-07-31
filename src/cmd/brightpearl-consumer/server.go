package main

import (
	"io"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

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
