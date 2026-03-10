// Package orchestrator manages the deployment and lifecycle of pipeline components
// to a Kubernetes cluster. It transforms the graph-based connection model
// (nodes and edges) into K8s Deployments and coordinates component startup/shutdown.
package orchestrator

import (
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	appsv1 "k8s.io/api/apps/v1"
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

// TopicPair contains the input and output NATS topics for a node.
type TopicPair struct {
	// InputTopic is where this node reads from (empty for consumer)
	InputTopic string

	// OutputTopic is where this node writes to (empty for producer)
	OutputTopic string
}

// DeploymentSpec wraps a K8s Deployment with orchestrator metadata.
type DeploymentSpec struct {
	// NodeID is the ID of the node this deployment represents
	NodeID string

	// NodeType is the type of node (consumer, filter, converter, producer)
	NodeType string

	// Deployment is the K8s Deployment specification
	Deployment *appsv1.Deployment
}

// OrchestratorConfig contains configuration for the orchestrator.
type OrchestratorConfig struct {
	// Namespace is the K8s namespace to deploy to
	Namespace string

	// ImageRegistry is the container registry (e.g., "gcr.io/vrsky")
	ImageRegistry string

	// ImageVersion is the image tag to use (e.g., "latest")
	ImageVersion string

	// NATSURLs is the NATS server URLs for components
	NATSURLs string

	// NATSAccount is the NATS account for tenant isolation
	NATSAccount string
}

// DefaultConfig returns the default orchestrator configuration.
func DefaultConfig() *OrchestratorConfig {
	return &OrchestratorConfig{
		Namespace:     "vrsky",
		ImageRegistry: "gcr.io/vrsky",
		ImageVersion:  "latest",
		NATSURLs:      "nats://nats:4222",
		NATSAccount:   "",
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
	ErrCodeInvalidGraph      = "INVALID_GRAPH"
	ErrCodeK8sDeployFailed   = "K8S_DEPLOY_FAILED"
	ErrCodeK8sDeleteFailed   = "K8S_DELETE_FAILED"
	ErrCodeK8sClientNil      = "K8S_CLIENT_NIL"
	ErrCodeTopologicalSort   = "TOPOLOGICAL_SORT_FAILED"
	ErrCodeImageNotFound     = "IMAGE_NOT_FOUND"
	ErrCodeNodeNotFound      = "NODE_NOT_FOUND"
	ErrCodeInvalidNodeType   = "INVALID_NODE_TYPE"
	ErrCodePartialDeployment = "PARTIAL_DEPLOYMENT"
)

// NewOrchestratorError creates a new OrchestratorError.
func NewOrchestratorError(code, message string, details map[string]string) *OrchestratorError {
	return &OrchestratorError{
		Code:    code,
		Message: message,
		Details: details,
	}
}

// GetContainerImage returns the container image for a given node type.
// Uses the fixed registry pattern: {registry}/vrsky-{nodeType}:{version}
func GetContainerImage(config *OrchestratorConfig, nodeType string) string {
	return fmt.Sprintf("%s/vrsky-%s:%s", config.ImageRegistry, nodeType, config.ImageVersion)
}

// ValidNodeTypes contains the valid node types for pipeline components.
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

// Resource limits and requests for K8s deployments
const (
	// CPURequest is the CPU request for each component (100m = 0.1 CPU)
	CPURequest = "100m"

	// MemoryRequest is the memory request for each component
	MemoryRequest = "128Mi"

	// CPULimit is the CPU limit for each component (500m = 0.5 CPU)
	CPULimit = "500m"

	// MemoryLimit is the memory limit for each component
	MemoryLimit = "512Mi"

	// HealthCheckPort is the port for health check endpoint
	HealthCheckPort = 8080

	// MetricsPort is the port for Prometheus metrics
	MetricsPort = 9090

	// HealthCheckPath is the path for liveness probe
	HealthCheckPath = "/health"

	// LivenessProbeInitialDelay is the initial delay before liveness probe starts
	LivenessProbeInitialDelay = 5

	// LivenessProbePeriod is the period between liveness probes
	LivenessProbePeriod = 10
)

// K8s label keys
const (
	LabelApp      = "app"
	LabelPipeline = "pipeline"
	LabelNode     = "node"
	LabelType     = "type"
	LabelTenant   = "tenant"

	// LabelAppValue is the value for the "app" label
	LabelAppValue = "vrsky"
)

// Environment variable names for component configuration
const (
	EnvTenantID          = "TENANT_ID"
	EnvConnectionID      = "CONNECTION_ID"
	EnvNodeID            = "NODE_ID"
	EnvNodeType          = "NODE_TYPE"
	EnvInputNATSSubject  = "INPUT_NATS_SUBJECT"
	EnvOutputNATSSubject = "OUTPUT_NATS_SUBJECT"
	EnvConfig            = "CONFIG"
	EnvNATSURLs          = "NATS_URLS"
	EnvNATSAccount       = "NATS_ACCOUNT"
)
