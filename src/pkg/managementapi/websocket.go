package managementapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// WebSocketMessage represents a message sent over WebSocket
type WebSocketMessage struct {
	Type      string      `json:"type"` // "metrics", "event", "connection_status", "ping", "pong"
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// ClientMessage represents a message received from client
type ClientMessage struct {
	Type string      `json:"type"` // "subscribe", "unsubscribe", "pong"
	Data interface{} `json:"data"`
}

// WebSocketClient represents a connected WebSocket client
type WebSocketClient struct {
	ID            string
	ConnectionID  string
	TenantID      string
	Ch            chan []byte
	DoneCh        chan struct{}
	ConnectedAt   time.Time
	LastMessageAt time.Time
	mu            sync.RWMutex
}

// NewWebSocketClient creates a new WebSocket client
func NewWebSocketClient(connID, tenantID string) *WebSocketClient {
	return &WebSocketClient{
		ID:           uuid.New().String(),
		ConnectionID: connID,
		TenantID:     tenantID,
		Ch:           make(chan []byte, 256),
		DoneCh:       make(chan struct{}),
		ConnectedAt:  time.Now().UTC(),
	}
}

// Send sends a message to the client
func (c *WebSocketClient) Send(msg []byte) error {
	select {
	case c.Ch <- msg:
		c.mu.Lock()
		c.LastMessageAt = time.Now().UTC()
		c.mu.Unlock()
		return nil
	case <-c.DoneCh:
		return fmt.Errorf("client closed")
	default:
		return fmt.Errorf("channel full")
	}
}

// Close closes the client connection
func (c *WebSocketClient) Close() {
	close(c.DoneCh)
}

// HandleMetricsWebSocket handles WebSocket-like connections for real-time metrics
// Endpoint: GET /api/v1/connections/{id}/metrics/ws
// Uses HTTP Server-Sent Events (SSE) as a fallback to WebSocket
// with heartbeat pings for connection keep-alive
func (h *Handler) HandleMetricsWebSocket(w http.ResponseWriter, r *http.Request) {
	// Get tenant ID from context
	tenantID, err := GetTenantIDFromContext(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Get connection ID from URL
	connID := r.PathValue("id")
	if connID == "" {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Verify connection exists and belongs to tenant
	conn, err := h.repo.GetConnection(r.Context(), connID)
	if err != nil {
		if _, ok := err.(*NotFoundError); ok {
			writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to retrieve connection", nil)
		}
		return
	}

	if conn.TenantID != tenantID {
		writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Create WebSocket client
	client := NewWebSocketClient(connID, tenantID)

	// Register client for metrics updates
	if h.clientRegistry != nil {
		h.clientRegistry.RegisterClient(connID, &WSClient{
			ID:           client.ID,
			ConnectionID: client.ConnectionID,
			TenantID:     client.TenantID,
			Ch:           client.Ch,
			ConnectedAt:  client.ConnectedAt,
		})
		defer h.clientRegistry.UnregisterClient(connID, client.ID)
	}

	// Set up SSE headers with WebSocket-like behavior
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Pragma", "no-cache")
	// CORS headers are managed by CORSMiddleware - don't override here

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "InternalError", "streaming not supported", nil)
		return
	}

	// Send connection established message
	connMsg := WebSocketMessage{
		Type:      "connected",
		Timestamp: time.Now().UTC(),
		Data: map[string]interface{}{
			"client_id":     client.ID,
			"connection_id": connID,
		},
	}
	data, _ := json.Marshal(connMsg)
	_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
	flusher.Flush()

	// Send initial metrics state
	if h.metricsCache != nil {
		if metrics := h.metricsCache.GetConnectionMetrics(connID); metrics != nil {
			msg := WebSocketMessage{
				Type:      "metrics",
				Timestamp: time.Now().UTC(),
				Data:      metrics,
			}
			data, _ := json.Marshal(msg)
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()
		}
	}

	// Context for cancellation
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Heartbeat ticker
	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

	// Stream metrics and keep-alive
	for {
		select {
		case data := <-client.Ch:
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()

		case <-heartbeatTicker.C:
			// Send periodic heartbeat/ping
			heartbeat := WebSocketMessage{
				Type:      "ping",
				Timestamp: time.Now().UTC(),
				Data:      map[string]string{"type": "heartbeat"},
			}
			data, _ := json.Marshal(heartbeat)
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()

		case <-ctx.Done():
			client.Close()
			// Send disconnection message
			disconnectMsg := WebSocketMessage{
				Type:      "disconnected",
				Timestamp: time.Now().UTC(),
				Data:      map[string]string{"reason": "client closed"},
			}
			data, _ := json.Marshal(disconnectMsg)
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()
			return

		case <-client.DoneCh:
			return
		}
	}
}

// InitializeWebSocketSupport initializes WebSocket support in the handler
func (h *Handler) InitializeWebSocketSupport(clientRegistry *ClientRegistry, metricsCache *MetricsCache) {
	h.clientRegistry = clientRegistry
	h.metricsCache = metricsCache
}

// HandleMetricsSSE is an alias for HandleMetricsWebSocket for backward compatibility
// Deprecated: Use HandleMetricsWebSocket instead
func (h *Handler) HandleMetricsSSE(w http.ResponseWriter, r *http.Request) {
	h.HandleMetricsWebSocket(w, r)
}
