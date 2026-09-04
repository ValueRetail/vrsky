package managementapi

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Node represents a component in the pipeline graph
// Nodes can be consumers, filters, converters, or producers
type Node struct {
	ID         string               `json:"id"`                   // Unique node ID (e.g., "consumer-0", "filter-1", "converter-0", "producer-0")
	Type       string               `json:"type"`                 // "consumer", "filter", "converter", "producer"
	Config     json.RawMessage      `json:"config"`               // Type-specific configuration (SourceConfig, FilterConfig, etc.)
	Enabled    bool                 `json:"enabled"`              // Whether this node is active
	Checkpoint *ComponentCheckpoint `json:"checkpoint,omitempty"` // For stateful components (runtime state, not persisted with connection)
}

// Edge represents a connection between two nodes in the pipeline graph
type Edge struct {
	ID     string `json:"id"`     // Unique edge ID (e.g., "edge-0", "edge-1")
	Source string `json:"source"` // Source node ID
	Target string `json:"target"` // Target node ID
	Order  int    `json:"order"`  // Ordering for UI rendering and execution (0 = first)
}

// ComponentCheckpoint stores the last processed message info for a component
// Used for resumable processing and exactly-once semantics
type ComponentCheckpoint struct {
	LastProcessedMessageID string    `json:"last_processed_message_id"`
	LastProcessedAt        time.Time `json:"last_processed_at"`
	MessageCount           int64     `json:"message_count"`
}

// Connection represents a data pipeline connection
// Supports both the new graph-based model (Nodes/Edges) and the legacy linear model
type Connection struct {
	ID          string `json:"id" db:"id"`
	TenantID    string `json:"tenant_id" db:"tenant_id"`
	Name        string `json:"name" db:"name"`
	Description string `json:"description" db:"description"`

	// NEW: Graph-based pipeline model (Phase 1)
	// When Nodes/Edges are populated, they take precedence over legacy fields
	Nodes []*Node `json:"nodes" db:"nodes"`
	Edges []*Edge `json:"edges" db:"edges"`

	Status    string     `json:"status" db:"status"` // stopped, running, error
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt time.Time  `json:"updated_at" db:"updated_at"`
	StartedAt *time.Time `json:"started_at" db:"started_at"`
	StoppedAt *time.Time `json:"stopped_at" db:"stopped_at"`
	LastError *string    `json:"last_error" db:"last_error"`
}

// WebhookSignatureConfig declares how to verify an incoming webhook's HMAC
// signature. When set on an HTTP consumer node, the webhook-consumer service
// rejects requests with a missing or invalid signature (401).
//
// The shared secret is stored in the `secrets` table (#66) and referenced by
// SecretID. Leaving SecretID empty disables verification (legacy/anon mode).
type WebhookSignatureConfig struct {
	Header    string `json:"header"`           // Request header that carries the signature (e.g. "X-Hub-Signature-256")
	Algorithm string `json:"algorithm"`        // "hmac-sha1" | "hmac-sha256" | "hmac-sha512"
	SecretID  string `json:"secret_id"`        // Reference into the secrets table
	Encoding  string `json:"encoding"`         // "hex" | "base64"
	Prefix    string `json:"prefix,omitempty"` // Literal prefix to strip from the header value, e.g. "sha256="
}

// AuthConfig represents authentication configuration
type AuthConfig struct {
	Type   string            `json:"type"` // basic, bearer, oauth, api_key
	Basic  *BasicAuthConfig  `json:"basic,omitempty"`
	Bearer *BearerAuthConfig `json:"bearer,omitempty"`
	APIKey *APIKeyAuthConfig `json:"api_key,omitempty"`
}

// BasicAuthConfig represents HTTP Basic authentication
type BasicAuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// BearerAuthConfig represents Bearer token authentication
type BearerAuthConfig struct {
	Token string `json:"token"`
}

// APIKeyAuthConfig represents API key authentication
type APIKeyAuthConfig struct {
	HeaderName string `json:"header_name"`
	Key        string `json:"key"`
}

// PollingConfig represents polling configuration
type PollingConfig struct {
	Interval int `json:"interval"` // Seconds
	Timeout  int `json:"timeout"`  // Seconds
}

// ConnectionEvent represents an event in the connection lifecycle
type ConnectionEvent struct {
	ID           string          `json:"id" db:"id"`
	ConnectionID string          `json:"connection_id" db:"connection_id"`
	TenantID     string          `json:"tenant_id" db:"tenant_id"`
	EventType    string          `json:"event_type" db:"event_type"` // started, stopped, error, metrics_snapshot, config_changed
	EventData    json.RawMessage `json:"event_data" db:"event_data"`
	CreatedAt    time.Time       `json:"created_at" db:"created_at"`
}

// Metrics represents real-time metrics for a connection
type Metrics struct {
	ConnectionID   string    `json:"connection_id"`
	TenantID       string    `json:"tenant_id"`
	Timestamp      time.Time `json:"timestamp"`
	Input          int64     `json:"input"`           // Messages received
	AfterFilter    int64     `json:"after_filter"`    // After filtering
	AfterConverter int64     `json:"after_converter"` // After conversion
	Output         int64     `json:"output"`          // Successfully sent
	Errors         int64     `json:"errors"`          // Failed messages
	Throughput     float64   `json:"throughput"`      // Messages/sec
}

// MetricsSnapshot is a snapshot of the latest metrics
type MetricsSnapshot struct {
	ConnectionID   string    `json:"connection_id"`
	TenantID       string    `json:"tenant_id"`
	Timestamp      time.Time `json:"timestamp"`
	Input          int64     `json:"input"`
	AfterFilter    int64     `json:"after_filter"`
	AfterConverter int64     `json:"after_converter"`
	Output         int64     `json:"output"`
	Errors         int64     `json:"errors"`
	Throughput     float64   `json:"throughput"`
	IsRunning      bool      `json:"is_running"`
	LastUpdateTime time.Time `json:"last_update_time"`
}

// CreateConnectionRequest is the request to create a new connection
// Supports both the new graph-based model (Nodes/Edges) and the legacy linear model
type CreateConnectionRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`

	// Graph-based pipeline model: the nodes and the edges between them.
	Nodes []*Node `json:"nodes,omitempty"`
	Edges []*Edge `json:"edges,omitempty"`
}

// UpdateConnectionRequest is the request to update a connection.
type UpdateConnectionRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`

	// Graph-based pipeline model: the nodes and the edges between them.
	Nodes []*Node `json:"nodes,omitempty"`
	Edges []*Edge `json:"edges,omitempty"`
}

// NewConnection creates a new Connection with default values.
func NewConnection(tenantID string, req CreateConnectionRequest) *Connection {
	now := time.Now().UTC()
	conn := &Connection{
		ID:          uuid.New().String(),
		TenantID:    tenantID,
		Name:        req.Name,
		Description: req.Description,
		Status:      "stopped",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	conn.Nodes = req.Nodes
	conn.Edges = req.Edges

	return conn
}

// NewConnectionEvent creates a new connection event
func NewConnectionEvent(connectionID, tenantID, eventType string, eventData json.RawMessage) *ConnectionEvent {
	return &ConnectionEvent{
		ID:           uuid.New().String(),
		ConnectionID: connectionID,
		TenantID:     tenantID,
		EventType:    eventType,
		EventData:    eventData,
		CreatedAt:    time.Now().UTC(),
	}
}
