package component

import (
	"context"
	"log/slog"
	"sync"
)

// GenericProducer implements the Producer interface with pluggable I/O
type GenericProducer struct {
	name   string
	input  Input
	output Output
	mu     sync.RWMutex
	health HealthStatus
}

// New creates a new generic producer
func New(input Input, output Output) *GenericProducer {
	return &GenericProducer{
		name:   "VRSky-Producer",
		input:  input,
		output: output,
		health: HealthStopped,
	}
}

// Name returns the producer's name
func (p *GenericProducer) Name() string {
	return p.name
}

// Type returns the component type
func (p *GenericProducer) Type() ComponentType {
	return TypeProducer
}

// Version returns the producer version
func (p *GenericProducer) Version() string {
	return "0.1.0"
}

// Start initializes the producer
func (p *GenericProducer) Start(ctx context.Context) error {
	slog.Info("Producer starting",
		"name", p.name,
		"version", p.Version())

	p.mu.Lock()
	p.health = HealthHealthy
	p.mu.Unlock()

	return nil
}

// Stop gracefully shuts down the producer
func (p *GenericProducer) Stop(ctx context.Context) error {
	slog.Info("Producer stopping")

	p.mu.Lock()
	p.health = HealthStopped
	p.mu.Unlock()

	if p.input != nil {
		if err := p.input.Close(); err != nil {
			slog.Error("Failed to close input", "error", err)
		}
	}

	if p.output != nil {
		if err := p.output.Close(); err != nil {
			slog.Error("Failed to close output", "error", err)
		}
	}

	return nil
}

// Health returns the current health status
func (p *GenericProducer) Health() HealthStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health
}

// Configure sets up the producer configuration
func (p *GenericProducer) Configure(config []byte) error {
	slog.Debug("Producer configured", "config_size", len(config))
	return nil
}

// Process runs the main producer loop: read from input, write to output
func (p *GenericProducer) Process(ctx context.Context, input Input, output Output) error {
	p.input = input
	p.output = output

	slog.Info("Producer starting main loop")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Producer context cancelled")
			return ctx.Err()
		default:
		}

		// Read message from input
		env, err := input.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // Context cancelled, exit gracefully
			}
			slog.Error("Failed to read from input", "error", err)
			continue
		}

		// Write message to output
		if err := output.Write(ctx, env); err != nil {
			slog.Error("Failed to write to output",
				"message_id", env.ID,
				"error", err)
			// Continue processing next message (error already logged and retried by output)
		}
	}
}
