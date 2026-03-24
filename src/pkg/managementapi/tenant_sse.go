package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TenantSSEHub manages per-tenant SSE connections for provisioning status updates.
// Follows the same pattern as ClientRegistry / websocket.go.
type TenantSSEHub struct {
	mu      sync.RWMutex
	clients map[string]map[string]*TenantSSEClient // tenantID → clientID → client
}

// TenantSSEClient represents a single SSE connection for tenant status.
type TenantSSEClient struct {
	ID       string
	TenantID string
	Ch       chan []byte
	DoneCh   chan struct{}
}

// NewTenantSSEHub creates a new hub.
func NewTenantSSEHub() *TenantSSEHub {
	return &TenantSSEHub{
		clients: make(map[string]map[string]*TenantSSEClient),
	}
}

// Subscribe creates a new SSE client for the given tenant.
func (hub *TenantSSEHub) Subscribe(tenantID string) *TenantSSEClient {
	hub.mu.Lock()
	defer hub.mu.Unlock()

	client := &TenantSSEClient{
		ID:       uuid.New().String(),
		TenantID: tenantID,
		Ch:       make(chan []byte, 64),
		DoneCh:   make(chan struct{}),
	}

	if hub.clients[tenantID] == nil {
		hub.clients[tenantID] = make(map[string]*TenantSSEClient)
	}
	hub.clients[tenantID][client.ID] = client
	return client
}

// Unsubscribe removes a client.
func (hub *TenantSSEHub) Unsubscribe(tenantID, clientID string) {
	hub.mu.Lock()
	defer hub.mu.Unlock()

	if tenantClients, ok := hub.clients[tenantID]; ok {
		if client, exists := tenantClients[clientID]; exists {
			close(client.DoneCh)
			delete(tenantClients, clientID)
		}
		if len(tenantClients) == 0 {
			delete(hub.clients, tenantID)
		}
	}
}

// Broadcast sends a status update to all clients subscribed to a tenant.
func (hub *TenantSSEHub) Broadcast(tenantID string, update ProvisioningStatusUpdate) {
	hub.mu.RLock()
	tenantClients := hub.clients[tenantID]
	hub.mu.RUnlock()

	if len(tenantClients) == 0 {
		return
	}

	msg := WebSocketMessage{
		Type:      "status",
		Timestamp: time.Now().UTC(),
		Data:      update,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	for _, client := range tenantClients {
		select {
		case client.Ch <- data:
		default:
			// Channel full, skip this client
		}
	}
}

// HandleTenantStatusSSE handles GET /api/v1/tenants/{tenant_id}/status/stream
// Streams provisioning status updates via Server-Sent Events.
func (h *Handler) HandleTenantStatusSSE(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}

	if h.tenantSSEHub == nil {
		_ = writeError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "status streaming not available", nil)
		return
	}

	// Subscribe to updates
	client := h.tenantSSEHub.Subscribe(tenant.ID)
	defer h.tenantSSEHub.Unsubscribe(tenant.ID, client.ID)

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Pragma", "no-cache")

	flusher, ok := w.(http.Flusher)
	if !ok {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "streaming not supported", nil)
		return
	}

	// Send initial status from DB
	initialStatus := ProvisioningStatusUpdate{
		TenantID:    tenant.ID,
		Status:      tenant.Status,
		Progress:    0,
		CurrentStep: "",
	}
	if tenant.NATSSlug != nil {
		initialStatus.NATSUrl = NATSUrl(tenant.Slug)
		initialStatus.Progress = 100
		initialStatus.CurrentStep = "Ready"
	}

	// Try to get latest provisioning job for progress info
	if job, err := h.repo.GetLatestProvisioningJob(r.Context(), tenant.ID); err == nil && job != nil {
		initialStatus.Progress = job.Progress
		initialStatus.CurrentStep = job.CurrentStep
		if job.ErrorMsg != "" {
			initialStatus.Error = job.ErrorMsg
		}
	}

	connMsg := WebSocketMessage{
		Type:      "status",
		Timestamp: time.Now().UTC(),
		Data:      initialStatus,
	}
	data, _ := json.Marshal(connMsg)
	_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
	flusher.Flush()

	// Stream updates
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case data := <-client.Ch:
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()

		case <-heartbeat.C:
			ping := WebSocketMessage{
				Type:      "ping",
				Timestamp: time.Now().UTC(),
				Data:      map[string]string{"type": "heartbeat"},
			}
			data, _ := json.Marshal(ping)
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()

		case <-ctx.Done():
			return

		case <-client.DoneCh:
			return
		}
	}
}
