package managementapi

import "context"

// Repository defines the interface for connection persistence operations
type Repository interface {
	// CreateConnection creates a new connection in the database
	CreateConnection(ctx context.Context, connection *Connection) error

	// GetConnection retrieves a connection by ID
	GetConnection(ctx context.Context, id string) (*Connection, error)

	// ListConnections retrieves connections for a tenant with optional filtering
	ListConnections(ctx context.Context, tenantID string, filters *ListFilters) ([]*Connection, int64, error)

	// UpdateConnection updates an existing connection
	UpdateConnection(ctx context.Context, connection *Connection) error

	// DeleteConnection deletes a connection and its related events
	DeleteConnection(ctx context.Context, id string) error

	// UpdateConnectionStatus updates the status of a connection
	UpdateConnectionStatus(ctx context.Context, id string, status string, lastError *string) error

	// CreateConnectionEvent creates a new connection event
	CreateConnectionEvent(ctx context.Context, event *ConnectionEvent) error

	// GetConnectionEvents retrieves events for a connection
	GetConnectionEvents(ctx context.Context, connectionID string) ([]*ConnectionEvent, error)

	// Close closes the repository connection
	Close() error
}

// ListFilters provides filtering options for listing connections
type ListFilters struct {
	Status string // Optional: filter by status (stopped, running, error)
	Search string // Optional: search by name
	Limit  int    // Default: 20, max: 100
	Offset int    // Default: 0
}

// ListResult contains the result of a list operation
type ListResult struct {
	Total       int64
	Connections []*Connection
}
