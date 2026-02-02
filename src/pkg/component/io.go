package component

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Input defines the interface for reading messages from external systems or queues.
// Implementations include NATS subscribers, HTTP servers, file watchers, etc.
type Input interface {
	// Start initializes the input and connects to the source.
	// Must be called before Read(). Returns an error if initialization fails.
	Start(ctx context.Context) error

	// Read retrieves the next message from the input source.
	// Returns an error if the input fails or context is cancelled.
	Read(ctx context.Context) (*envelope.Envelope, error)

	// Close gracefully shuts down the input source.
	Close() error
}

// Output defines the interface for writing messages to external systems or queues.
// Implementations include HTTP clients, NATS publishers, file writers, etc.
type Output interface {
	// Start initializes the output and prepares the destination.
	// Must be called before Write(). Returns an error if initialization fails.
	Start(ctx context.Context) error

	// Write sends an envelope to the output destination.
	// Returns an error if the output fails.
	Write(ctx context.Context, env *envelope.Envelope) error

	// Close gracefully shuts down the output destination.
	Close() error
}
