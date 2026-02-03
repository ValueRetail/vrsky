package envelope

import (
	"time"
)

// Envelope represents a message as it flows through the VRSky pipeline.
// It contains the actual payload and metadata about its journey.
type Envelope struct {
	// Core identifiers
	ID            string `json:"id"`
	TenantID      string `json:"tenant_id"`
	IntegrationID string `json:"integration_id"`

	// Payload (inline or reference)
	Payload     []byte `json:"payload,omitempty"`     // For payloads < 256KB
	PayloadRef  string `json:"payload_ref,omitempty"` // MinIO reference for large payloads
	PayloadSize int64  `json:"payload_size"`
	ContentType string `json:"content_type"`

	// Pipeline tracking
	Source      string   `json:"source"`       // Component that created this envelope
	CurrentStep int      `json:"current_step"` // Current position in pipeline
	StepHistory []string `json:"step_history"` // Path through pipeline

	// Timestamps
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`

	// Error handling
	RetryCount int    `json:"retry_count"`
	LastError  string `json:"last_error,omitempty"`
}

// New creates a new envelope with a generated ID and timestamps
func New() *Envelope {
	return &Envelope{
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(15 * time.Minute), // 15-minute TTL by default
		RetryCount:  0,
		StepHistory: []string{},
	}
}
