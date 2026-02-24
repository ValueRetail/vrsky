package managementapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// WebSocketMessage represents a message sent over WebSocket
type WebSocketMessage struct {
	Type      string      `json:"type"` // "metrics", "event", "connection_status"
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// HandleMetricsSSE handles Server-Sent Events for real-time metrics
// Endpoint: GET /api/v1/connections/{id}/metrics/stream
// Alternative to WebSocket using HTTP Server-Sent Events
func (h *Handler) HandleMetricsSSE(w http.ResponseWriter, r *http.Request) {
	// Get tenant ID from context
	tenantID, ok := r.Context().Value("tenant_id").(string)
	if !ok || tenantID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Get connection ID from URL
	connID := r.PathValue("id")
	if connID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Verify connection exists and belongs to tenant
	conn, err := h.repo.GetConnection(r.Context(), connID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if conn.TenantID != tenantID {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// Create client
	clientID := uuid.New().String()
	client := &WSClient{
		ID:           clientID,
		ConnectionID: connID,
		TenantID:     tenantID,
		Ch:           make(chan []byte, 100),
		ConnectedAt:  time.Now().UTC(),
	}

	// Register client
	if h.clientRegistry != nil {
		h.clientRegistry.RegisterClient(connID, client)
		defer h.clientRegistry.UnregisterClient(connID, clientID)
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Send initial metrics state
	if h.metricsCache != nil {
		if metrics := h.metricsCache.GetConnectionMetrics(connID); metrics != nil {
			msg := WebSocketMessage{
				Type:      "metrics",
				Timestamp: time.Now().UTC(),
				Data:      metrics,
			}
			data, _ := json.Marshal(msg)
			w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()
		}
	}

	// Stream metrics
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case data := <-client.Ch:
			w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()
			client.LastMessageAt = time.Now().UTC()

		case <-ticker.C:
			// Send periodic health check
			msg := WebSocketMessage{
				Type:      "ping",
				Timestamp: time.Now().UTC(),
				Data:      nil,
			}
			data, _ := json.Marshal(msg)
			w.Write([]byte(": " + string(data) + "\n\n"))
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

// InitializeWebSocketSupport initializes WebSocket support in the handler
func (h *Handler) InitializeWebSocketSupport(clientRegistry *ClientRegistry, metricsCache *MetricsCache) {
	h.clientRegistry = clientRegistry
	h.metricsCache = metricsCache
}
