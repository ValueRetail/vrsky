package managementapi

import (
	"sync"
	"time"
)

// ClientRegistry manages WebSocket clients connected to each connection
type ClientRegistry struct {
	mu      sync.RWMutex
	clients map[string][]*WSClient // key: connectionID, value: list of connected clients
}

// WSClient represents a WebSocket client
type WSClient struct {
	ID            string      // Unique client ID
	ConnectionID  string      // Connection being monitored
	TenantID      string      // Tenant ID
	Ch            chan []byte // Channel to send messages to client
	ConnectedAt   time.Time   // When client connected
	LastMessageAt time.Time   // Last message sent time
}

// NewClientRegistry creates a new client registry
func NewClientRegistry() *ClientRegistry {
	return &ClientRegistry{
		clients: make(map[string][]*WSClient),
	}
}

// RegisterClient registers a new WebSocket client
func (cr *ClientRegistry) RegisterClient(connID string, client *WSClient) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	if _, exists := cr.clients[connID]; !exists {
		cr.clients[connID] = make([]*WSClient, 0)
	}
	cr.clients[connID] = append(cr.clients[connID], client)
}

// UnregisterClient removes a client from the registry
func (cr *ClientRegistry) UnregisterClient(connID string, clientID string) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	clients, exists := cr.clients[connID]
	if !exists {
		return
	}

	for i, client := range clients {
		if client.ID == clientID {
			// Remove client from slice
			cr.clients[connID] = append(clients[:i], clients[i+1:]...)
			if len(cr.clients[connID]) == 0 {
				delete(cr.clients, connID)
			}
			return
		}
	}
}

// GetClients retrieves all clients for a connection
func (cr *ClientRegistry) GetClients(connID string) []*WSClient {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	clients, exists := cr.clients[connID]
	if !exists {
		return make([]*WSClient, 0)
	}

	// Return a copy to avoid external modification
	result := make([]*WSClient, len(clients))
	copy(result, clients)
	return result
}

// BroadcastToConnection sends a message to all clients connected to a specific connection
func (cr *ClientRegistry) BroadcastToConnection(connID string, message []byte) {
	cr.mu.RLock()
	clients := cr.clients[connID]
	cr.mu.RUnlock()

	for _, client := range clients {
		select {
		case client.Ch <- message:
		default:
			// Client channel full, skip
		}
	}
}

// BroadcastToTenant sends a message to all clients from a specific tenant
func (cr *ClientRegistry) BroadcastToTenant(tenantID string, message []byte) {
	cr.mu.RLock()
	allClients := make([]*WSClient, 0)
	for _, clients := range cr.clients {
		for _, client := range clients {
			if client.TenantID == tenantID {
				allClients = append(allClients, client)
			}
		}
	}
	cr.mu.RUnlock()

	for _, client := range allClients {
		select {
		case client.Ch <- message:
		default:
			// Client channel full, skip
		}
	}
}

// GetConnectionClientCount returns the number of connected clients for a connection
func (cr *ClientRegistry) GetConnectionClientCount(connID string) int {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	clients, exists := cr.clients[connID]
	if !exists {
		return 0
	}
	return len(clients)
}

// GetTotalClientCount returns the total number of connected clients
func (cr *ClientRegistry) GetTotalClientCount() int {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	total := 0
	for _, clients := range cr.clients {
		total += len(clients)
	}
	return total
}

// GetStats returns registry statistics
func (cr *ClientRegistry) GetStats() map[string]interface{} {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	connectionStats := make(map[string]int)
	totalClients := 0

	for connID, clients := range cr.clients {
		connectionStats[connID] = len(clients)
		totalClients += len(clients)
	}

	return map[string]interface{}{
		"total_clients":      totalClients,
		"active_connections": len(cr.clients),
		"connections":        connectionStats,
	}
}

// Close closes all client channels
func (cr *ClientRegistry) Close() error {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	for _, clients := range cr.clients {
		for _, client := range clients {
			close(client.Ch)
		}
	}

	cr.clients = make(map[string][]*WSClient)
	return nil
}
