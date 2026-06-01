package sdk

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Converter transforms an envelope's payload mid-pipeline, always emitting
// exactly one envelope. The SDK runner owns subscribe + republish; the
// connector implements only Configure + Convert.
type Converter interface {
	component.Component
	Configure(ctx context.Context, res *Resources) error
	Convert(ctx context.Context, env *envelope.Envelope) (*envelope.Envelope, error)
}

// BaseConverter is embedded by converter connectors.
type BaseConverter struct {
	baseKit
}

// Type identifies this as a converter node.
func (b *BaseConverter) Type() component.ComponentType { return component.TypeConverter }
