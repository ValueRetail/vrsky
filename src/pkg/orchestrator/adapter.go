// Package orchestrator - Adapter that implements the managementapi.PipelineOrchestrator interface
package orchestrator

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"k8s.io/client-go/kubernetes"
)

// NATSURLResolver returns the NATS URL a connection's workers should dial,
// and true when one is known. It lets the orchestrator point per-connection
// worker Deployments at the tenant's placed NATS instance (#19) instead of the
// static config default — the pkg/runtime workers can't self-discover. Returns
// ("", false) for connections with no dedicated instance (single-NATS/compose),
// in which case the static OrchestratorConfig.NATSURLs is used.
type NATSURLResolver func(ctx context.Context, tenantID, connectionID string) (string, bool)

// FactoryOption configures the orchestrator factory / adapter.
type FactoryOption func(*PipelineOrchestratorAdapter)

// WithNATSURLResolver wires a per-connection NATS URL resolver (#19 placement).
func WithNATSURLResolver(r NATSURLResolver) FactoryOption {
	return func(a *PipelineOrchestratorAdapter) { a.natsURLResolver = r }
}

// PipelineOrchestratorAdapter wraps Orchestrator to implement managementapi.PipelineOrchestrator.
// This adapter breaks the import cycle by providing a bridge between packages.
type PipelineOrchestratorAdapter struct {
	k8sClient       kubernetes.Interface
	config          *OrchestratorConfig
	validator       *managementapi.Validator
	natsURLResolver NATSURLResolver
}

// NewPipelineOrchestratorAdapter creates a new adapter.
func NewPipelineOrchestratorAdapter(
	k8sClient kubernetes.Interface,
	config *OrchestratorConfig,
	validator *managementapi.Validator,
	opts ...FactoryOption,
) *PipelineOrchestratorAdapter {
	if config == nil {
		config = DefaultConfig()
	}
	a := &PipelineOrchestratorAdapter{
		k8sClient: k8sClient,
		config:    config,
		validator: validator,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// configForConn returns the orchestrator config to use for a connection: the
// base config, but with NATSURLs overridden to the connection's placed NATS
// instance when a resolver is set and the connection is placed (#19). Cloned so
// concurrent connections on different instances don't race on shared config.
func (a *PipelineOrchestratorAdapter) configForConn(ctx context.Context, conn *managementapi.Connection) *OrchestratorConfig {
	if a.natsURLResolver == nil {
		return a.config
	}
	url, ok := a.natsURLResolver(ctx, conn.TenantID, conn.ID)
	if !ok || url == "" {
		return a.config
	}
	cloned := *a.config
	cloned.NATSURLs = url
	return &cloned
}

// StartPipeline implements managementapi.PipelineOrchestrator.
// It deploys all components for a connection to Kubernetes.
func (a *PipelineOrchestratorAdapter) StartPipeline(ctx context.Context, conn *managementapi.Connection) error {
	orch := New(conn, a.k8sClient, a.configForConn(ctx, conn), a.validator)

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
	opts ...FactoryOption,
) managementapi.OrchestratorFactory {
	adapter := NewPipelineOrchestratorAdapter(k8sClient, config, validator, opts...)
	return func(conn *managementapi.Connection) managementapi.PipelineOrchestrator {
		// Return the same adapter instance - it creates new Orchestrator instances per call
		return adapter
	}
}
