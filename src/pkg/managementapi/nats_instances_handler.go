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
//
// DataPlane is the honest part. The urls field reads like something to dial,
// and until ADR 0005 nothing in the payload said otherwise — a caller could
// reasonably conclude a placed connection's data flows through the instance it
// names. It does not: tenant instances run core NATS with no JetStream, and the
// standing connector services dial the platform NATS from their own pod env.
// Naming the data plane in the response is what stops the next caller drawing
// the same wrong conclusion from the same field.
type natsInstancesResponse struct {
	Instances []*NATSInstance `json:"instances"`
	URLs      []string        `json:"urls"`
	// DataPlane names where tenant data actually flows — always
	// "platform-nats" under ADR 0005. Placement is accounting, not routing.
	DataPlane string `json:"data_plane"`
}

// dataPlanePlatformNATS is the only value DataPlane takes while ADR 0005
// stands. It becomes per-instance the day tenant instances can carry data.
const dataPlanePlatformNATS = "platform-nats"

// natsInstanceStore returns the NATSInstanceStore backing this handler, or false
// if the repository doesn't support it (e.g. a narrow test mock).
func (h *Handler) natsInstanceStore() (NATSInstanceStore, bool) {
	s, ok := h.repo.(NATSInstanceStore)
	return s, ok
}

// placeConnection records a connection against the least-loaded active NATS
// instance for its tenant (#19). No-op when the tenant has no tracked instances
// (single-instance / compose) or the connection is already placed.
//
// WHAT THIS DOES AND DOES NOT DO. It writes a row. It does not change where the
// connection's data flows.
//
// This comment used to say "so its workers connect to the right instance",
// which was true when the orchestrator stamped NATS_URLS onto a per-connection
// worker Deployment. #201 and #205 replaced those pods with standing connector
// services, and a standing service serves every tenant from one process — it
// dials the NATS_URL in its own pod env, which deploy-connectors-azure.sh
// points at the platform NATS. So a connection placed on a dedicated instance
// still moves its data over the shared one.
//
// The record is not useless: it is a real count, and it is what the autoscaler
// scales and meters on. It is only a claim about ROUTING that no longer holds.
// Do not let it be read as isolation — see #209 for the four ways to make it
// true and the product question that picks between them.
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
		_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: natsInstancesResponse{Instances: []*NATSInstance{}, URLs: []string{}, DataPlane: dataPlanePlatformNATS}})
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
				DataPlane: dataPlanePlatformNATS,
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
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: natsInstancesResponse{Instances: instances, URLs: urls, DataPlane: dataPlanePlatformNATS}})
}
