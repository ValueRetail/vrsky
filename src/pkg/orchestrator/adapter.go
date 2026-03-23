// Package orchestrator - Adapter that implements the managementapi.PipelineOrchestrator interface
package orchestrator

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"k8s.io/client-go/kubernetes"
)

// PipelineOrchestratorAdapter wraps Orchestrator to implement managementapi.PipelineOrchestrator.
// This adapter breaks the import cycle by providing a bridge between packages.
type PipelineOrchestratorAdapter struct {
	k8sClient kubernetes.Interface
	config    *OrchestratorConfig
	validator *managementapi.Validator
}

// NewPipelineOrchestratorAdapter creates a new adapter.
func NewPipelineOrchestratorAdapter(
	k8sClient kubernetes.Interface,
	config *OrchestratorConfig,
	validator *managementapi.Validator,
) *PipelineOrchestratorAdapter {
	if config == nil {
		config = DefaultConfig()
	}
	return &PipelineOrchestratorAdapter{
		k8sClient: k8sClient,
		config:    config,
		validator: validator,
	}
}

// StartPipeline implements managementapi.PipelineOrchestrator.
// It deploys all components for a connection to Kubernetes.
func (a *PipelineOrchestratorAdapter) StartPipeline(ctx context.Context, conn *managementapi.Connection) error {
	orch := New(conn, a.k8sClient, a.config, a.validator)

	// Build the execution graph
	if _, err := orch.BuildGraph(ctx); err != nil {
		return err
	}

	// Deploy all components
	return orch.StartConnection(ctx)
}

// StopPipeline implements managementapi.PipelineOrchestrator.
// It removes all K8s resources for a connection.
func (a *PipelineOrchestratorAdapter) StopPipeline(ctx context.Context, conn *managementapi.Connection) error {
	orch := New(conn, a.k8sClient, a.config, a.validator)
	return orch.StopConnection(ctx)
}

// GetPipelineStatus implements managementapi.PipelineOrchestrator.
// It returns the status of each node's deployment.
func (a *PipelineOrchestratorAdapter) GetPipelineStatus(ctx context.Context, conn *managementapi.Connection) (map[string]string, error) {
	orch := New(conn, a.k8sClient, a.config, a.validator)
	return orch.GetDeploymentStatus(ctx)
}

// NewOrchestratorFactory creates an OrchestratorFactory function for use with managementapi.Handler.
// This factory creates adapters that reuse the same K8s client and config for all connections.
//
// Usage:
//
//	k8sClient, _ := kubernetes.NewForConfig(config)
//	factory := orchestrator.NewOrchestratorFactory(k8sClient, orchestrator.DefaultConfig(), validator)
//	handler.SetOrchestratorFactory(factory)
func NewOrchestratorFactory(
	k8sClient kubernetes.Interface,
	config *OrchestratorConfig,
	validator *managementapi.Validator,
) managementapi.OrchestratorFactory {
	adapter := NewPipelineOrchestratorAdapter(k8sClient, config, validator)
	return func(conn *managementapi.Connection) managementapi.PipelineOrchestrator {
		// Return the same adapter instance - it creates new Orchestrator instances per call
		return adapter
	}
}
