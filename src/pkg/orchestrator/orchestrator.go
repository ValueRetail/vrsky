// Package orchestrator - Main orchestrator type and lifecycle management
package orchestrator

import (
	"context"
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Orchestrator manages the deployment and lifecycle of pipeline components
// to a Kubernetes cluster. It transforms the graph-based connection model
// (nodes and edges) into K8s Deployments and coordinates component startup/shutdown.
type Orchestrator struct {
	// Connection being orchestrated
	Connection *managementapi.Connection

	// K8sClient is the Kubernetes client for deploying resources
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
//   - k8sClient: Kubernetes client for deploying resources
//   - config: Orchestrator configuration (namespace, images, etc.)
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

// StartConnection deploys all pipeline components to Kubernetes.
// It creates K8s Deployments for each node in the execution order.
//
// IMPORTANT: Call BuildGraph() before calling this method.
//
// Deployment follows the execution order (consumer first, producer last).
// If any deployment fails, the method returns an error but leaves
// previously deployed components running (partial deployment - decision B).
//
// Returns:
//   - error: If deployment fails for any component
func (o *Orchestrator) StartConnection(ctx context.Context) error {
	if o.K8sClient == nil {
		return NewOrchestratorError(ErrCodeK8sClientNil, "Kubernetes client is nil", nil)
	}

	if o.Graph == nil {
		return NewOrchestratorError(ErrCodeInvalidGraph, "execution graph not built - call BuildGraph first", nil)
	}

	// Create deployment specs for all nodes
	specs, err := CreateAllDeploymentSpecs(o.Graph, o.Config)
	if err != nil {
		return err
	}

	// Track successfully deployed nodes for error reporting
	var deployedNodes []string

	// Deploy each component in execution order
	for _, spec := range specs {
		err := o.deployComponent(ctx, spec)
		if err != nil {
			// Partial deployment - leave deployed components and return error
			return NewOrchestratorError(ErrCodePartialDeployment,
				fmt.Sprintf("failed to deploy node %s: %v", spec.NodeID, err),
				map[string]string{
					"failedNode":    spec.NodeID,
					"deployedNodes": fmt.Sprintf("%v", deployedNodes),
				})
		}
		deployedNodes = append(deployedNodes, spec.NodeID)
	}

	return nil
}

// deployComponent deploys a single component to Kubernetes.
func (o *Orchestrator) deployComponent(ctx context.Context, spec *DeploymentSpec) error {
	deploymentsClient := o.K8sClient.AppsV1().Deployments(o.Config.Namespace)

	// Check if deployment already exists
	existing, err := deploymentsClient.Get(ctx, spec.Deployment.Name, metav1.GetOptions{})
	if err == nil && existing != nil {
		// Deployment exists - update it
		_, err = deploymentsClient.Update(ctx, spec.Deployment, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update deployment %s: %w", spec.Deployment.Name, err)
		}
		return o.applyHPA(ctx, spec)
	}

	// Create new deployment
	_, err = deploymentsClient.Create(ctx, spec.Deployment, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create deployment %s: %w", spec.Deployment.Name, err)
	}

	return o.applyHPA(ctx, spec)
}

// applyHPA creates or updates the HorizontalPodAutoscaler for a deployment so
// the connection's worker scales between min/max replicas under load (#135).
func (o *Orchestrator) applyHPA(ctx context.Context, spec *DeploymentSpec) error {
	if spec.HPA == nil {
		return nil
	}
	hpaClient := o.K8sClient.AutoscalingV2().HorizontalPodAutoscalers(o.Config.Namespace)
	if existing, err := hpaClient.Get(ctx, spec.HPA.Name, metav1.GetOptions{}); err == nil && existing != nil {
		spec.HPA.ResourceVersion = existing.ResourceVersion
		if _, err = hpaClient.Update(ctx, spec.HPA, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update HPA %s: %w", spec.HPA.Name, err)
		}
		return nil
	}
	if _, err := hpaClient.Create(ctx, spec.HPA, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create HPA %s: %w", spec.HPA.Name, err)
	}
	return nil
}

// StopConnection removes all pipeline components from Kubernetes.
// It deletes all K8s Deployments associated with the connection.
//
// Returns:
//   - error: If cleanup fails
func (o *Orchestrator) StopConnection(ctx context.Context) error {
	if o.K8sClient == nil {
		return NewOrchestratorError(ErrCodeK8sClientNil, "Kubernetes client is nil", nil)
	}

	deploymentsClient := o.K8sClient.AppsV1().Deployments(o.Config.Namespace)

	// Build label selector to find all deployments for this connection
	labels := GetDeploymentLabelsForConnection(o.Connection.ID)
	labelSelector := BuildLabelSelector(labels)

	// List all deployments for this connection
	deployments, err := deploymentsClient.List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return NewOrchestratorError(ErrCodeK8sDeleteFailed,
			fmt.Sprintf("failed to list deployments: %v", err),
			map[string]string{"connectionID": o.Connection.ID})
	}

	// Delete each deployment
	var deleteErrors []string
	for _, deployment := range deployments.Items {
		err := deploymentsClient.Delete(ctx, deployment.Name, metav1.DeleteOptions{})
		if err != nil {
			deleteErrors = append(deleteErrors, fmt.Sprintf("%s: %v", deployment.Name, err))
		}
	}

	// Delete the connection's HPAs too (#135) — they share the connection
	// label selector. Best-effort: an HPA delete failure shouldn't mask the
	// deployment teardown result.
	hpaClient := o.K8sClient.AutoscalingV2().HorizontalPodAutoscalers(o.Config.Namespace)
	if hpas, listErr := hpaClient.List(ctx, metav1.ListOptions{LabelSelector: labelSelector}); listErr == nil {
		for _, hpa := range hpas.Items {
			if err := hpaClient.Delete(ctx, hpa.Name, metav1.DeleteOptions{}); err != nil {
				deleteErrors = append(deleteErrors, fmt.Sprintf("hpa/%s: %v", hpa.Name, err))
			}
		}
	}

	if len(deleteErrors) > 0 {
		return NewOrchestratorError(ErrCodeK8sDeleteFailed,
			fmt.Sprintf("failed to delete some resources: %v", deleteErrors),
			map[string]string{"connectionID": o.Connection.ID})
	}

	return nil
}

// GetNATSTopicForNode returns the output NATS topic for a node.
// Returns empty string for the producer (it has no output topic).
func (o *Orchestrator) GetNATSTopicForNode(nodeID string) (string, error) {
	if o.Graph == nil {
		return "", NewOrchestratorError(ErrCodeInvalidGraph, "execution graph not built", nil)
	}

	// Verify node exists
	if _, err := o.Graph.GetNodeByID(nodeID); err != nil {
		return "", err
	}

	return GetOutputTopicForNode(o.Graph, nodeID), nil
}

// GetDeploymentStatus returns the status of deployments for this connection.
// This is useful for monitoring pipeline health.
func (o *Orchestrator) GetDeploymentStatus(ctx context.Context) (map[string]string, error) {
	if o.K8sClient == nil {
		return nil, NewOrchestratorError(ErrCodeK8sClientNil, "Kubernetes client is nil", nil)
	}

	deploymentsClient := o.K8sClient.AppsV1().Deployments(o.Config.Namespace)

	// Build label selector to find all deployments for this connection
	labels := GetDeploymentLabelsForConnection(o.Connection.ID)
	labelSelector := BuildLabelSelector(labels)

	// List all deployments for this connection
	deployments, err := deploymentsClient.List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, NewOrchestratorError(ErrCodeK8sDeployFailed,
			fmt.Sprintf("failed to list deployments: %v", err),
			map[string]string{"connectionID": o.Connection.ID})
	}

	// Build status map
	status := make(map[string]string)
	for _, deployment := range deployments.Items {
		nodeID := deployment.Labels[LabelNode]
		if deployment.Status.ReadyReplicas > 0 {
			status[nodeID] = "running"
		} else if deployment.Status.Replicas > 0 {
			status[nodeID] = "starting"
		} else {
			status[nodeID] = "stopped"
		}
	}

	return status, nil
}

// GetGraph returns the execution graph.
// Returns nil if BuildGraph has not been called.
func (o *Orchestrator) GetGraph() *ExecutionGraph {
	return o.Graph
}

// GetConfig returns the orchestrator configuration.
func (o *Orchestrator) GetConfig() *OrchestratorConfig {
	return o.Config
}

// ValidateConnection validates the connection's DAG structure.
// This is a convenience method that wraps the validator.
func (o *Orchestrator) ValidateConnection() error {
	if o.Validator == nil {
		return nil // No validator configured
	}
	return o.Validator.ValidateDAG(o.Connection)
}
