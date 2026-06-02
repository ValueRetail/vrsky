package sdk

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Filter gates / reshapes envelopes mid-pipeline. Evaluate returns keep=false
// to drop the envelope, or keep=true with the envelope to forward (which may
// be the same pointer or a rewritten one). The SDK runner owns the subscribe
// + republish; the connector implements only Configure + Evaluate.
type Filter interface {
	component.Component
	Configure(ctx context.Context, res *Resources) error
	Evaluate(ctx context.Context, env *envelope.Envelope) (keep bool, out *envelope.Envelope, err error)
}

// BaseFilter is embedded by filter connectors.
type BaseFilter struct {
	baseKit
}

// Type identifies this as a filter node.
func (b *BaseFilter) Type() component.ComponentType { return component.TypeFilter }
