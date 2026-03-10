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

// DAGValidationError represents multiple validation errors in pipeline topology
// Returns all errors at once rather than fail-fast for better UX
type DAGValidationError struct {
	Errors []string
}

func (dve *DAGValidationError) Error() string {
	if len(dve.Errors) == 1 {
		return dve.Errors[0]
	}
	return fmt.Sprintf("pipeline validation failed with %d errors: %v", len(dve.Errors), dve.Errors)
}

// ConsumerCountError indicates invalid number of consumer nodes
type ConsumerCountError struct {
	Found    int
	Expected int
}

func (e *ConsumerCountError) Error() string {
	if e.Found == 0 {
		return "no consumer node found: pipeline requires exactly 1 consumer"
	}
	return fmt.Sprintf("found %d consumer nodes: pipeline requires exactly 1 consumer", e.Found)
}

// ProducerCountError indicates invalid number of producer nodes
type ProducerCountError struct {
	Found    int
	Expected int
}

func (e *ProducerCountError) Error() string {
	if e.Found == 0 {
		return "no producer node found: pipeline requires exactly 1 producer"
	}
	return fmt.Sprintf("found %d producer nodes: pipeline requires exactly 1 producer", e.Found)
}

// CircularDependencyError indicates a cycle was detected in the pipeline graph
type CircularDependencyError struct {
	Message string
}

func (e *CircularDependencyError) Error() string {
	return e.Message
}

// ConsumerIsolatedError indicates the consumer has no outgoing edges
type ConsumerIsolatedError struct {
	ConsumerID string
}

func (e *ConsumerIsolatedError) Error() string {
	return fmt.Sprintf("consumer node '%s' is isolated: no outgoing edges to other nodes", e.ConsumerID)
}

// ProducerUnreachableError indicates the producer cannot be reached from the consumer
type ProducerUnreachableError struct {
	ConsumerID string
	ProducerID string
}

func (e *ProducerUnreachableError) Error() string {
	return fmt.Sprintf("producer node '%s' is not reachable from consumer node '%s'", e.ProducerID, e.ConsumerID)
}

// OrphanedNodesError indicates nodes that are not on the path from consumer to producer
type OrphanedNodesError struct {
	Nodes []string
}

func (e *OrphanedNodesError) Error() string {
	if len(e.Nodes) == 1 {
		return fmt.Sprintf("orphaned node '%s': not on path from consumer to producer", e.Nodes[0])
	}
	return fmt.Sprintf("orphaned nodes %v: not on path from consumer to producer", e.Nodes)
}

// InvalidEdgeError indicates an edge references a non-existent node
type InvalidEdgeError struct {
	EdgeID     string
	InvalidRef string
	RefType    string // "source" or "target"
}

func (e *InvalidEdgeError) Error() string {
	return fmt.Sprintf("edge '%s' has invalid %s: node '%s' does not exist", e.EdgeID, e.RefType, e.InvalidRef)
}
