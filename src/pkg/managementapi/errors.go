package managementapi

import "fmt"

// Error types for Management API
var (
	ErrConnectionNotFound      = fmt.Errorf("connection not found")
	ErrInvalidConfiguration    = fmt.Errorf("invalid configuration")
	ErrTenantIDMissing         = fmt.Errorf("tenant ID is missing")
	ErrConnectionNameExists    = fmt.Errorf("connection name already exists for this tenant")
	ErrConnectionRunning       = fmt.Errorf("cannot perform operation on a running connection")
	ErrConnectionNotRunning    = fmt.Errorf("connection is not running")
	ErrNATSUnavailable         = fmt.Errorf("NATS service is unavailable")
	ErrDatabaseError           = fmt.Errorf("database error")
	ErrValidationFailed        = fmt.Errorf("validation failed")
	ErrTooManyGenerators       = fmt.Errorf("too many generators for this connection")
	ErrPayloadTooLarge         = fmt.Errorf("payload exceeds maximum size")
	ErrInvalidPayload          = fmt.Errorf("invalid payload format")
	ErrGeneratorAlreadyRunning = fmt.Errorf("generator is already running for this connection")
)

// ValidationError provides detailed validation error information
type ValidationError struct {
	Field   string
	Message string
	Value   interface{}
}

func (ve *ValidationError) Error() string {
	return fmt.Sprintf("validation error at %s: %s (value: %v)", ve.Field, ve.Message, ve.Value)
}

// ConfigError represents a configuration validation error
type ConfigError struct {
	Component string // source, converter, filter, destination
	Field     string
	Reason    string
}

func (ce *ConfigError) Error() string {
	return fmt.Sprintf("invalid %s configuration at %s: %s", ce.Component, ce.Field, ce.Reason)
}

// BadRequestError represents a 400 Bad Request error
type BadRequestError struct {
	Message string
	Details map[string]string
}

func (bre *BadRequestError) Error() string {
	return bre.Message
}

// NotFoundError represents a 404 Not Found error
type NotFoundError struct {
	ResourceType string
	ResourceID   string
}

func (nfe *NotFoundError) Error() string {
	return fmt.Sprintf("%s not found: %s", nfe.ResourceType, nfe.ResourceID)
}

// ConflictError represents a 409 Conflict error
type ConflictError struct {
	Message string
}

func (ce *ConflictError) Error() string {
	return ce.Message
}
