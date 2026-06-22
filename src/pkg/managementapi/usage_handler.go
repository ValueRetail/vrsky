package managementapi

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"
)

// Per-tenant usage metering API (Phase 4A / #92).
//
//	GET /api/v1/tenants/{tenant_id}/usage[?from=YYYY-MM-DD&to=YYYY-MM-DD]
//	GET /api/v1/tenants/{tenant_id}/usage/export?format=csv[&from=&to=]
//
// Both default to the current calendar month (UTC). Membership is enforced by
// the tenant middleware on the route; we read the tenant from the path.

// UsageResponse is the GET /usage payload: a range, the range totals, and the
// per-day rows that back a chart/table.
type UsageResponse struct {
	From  string        `json:"from"`
	To    string        `json:"to"`
	Month *UsageTotals  `json:"month"`
	Daily []*UsageDaily `json:"daily"`
}

// usageRange resolves the from/to query params, defaulting to the current
// calendar month (UTC): from = first of month, to = today. Invalid dates fall
// back to the default bound. to is clamped to be >= from.
func usageRange(r *http.Request) (from, to time.Time) {
	now := time.Now().UTC()
	from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	if v := r.URL.Query().Get("from"); v != "" {
		if d, err := time.Parse("2006-01-02", v); err == nil {
			from = d
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if d, err := time.Parse("2006-01-02", v); err == nil {
			to = d
		}
	}
	if to.Before(from) {
		to = from
	}
	return from, to
}

// HandleGetUsage returns range totals + daily rows for the tenant.
func (h *Handler) HandleGetUsage(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	from, to := usageRange(r)
	ctx := r.Context()

	totals, err := h.repo.SumUsage(ctx, tenantID, from, to)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to aggregate usage", nil)
		return
	}
	daily, err := h.repo.ListUsageDaily(ctx, tenantID, from, to)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to load usage", nil)
		return
	}
	if daily == nil {
		daily = []*UsageDaily{}
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: UsageResponse{
		From:  from.Format("2006-01-02"),
		To:    to.Format("2006-01-02"),
		Month: totals,
		Daily: daily,
	}})
}

// HandleExportUsage streams the daily rows as CSV (Stripe/invoice-ready).
func (h *Handler) HandleExportUsage(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	from, to := usageRange(r)

	daily, err := h.repo.ListUsageDaily(r.Context(), tenantID, from, to)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to load usage", nil)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition",
		"attachment; filename=\"vrsky-usage-"+from.Format("2006-01-02")+"_"+to.Format("2006-01-02")+".csv\"")

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"day", "messages_published", "deploys", "storage_bytes"})
	for _, d := range daily {
		_ = cw.Write([]string{
			d.Day,
			strconv.FormatInt(d.MessagesPublished, 10),
			strconv.FormatInt(d.Deploys, 10),
			strconv.FormatInt(d.StorageBytes, 10),
		})
	}
	cw.Flush()
}
