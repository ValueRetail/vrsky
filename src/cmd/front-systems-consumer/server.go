package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// handleWebhook receives Front Systems event notifications (SaleCreated,
// StockMovementCreated, …). Front Systems POSTs to /frontsystems/events/{connID}
// and REQUIRES an HTTP 2xx — without it, it treats the event as undelivered and
// will retry. We publish the body and 202 as soon as it's on the stream.
func (c *frontSystemsConsumer) handleWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		connID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/frontsystems/events/"), "/")
		if connID == "" {
			http.Error(w, "missing connection id (/frontsystems/events/{connectionID})", http.StatusBadRequest)
			return
		}
		tenantID, err := c.resolveTenant(connID)
		if err != nil {
			c.logger.Warn("Front Systems webhook for unknown connection", "connection_id", connID, "error", err)
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
		env.Source = "front-systems-consumer"
		env.Payload = body
		env.PayloadSize = int64(len(body))
		env.StepHistory = []string{"front-systems-consumer"}
		env.Metadata = map[string]interface{}{"mode": "webhook", "event_type": eventType(body)}

		if err := c.publish(r.Context(), env); err != nil {
			// A non-2xx makes Front Systems retry — which is what we want when we
			// couldn't durably accept the event.
			c.logger.Error("publish Front Systems webhook failed", "connection_id", connID, "error", err)
			http.Error(w, "publish failed", http.StatusInternalServerError)
			return
		}
		c.logger.Info("Front Systems webhook received and published", "connection_id", connID, "event", eventType(body))
		w.WriteHeader(http.StatusAccepted)
	}
}

// eventType best-effort extracts the Front Systems event name from the body.
func eventType(body []byte) string {
	var e struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(body, &e); err == nil {
		return e.Event
	}
	return ""
}
