package managementapi

import (
	"context"
	"log/slog"
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

// placeConnection pins a connection to the least-loaded active NATS instance
// for its tenant (#19), so its workers connect to the right instance. No-op
// when the tenant has no tracked instances (single-instance / compose) or the
// connection is already placed.
func (h *Handler) placeConnection(ctx context.Context, tenantID, connectionID string) {
	store, ok := h.natsInstanceStore()
	if !ok {
		return
	}
	if _, err := store.GetConnectionInstance(ctx, tenantID, connectionID); err == nil {
		return // already placed
	}
	insts, err := store.ListNATSInstances(ctx, tenantID)
	if err != nil || len(insts) == 0 {
		return
	}
	counts, _ := store.CountConnectionsPerInstance(ctx, tenantID)
	var target *NATSInstance
	best := int(^uint(0) >> 1)
	for _, in := range insts {
		if counts[in.ID] < best {
			best, target = counts[in.ID], in
		}
	}
	if target != nil {
		if err := store.AssignConnectionInstance(ctx, tenantID, connectionID, target.ID); err != nil {
			slog.Default().Warn("could not place connection on nats instance",
				"tenant", tenantID, "connection", connectionID, "error", err)
		}
	}
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
	// A worker passes ?connection_id= to resolve the single instance its
	// connection is pinned to (#19 placement). All of a connection's nodes run
	// on the same instance. Falls through to the full active set when the
	// connection isn't placed yet.
	if connID := r.URL.Query().Get("connection_id"); connID != "" {
		if inst, err := store.GetConnectionInstance(r.Context(), tenantID, connID); err == nil {
			_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: natsInstancesResponse{
				Instances: []*NATSInstance{inst}, URLs: []string{inst.NATSURL()},
			}})
			return
		}
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
