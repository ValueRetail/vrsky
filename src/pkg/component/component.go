package component

import "context"

// ComponentType identifies the type of component in the pipeline
type ComponentType string

const (
	TypeConsumer  ComponentType = "consumer"
	TypeProducer  ComponentType = "producer"
	TypeConverter ComponentType = "converter"
	TypeFilter    ComponentType = "filter"
)

// HealthStatus represents the current health of a component
type HealthStatus string

const (
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthStopped   HealthStatus = "stopped"
)

// Component is the base interface for all VRSky components
type Component interface {
	// Name returns the component's human-readable name
	Name() string

	// Type returns the component type (consumer, producer, converter, filter)
	Type() ComponentType

	// Version returns the component version
	Version() string

	// Start initializes and starts the component
	Start(ctx context.Context) error

	// Stop gracefully shuts down the component
	Stop(ctx context.Context) error

	// Health returns the current health status of the component
	Health() HealthStatus
}
