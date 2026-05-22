package managementapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/nats-io/nats.go"
)

// DLQ HTTP handlers (Phase 1E / #70).
//
// Routes:
//
//	GET    /api/v1/connections/{id}/dlq               list (paginated)
//	GET    /api/v1/connections/{id}/dlq/{seq}         single message + payload
//	POST   /api/v1/connections/{id}/dlq/{seq}/retry   re-publish
//	POST   /api/v1/connections/{id}/dlq/{seq}/discard remove

// SetJetStream wires a JetStream context onto the handler. Called by the
// management-api main once the NATS connection is up. Optional — DLQ
// endpoints simply return 503 when JS is unconfigured.
func (h *Handler) SetJetStream(js nats.JetStreamContext) {
	h.js = js
}

func (h *Handler) requireJS(w http.ResponseWriter) (nats.JetStreamContext, bool) {
	if h.js == nil {
		_ = writeError(w, http.StatusServiceUnavailable, "JetStreamNotConfigured",
			"DLQ endpoints require JetStream", nil)
		return nil, false
	}
	return h.js, true
}

func (h *Handler) verifyConnectionOwnership(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	tenantID, err := GetTenantIDFromContext(r.Context())
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/connections/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidPath", "connection id required", nil)
		return "", "", false
	}
	connID := parts[0]
	conn, err := h.repo.GetConnection(r.Context(), connID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		return "", "", false
	}
	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "access denied", nil)
		return "", "", false
	}
	return tenantID, connID, true
}

// ListDLQMessages handles GET /api/v1/connections/{id}/dlq.
func (h *Handler) ListDLQMessages(w http.ResponseWriter, r *http.Request) {
	js, ok := h.requireJS(w)
	if !ok {
		return
	}
	tenantID, connID, ok := h.verifyConnectionOwnership(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	entries, err := messaging.ListDLQ(js, tenantID, connID, limit, offset)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "JetStreamError", err.Error(), nil)
		return
	}
	if entries == nil {
		entries = []*messaging.DLQEntry{}
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: entries})
}

// GetDLQMessage handles GET /api/v1/connections/{id}/dlq/{seq}.
func (h *Handler) GetDLQMessage(w http.ResponseWriter, r *http.Request) {
	js, ok := h.requireJS(w)
	if !ok {
		return
	}
	tenantID, connID, ok := h.verifyConnectionOwnership(w, r)
	if !ok {
		return
	}
	seq := h.extractDLQSeq(w, r)
	if seq == 0 {
		return
	}
	entry, payload, err := messaging.GetDLQRaw(js, tenantID, connID, seq)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", err.Error(), nil)
		return
	}
	// Try to attach parsed envelope payload for the UI.
	var parsed any
	if err := json.Unmarshal(payload, &parsed); err == nil {
		entry.PayloadJSON = parsed
	} else {
		entry.PayloadJSON = string(payload)
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: entry})
}

// RetryDLQMessage handles POST /api/v1/connections/{id}/dlq/{seq}/retry.
func (h *Handler) RetryDLQMessage(w http.ResponseWriter, r *http.Request) {
	js, ok := h.requireJS(w)
	if !ok {
		return
	}
	tenantID, connID, ok := h.verifyConnectionOwnership(w, r)
	if !ok {
		return
	}
	seq := h.extractDLQSeq(w, r)
	if seq == 0 {
		return
	}
	if err := messaging.RetryDLQ(js, tenantID, connID, seq); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "RetryFailed", err.Error(), nil)
		return
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: map[string]any{"retried": seq}})
}

// DiscardDLQMessage handles POST /api/v1/connections/{id}/dlq/{seq}/discard.
func (h *Handler) DiscardDLQMessage(w http.ResponseWriter, r *http.Request) {
	js, ok := h.requireJS(w)
	if !ok {
		return
	}
	_, _, ok = h.verifyConnectionOwnership(w, r)
	if !ok {
		return
	}
	seq := h.extractDLQSeq(w, r)
	if seq == 0 {
		return
	}
	if err := messaging.DiscardDLQ(js, seq); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DiscardFailed", err.Error(), nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) extractDLQSeq(w http.ResponseWriter, r *http.Request) uint64 {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/connections/"), "/")
	// expect: <id> dlq <seq> [retry|discard]
	if len(parts) < 3 {
		_ = writeError(w, http.StatusBadRequest, "InvalidPath", "sequence required", nil)
		return 0
	}
	seq, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || seq == 0 {
		_ = writeError(w, http.StatusBadRequest, "InvalidSequence", "sequence must be a positive integer", nil)
		return 0
	}
	return seq
}

// DLQRouter routes /api/v1/connections/{id}/dlq/... requests to the right
// handler. The /api/v1/connections/{id} path is already claimed by the
// connection CRUD pattern, so we wire DLQ via a separate mux entry that
// matches the trailing path.
func (h *Handler) DLQRouter(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/connections/"), "/")
	// parts: [{id}, "dlq", optional <seq>, optional "retry"|"discard"]
	if len(parts) < 2 || parts[1] != "dlq" {
		http.NotFound(w, r)
		return
	}
	switch len(parts) {
	case 2:
		if r.Method != http.MethodGet {
			_ = writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "use GET", nil)
			return
		}
		h.ListDLQMessages(w, r)
	case 3:
		if r.Method != http.MethodGet {
			_ = writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "use GET", nil)
			return
		}
		h.GetDLQMessage(w, r)
	case 4:
		if r.Method != http.MethodPost {
			_ = writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "use POST", nil)
			return
		}
		switch parts[3] {
		case "retry":
			h.RetryDLQMessage(w, r)
		case "discard":
			h.DiscardDLQMessage(w, r)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}
