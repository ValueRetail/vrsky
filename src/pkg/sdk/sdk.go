package sdk

import (
	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Re-exported types so connector authors import a single package. The SDK is
// a thin public surface over the internal pkg/* building blocks; these aliases
// keep author code free of direct internal imports.
type (
	// Envelope is the message that flows through a VRSky pipeline.
	Envelope = envelope.Envelope
	// HealthStatus is a connector's reported liveness.
	HealthStatus = component.HealthStatus
	// ComponentType is the connector kind (consumer/producer/filter/converter).
	ComponentType = component.ComponentType
)

// NewEnvelope returns a fresh envelope with ID/timestamps populated.
func NewEnvelope() *Envelope { return envelope.New() }
