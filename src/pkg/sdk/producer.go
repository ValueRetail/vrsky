package sdk

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Producer delivers pipeline envelopes to the outside world (HTTP, file, DB,
// …). The SDK runner owns the durable JetStream subscription and hands each
// envelope to Deliver; the connector implements only Configure + Deliver.
//
// Deliver's error is classified (see errors.go): nil acks, Retriable NAKs for
// redelivery, Permanent acks+logs (poison), and a bare error is Retriable.
type Producer interface {
	component.Component
	Configure(ctx context.Context, res *Resources) error
	Deliver(ctx context.Context, env *envelope.Envelope) error
}

// BaseProducer is embedded by producer connectors. It supplies Name/Version/
// Start/Stop/Health + the RegisterHTTPHandler hook, so an author writes only
// Configure + Deliver.
type BaseProducer struct {
	baseKit
}

// Type identifies this as a producer node.
func (b *BaseProducer) Type() component.ComponentType { return component.TypeProducer }
