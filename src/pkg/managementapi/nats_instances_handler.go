package managementapi

import (
	"net/http"
)

// Tenant NATS service-discovery API (#21). Workers (and the UI) resolve the set
// of NATS instances for a tenant here instead of relying on a single hardcoded
// URL, so newly-provisioned instances are picked up automatically.

// natsInstancesResponse is the discovery payload: the full instance records
// plus a convenience comma-join-ready list of client URLs.
type natsInstancesResponse struct {
	Instances []*NATSInstance `json:"instances"`
	URLs      []string        `json:"urls"`
}

// natsInstanceStore returns the NATSInstanceStore backing this handler, or false
// if the repository doesn't support it (e.g. a narrow test mock).
func (h *Handler) natsInstanceStore() (NATSInstanceStore, bool) {
	s, ok := h.repo.(NATSInstanceStore)
	return s, ok
}

// HandleListNATSInstances: GET /api/v1/tenants/{tenant_id}/nats-instances
// Returns the tenant's active NATS instances + their client URLs. Any member
// may read it (workers and the UI both consume it).
func (h *Handler) HandleListNATSInstances(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	store, ok := h.natsInstanceStore()
	if !ok {
		// No discovery backend → empty set; callers fall back to NATS_URL.
		_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: natsInstancesResponse{Instances: []*NATSInstance{}, URLs: []string{}}})
		return
	}
	instances, err := store.ListNATSInstances(r.Context(), tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	urls := make([]string, 0, len(instances))
	for _, n := range instances {
		urls = append(urls, n.NATSURL())
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: natsInstancesResponse{Instances: instances, URLs: urls}})
}
