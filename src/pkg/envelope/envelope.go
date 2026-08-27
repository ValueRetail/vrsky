package envelope

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
	// Checksum is "sha256:<hex>" of the payload, set when the payload is
	// offloaded (PayloadRef) and verified when it is read back, so corruption or
	// a mismatched object is caught rather than delivered. Empty for inline
	// payloads and for envelopes written before checksums existed.
	Checksum string `json:"checksum,omitempty"`

	// Pipeline tracking
	Source      string   `json:"source"`       // Component that created this envelope
	CurrentStep int      `json:"current_step"` // Current position in pipeline
	StepHistory []string `json:"step_history"` // Path through pipeline

	// Metadata - arbitrary key-value pairs for custom data (e.g., CDC operation, table name)
	Metadata map[string]interface{} `json:"metadata,omitempty"`

	// Timestamps
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`

	// Error handling
	RetryCount int    `json:"retry_count"`
	LastError  string `json:"last_error,omitempty"`
}

// New creates a new envelope with a generated ID and timestamps.
//
// The ID is not decoration. It is the JetStream dedup key (Nats-Msg-Id) and the
// object key for claim-check payload offload (spill/<tenant>/<connection>/<id>),
// so an envelope without one loses duplicate suppression AND collides with every
// other offloaded payload on its connection. Callers may overwrite ID with their
// own stable identifier (that is how a source's natural key gets dedup); they
// must not clear it.
func New() *Envelope {
	return &Envelope{
		ID:          uuid.NewString(),
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(15 * time.Minute), // 15-minute TTL by default
		RetryCount:  0,
		StepHistory: []string{},
	}
}

// Marshal serializes an envelope to JSON bytes
func Marshal(e *Envelope) ([]byte, error) {
	return json.Marshal(e)
}

// Unmarshal deserializes JSON bytes into an envelope
func Unmarshal(data []byte) (*Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}
