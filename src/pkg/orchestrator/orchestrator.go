// Package orchestrator - Main orchestrator type and lifecycle management
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Orchestrator turns a connection's node/edge graph into a validated execution
// order, and owns what little Kubernetes state a connection still has.
//
// It no longer deploys anything. Every node kind is served by a standing
// platform service — the shared data-filter/data-converter for transforms
// (#201) and the SDK connector services for sources and destinations (#205) —
// which the management API activates by publishing
// vrsky.commands.{tenant}.connection.start. See ADR 0004.
//
// What remains here is validation (a connection that cannot be ordered must not
// start) and cleanup of the per-connection worker Deployments that older
// versions created.
type Orchestrator struct {
	// Connection being orchestrated
	Connection *managementapi.Connection

	// K8sClient is the Kubernetes client for managing resources
	K8sClient kubernetes.Interface

	// Config contains orchestrator configuration
	Config *OrchestratorConfig

	// Validator for DAG validation
	Validator *managementapi.Validator

	// Graph is the computed execution graph (populated by BuildGraph)
	Graph *ExecutionGraph
}

// New creates a new Orchestrator instance.
//
// Args:
//   - conn: The connection to orchestrate
//   - k8sClient: Kubernetes client for managing resources
//   - config: Orchestrator configuration (namespace, NATS, …)
//   - validator: Validator for graph structure validation
//
// Returns:
//   - *Orchestrator: New orchestrator instance
func New(conn *managementapi.Connection, k8sClient kubernetes.Interface, config *OrchestratorConfig, validator *managementapi.Validator) *Orchestrator {
	if config == nil {
		config = DefaultConfig()
	}

	return &Orchestrator{
		Connection: conn,
		K8sClient:  k8sClient,
		Config:     config,
		Validator:  validator,
	}
}

// BuildGraph validates the connection and builds the execution graph.
// This must be called before StartConnection.
//
// Returns:
//   - *ExecutionGraph: The validated and ordered execution graph
//   - error: If validation fails or graph building fails
func (o *Orchestrator) BuildGraph(ctx context.Context) (*ExecutionGraph, error) {
	graph, err := BuildExecutionGraph(o.Connection, o.Validator)
	if err != nil {
		return nil, err
	}

	o.Graph = graph
	return graph, nil
}

// StartConnection prepares a connection to run.
//
// IMPORTANT: Call BuildGraph() before calling this method — the graph build is
// where an unorderable or malformed pipeline is rejected, and reaching this
// method means the connection is about to be marked running.
//
// Nothing is deployed: the standing services pick the connection up from the
// start command the management API publishes next. The one Kubernetes action
// left is sweeping away per-connection worker Deployments from before #201/#205,
// so a connection created back then sheds its leftover no-op pods the next time
// it starts. That sweep is best-effort — a stale worker is inert, so failing a
// start over one would be strictly worse than leaving it running.
func (o *Orchestrator) StartConnection(ctx context.Context) error {
	if o.K8sClient == nil {
		return NewOrchestratorError(ErrCodeK8sClientNil, "Kubernetes client is nil", nil)
	}

	if o.Graph == nil {
		return NewOrchestratorError(ErrCodeInvalidGraph, "execution graph not built - call BuildGraph first", nil)
	}

	slog.Default().Info("orchestrator starting connection",
		"connection", o.Graph.ConnectionID, "nodes", len(o.Graph.ExecutionOrder))

	if errs := o.deleteConnectionWorkers(ctx); len(errs) > 0 {
		slog.Default().Warn("orchestrator could not sweep legacy per-connection workers",
			"connection", o.Connection.ID, "errors", errs)
	}

	return nil
}

// StopConnection removes any Kubernetes resources still associated with the
// connection. Since #201/#205 that is only the per-connection workers older
// versions deployed; a connection created after them owns no cluster state, and
// stopping it is the DB status change plus the stop command the management API
// publishes.
//
// Returns:
//   - error: If cleanup fails
func (o *Orchestrator) StopConnection(ctx context.Context) error {
	if o.K8sClient == nil {
		return NewOrchestratorError(ErrCodeK8sClientNil, "Kubernetes client is nil", nil)
	}

	if errs := o.deleteConnectionWorkers(ctx); len(errs) > 0 {
		return NewOrchestratorError(ErrCodeK8sDeleteFailed,
			fmt.Sprintf("failed to delete some resources: %v", errs),
			map[string]string{"connectionID": o.Connection.ID})
	}

	return nil
}

// deleteConnectionWorkers removes every Deployment and HorizontalPodAutoscaler
// labelled for this connection, returning a message per failure. Both the start
// sweep and the stop teardown want exactly this; they differ only in whether a
// failure is fatal.
func (o *Orchestrator) deleteConnectionWorkers(ctx context.Context) []string {
	labelSelector := BuildLabelSelector(GetDeploymentLabelsForConnection(o.Connection.ID))
	listOpts := metav1.ListOptions{LabelSelector: labelSelector}

	var errs []string

	deploymentsClient := o.K8sClient.AppsV1().Deployments(o.Config.Namespace)
	deployments, err := deploymentsClient.List(ctx, listOpts)
	if err != nil {
		return []string{fmt.Sprintf("list deployments: %v", err)}
	}
	for i := range deployments.Items {
		name := deployments.Items[i].Name
		if err := deploymentsClient.Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		slog.Default().Info("orchestrator removed per-connection worker",
			"deployment", name, "connection", o.Connection.ID)
	}

	// HPAs share the connection's label selector. Best-effort: an HPA failure
	// shouldn't mask the deployment teardown result.
	hpaClient := o.K8sClient.AutoscalingV2().HorizontalPodAutoscalers(o.Config.Namespace)
	if hpas, listErr := hpaClient.List(ctx, listOpts); listErr == nil {
		for i := range hpas.Items {
			name := hpas.Items[i].Name
			if err := hpaClient.Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
				errs = append(errs, fmt.Sprintf("hpa/%s: %v", name, err))
			}
		}
	}

	return errs
}
