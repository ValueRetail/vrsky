// Package managementapi - Orchestrator interface for K8s integration (Phase 2)
package managementapi

import "context"

// PipelineOrchestrator defines the interface for deploying and managing
// pipeline components on Kubernetes. This interface allows the handler
// to use the orchestrator package without creating an import cycle.
//
// Implementations must:
// - Deploy K8s Deployments for each node in a graph-based connection
// - Clean up all K8s resources when stopping a connection
// - Handle partial failures according to project decisions (leave partial deployments)
type PipelineOrchestrator interface {
	// StartPipeline deploys all components for a connection to Kubernetes.
	// It creates K8s Deployments for each node in the execution order.
	//
	// Returns an error if deployment fails for any component.
	// Previously deployed components are left running (partial deployment).
	StartPipeline(ctx context.Context, conn *Connection) error

	// StopPipeline removes all K8s resources for a connection.
	// It deletes all Deployments associated with the connection ID.
	//
	// Returns an error if cleanup fails.
	StopPipeline(ctx context.Context, conn *Connection) error

	// GetPipelineStatus returns the status of each node's deployment.
	// Returns a map of nodeID -> status (running, starting, stopped).
	GetPipelineStatus(ctx context.Context, conn *Connection) (map[string]string, error)
}

// OrchestratorFactory creates PipelineOrchestrator instances.
// This allows the orchestrator to be injected without direct import.
type OrchestratorFactory func(conn *Connection) PipelineOrchestrator
