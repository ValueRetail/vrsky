package component

import "context"

// Producer defines the interface for a component that consumes from an Input
// and publishes to an Output. Producers are the "OUT" components in the pipeline.
type Producer interface {
	Component

	// Configure sets up the producer with the given configuration (JSON)
	Configure(config []byte) error

	// Process starts the producer's main loop:
	// - Reads messages from the Input
	// - Publishes them to the Output
	// - Handles retries and errors
	Process(ctx context.Context, input Input, output Output) error
}
