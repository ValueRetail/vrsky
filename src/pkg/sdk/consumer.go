package sdk

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// PublishFunc emits an envelope into the pipeline. The SDK injects it into a
// Consumer's Run; the connector never touches NATS directly.
type PublishFunc func(ctx context.Context, env *envelope.Envelope) error

// Consumer ingests from the outside world (webhook, poll, file watch, …) and
// publishes envelopes into the pipeline. Unlike a Producer it drives its own
// ingestion loop: Run blocks until ctx is cancelled.
type Consumer interface {
	component.Component
	Configure(ctx context.Context, res *Resources) error
	Run(ctx context.Context, publish PublishFunc) error
}

// BaseConsumer is embedded by consumer connectors.
type BaseConsumer struct {
	baseKit
}

// Type identifies this as a consumer node.
func (b *BaseConsumer) Type() component.ComponentType { return component.TypeConsumer }
