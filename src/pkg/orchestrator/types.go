// Package orchestrator validates a connection's graph-based model (nodes and
// edges) into an execution order, and cleans up the per-connection Kubernetes
// workers that older versions deployed.
//
// It does not deploy pipeline components. Standing platform services run every
// node kind — see ADR 0004 and the Orchestrator doc comment.
package orchestrator

import (
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
)

// ExecutionGraph represents a validated and topologically ordered pipeline.
// It contains nodes, edges, and the computed execution order where the consumer
// is always first and the producer is always last.
type ExecutionGraph struct {
	// Nodes maps node ID to node definition
	Nodes map[string]*managementapi.Node

	// Edges contains all edges in the pipeline
	Edges []*managementapi.Edge

	// ExecutionOrder is the topologically sorted order of node IDs.
	// Index 0 is always the consumer, last index is always the producer.
	ExecutionOrder []string

	// ConsumerNodeID is the ID of the consumer node
	ConsumerNodeID string

	// ProducerNodeID is the ID of the producer node
	ProducerNodeID string

	// TenantID is the tenant this pipeline belongs to
	TenantID string

	// ConnectionID is the connection this pipeline belongs to
	ConnectionID string
}

// OrchestratorConfig contains configuration for the orchestrator.
type OrchestratorConfig struct {
	// Namespace is the K8s namespace per-connection resources live in
	Namespace string

	// NATSURLs is the NATS server URL a connection is placed on (#19).
	//
	// NOTE: currently inert. Its only consumer was the NATS_URLS env stamped on
	// per-connection worker pods, which are no longer deployed; the standing
	// connector services dial the NATS_URL in their own env. Tracked in #209.
	NATSURLs string

	// NATSAccount is the NATS account for tenant isolation
	NATSAccount string
}

// DefaultConfig returns the default orchestrator configuration.
func DefaultConfig() *OrchestratorConfig {
	return &OrchestratorConfig{
		Namespace:   "vrsky",
		NATSURLs:    "nats://nats:4222",
		NATSAccount: "",
	}
}

// OrchestratorError represents orchestrator-specific errors.
type OrchestratorError struct {
	// Code is a machine-readable error code
	Code string

	// Message is a human-readable error message
	Message string

	// Details contains additional error context
	Details map[string]string
}

// Error implements the error interface.
func (e *OrchestratorError) Error() string {
	if len(e.Details) > 0 {
		return fmt.Sprintf("%s: %s (details: %v)", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Common error codes
const (
	ErrCodeInvalidGraph    = "INVALID_GRAPH"
	ErrCodeK8sDeleteFailed = "K8S_DELETE_FAILED"
	ErrCodeK8sClientNil    = "K8S_CLIENT_NIL"
	ErrCodeTopologicalSort = "TOPOLOGICAL_SORT_FAILED"
	ErrCodeNodeNotFound    = "NODE_NOT_FOUND"
	ErrCodeInvalidNodeType = "INVALID_NODE_TYPE"
)

// NewOrchestratorError creates a new OrchestratorError.
func NewOrchestratorError(code, message string, details map[string]string) *OrchestratorError {
	return &OrchestratorError{
		Code:    code,
		Message: message,
		Details: details,
	}
}

// ValidNodeTypes contains the valid node types for pipeline components.
//
// Each maps to a standing service that claims the node by its config `type`:
// consumer/producer to the SDK connector services, filter/converter to the
// shared data-filter/data-converter.
var ValidNodeTypes = map[string]bool{
	"consumer":  true,
	"filter":    true,
	"converter": true,
	"producer":  true,
}

// IsValidNodeType returns true if the given type is a valid node type.
func IsValidNodeType(nodeType string) bool {
	return ValidNodeTypes[nodeType]
}

// K8s label keys carried by per-connection resources.
const (
	LabelApp      = "app"
	LabelPipeline = "pipeline"
	LabelNode     = "node"
	LabelType     = "type"
	LabelTenant   = "tenant"

	// LabelAppValue is the value for the "app" label
	LabelAppValue = "vrsky"
)
