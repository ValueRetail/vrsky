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
// and counts only an HTTP 2xx as delivered. It does not resend on its own: a
// failed event stays in its event log (GET /api/WebhooksEvents/{id}?success=false)
// until someone resends it. We publish the body and 202 as soon as it's on the
// stream.
//
// The route has no authentication of its own (Front Systems documents no
// signature); the connection id in the URL is the secret, and the tenant comes
// from that connection's row. An event is published only while this process
// has the connection started, for that same tenant (#503). Otherwise nothing
// is published and the answer is 410 Gone (not running, or the row's tenant
// is not the one it was started for), 503 while boot restore is still running
// or the process is shutting down, or 404 for an unknown connection.
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
		switch c.refuseEvent(connID, tenantID) {
		case 0:
		case http.StatusServiceUnavailable:
			c.logger.Info("Front Systems event refused: connection not started yet", "connection_id", connID)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		default:
			c.logger.Warn("Front Systems event refused: connection not running", "connection_id", connID)
			http.Error(w, "connection not running", http.StatusGone)
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
			// A non-2xx marks the event failed at Front Systems, where it can be
			// resent: what we want when we couldn't durably accept it.
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
