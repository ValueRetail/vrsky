package managementapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Audit HTTP endpoints (Phase 1G / #72).
//
//	GET /api/v1/audit                       JSON list (paginated)
//	GET /api/v1/audit?format=jsonl          streaming JSONL export
//
// Both are tenant-scoped. Filters: action, resource_type, resource_id,
// user_id, since, until.

// auditListResponse mirrors ListResponse but for AuditEntry, kept distinct
// because ListResponse is typed against *Connection.
type auditListResponse struct {
	Data   []*AuditEntry `json:"data"`
	Total  int64         `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// ListAudit handles GET /api/v1/audit. When format=jsonl is set, the body
// is streamed as one JSON object per line for SIEM ingestion.
func (h *Handler) ListAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	q := r.URL.Query()
	filters := AuditFilters{
		Action:       q.Get("action"),
		ResourceType: q.Get("resource_type"),
		ResourceID:   q.Get("resource_id"),
		UserID:       q.Get("user_id"),
	}
	if s := q.Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			filters.Since = &t
		}
	}
	if s := q.Get("until"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			filters.Until = &t
		}
	}

	if q.Get("format") == "jsonl" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", "attachment; filename=\"vrsky-audit.jsonl\"")
		enc := json.NewEncoder(w)
		_ = h.repo.StreamAuditEntries(ctx, tenantID, filters, func(e *AuditEntry) error {
			return enc.Encode(e)
		})
		return
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = 50
	}

	entries, total, err := h.repo.ListAuditEntries(ctx, tenantID, filters, limit, offset)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to list audit entries", nil)
		return
	}
	if entries == nil {
		entries = []*AuditEntry{}
	}
	_ = writeJSON(w, http.StatusOK, auditListResponse{
		Data:   entries,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}
