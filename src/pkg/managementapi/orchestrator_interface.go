// Package managementapi - Orchestrator interface for K8s integration (Phase 2)
package managementapi

import "context"

// PipelineOrchestrator defines the interface the handler uses to prepare and
// tear down a connection's Kubernetes state, without importing the orchestrator
// package (which would create an import cycle).
//
// Since #201/#205 there is no per-connection deployment: standing platform
// services run every node kind, activated by the connection start/stop commands
// the handler publishes. Implementations validate the graph on start and clean
// up whatever cluster state a connection still owns on stop.
type PipelineOrchestrator interface {
	// StartPipeline validates a connection's graph and prepares it to run.
	//
	// Returns an error if the connection's graph is invalid, in which case the
	// handler must not mark it running.
	StartPipeline(ctx context.Context, conn *Connection) error

	// StopPipeline removes any K8s resources still associated with a connection.
	//
	// Returns an error if cleanup fails.
	StopPipeline(ctx context.Context, conn *Connection) error
}

// OrchestratorFactory creates PipelineOrchestrator instances.
// This allows the orchestrator to be injected without direct import.
type OrchestratorFactory func(conn *Connection) PipelineOrchestrator
