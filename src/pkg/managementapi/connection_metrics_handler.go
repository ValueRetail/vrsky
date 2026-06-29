package managementapi

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HandleConnectionMetrics returns point-in-time metrics for a single connection,
// sourced from Prometheus. Per-connection throughput comes from
// vrsky_messages_published_total (labeled by connection_id) and errors from
// vrsky_dlq_messages_total (labeled by pipeline_id == connection id).
//
// This replaces the old in-process metrics cache that was fed by a NATS
// subscriber on vrsky.metrics.* — a subject nothing ever published to, so the
// dashboard always showed "No pipeline data available".
//
// The response is returned unwrapped (no {data:...} envelope) to match the UI's
// metricsService.getConnectionMetrics, which reads the body directly.
func (h *Handler) HandleConnectionMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	connID := r.PathValue("id")
	if connID == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to retrieve connection", nil)
		}
		return
	}
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Prometheus is the source of truth. When it isn't wired (h.prom == nil)
	// every query returns 0, so the dashboard shows zeros rather than failing.
	published := h.promScalar(ctx,
		fmt.Sprintf(`sum by (connection_id) (vrsky_messages_published_total{connection_id=%q})`, connID),
		"connection_id", connID)
	throughput := h.promScalar(ctx,
		fmt.Sprintf(`sum by (connection_id) (rate(vrsky_messages_published_total{connection_id=%q}[1m]))`, connID),
		"connection_id", connID)
	dlq := h.promScalar(ctx,
		fmt.Sprintf(`sum by (pipeline_id) (vrsky_dlq_messages_total{pipeline_id=%q})`, connID),
		"pipeline_id", connID)
	dlqRate := h.promScalar(ctx,
		fmt.Sprintf(`sum by (pipeline_id) (rate(vrsky_dlq_messages_total{pipeline_id=%q}[1m]))`, connID),
		"pipeline_id", connID)

	compStatus := "idle"
	if conn.Status == "running" {
		compStatus = "active"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	msgs := int64(published)
	errs := int64(dlq)

	component := func(processed, sent, e int64) map[string]interface{} {
		return map[string]interface{}{
			"status":             compStatus,
			"messages_processed": processed,
			"messages_sent":      sent,
			"errors":             e,
			"last_update":        now,
		}
	}

	resp := map[string]interface{}{
		"connection_id": connID,
		"tenant_id":     tenantID,
		"status":        conn.Status,
		"components": map[string]interface{}{
			"consumer":  component(msgs, 0, 0),
			"converter": component(0, 0, 0),
			"filter":    map[string]interface{}{"status": compStatus, "messages_processed": int64(0), "filtered_out": int64(0), "errors": int64(0), "last_update": now},
			"producer":  component(msgs, msgs, errs),
		},
		"total_messages_in":  msgs,
		"total_messages_out": msgs,
		"total_errors":       errs,
		"errors_per_second":  dlqRate,
		"throughput_mps":     throughput,
		"last_updated":       now,
	}

	_ = writeJSON(w, http.StatusOK, resp)
}

// promScalar runs a Prometheus query expected to return a single series keyed by
// label=value and returns that value. Returns 0 when Prometheus is unconfigured,
// the query errors, or the series is absent.
func (h *Handler) promScalar(ctx context.Context, query, label, value string) float64 {
	if h.prom == nil {
		return 0
	}
	m, err := h.prom.QueryByLabel(ctx, query, label)
	if err != nil {
		return 0
	}
	return m[value]
}
