// Package orchestrator - Kubernetes Deployment template generation
package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// CreateDeploymentSpec creates a Kubernetes Deployment specification for a node.
// The deployment includes:
// - Proper labels for identification (app, pipeline, node, type, tenant)
// - Environment variables for component configuration
// - Resource requests and limits
// - Liveness probe for health checking
// - Prometheus metrics port
func CreateDeploymentSpec(node *managementapi.Node, graph *ExecutionGraph, config *OrchestratorConfig) (*DeploymentSpec, error) {
	if node == nil {
		return nil, NewOrchestratorError(ErrCodeInvalidNodeType, "node is nil", nil)
	}

	if !IsValidNodeType(node.Type) {
		return nil, NewOrchestratorError(ErrCodeInvalidNodeType,
			fmt.Sprintf("invalid node type: %s", node.Type),
			map[string]string{"nodeType": node.Type})
	}

	// Build environment variables
	envVars, err := buildEnvironmentVariables(node, graph, config)
	if err != nil {
		return nil, err
	}

	// Build labels
	labels := buildLabels(graph.ConnectionID, node.ID, node.Type, graph.TenantID)

	// Build the deployment
	deployment := buildDeployment(node, graph, config, labels, envVars)

	return &DeploymentSpec{
		NodeID:     node.ID,
		NodeType:   node.Type,
		Deployment: deployment,
	}, nil
}

// buildEnvironmentVariables builds the environment variables for a component.
func buildEnvironmentVariables(node *managementapi.Node, graph *ExecutionGraph, config *OrchestratorConfig) ([]corev1.EnvVar, error) {
	// Serialize node config to JSON
	configJSON := "{}"
	if len(node.Config) > 0 {
		// Validate it's valid JSON
		var temp interface{}
		if err := json.Unmarshal(node.Config, &temp); err != nil {
			return nil, NewOrchestratorError(ErrCodeInvalidGraph,
				fmt.Sprintf("invalid config JSON for node %s: %v", node.ID, err),
				map[string]string{"nodeID": node.ID})
		}
		configJSON = string(node.Config)
	}

	// Get input/output topics
	inputTopic := GetInputTopicForNode(graph, node.ID)
	outputTopic := GetOutputTopicForNode(graph, node.ID)

	envVars := []corev1.EnvVar{
		{Name: EnvTenantID, Value: graph.TenantID},
		{Name: EnvConnectionID, Value: graph.ConnectionID},
		{Name: EnvNodeID, Value: node.ID},
		{Name: EnvNodeType, Value: node.Type},
		{Name: EnvInputNATSSubject, Value: inputTopic},
		{Name: EnvOutputNATSSubject, Value: outputTopic},
		{Name: EnvConfig, Value: configJSON},
		{Name: EnvNATSURLs, Value: config.NATSURLs},
		{Name: EnvNATSAccount, Value: config.NATSAccount},
	}

	return envVars, nil
}

// buildLabels builds the Kubernetes labels for a deployment.
func buildLabels(connectionID, nodeID, nodeType, tenantID string) map[string]string {
	return map[string]string{
		LabelApp:      LabelAppValue,
		LabelPipeline: sanitizeLabelValue(connectionID),
		LabelNode:     sanitizeLabelValue(nodeID),
		LabelType:     nodeType,
		LabelTenant:   sanitizeLabelValue(tenantID),
	}
}

// sanitizeLabelValue sanitizes a string for use as a Kubernetes label value.
// Label values must be 63 characters or less and match regex [a-z0-9A-Z_.-]*
func sanitizeLabelValue(s string) string {
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// buildDeployment builds the Kubernetes Deployment object.
func buildDeployment(node *managementapi.Node, graph *ExecutionGraph, config *OrchestratorConfig, labels map[string]string, envVars []corev1.EnvVar) *appsv1.Deployment {
	replicas := int32(1)
	containerImage := GetContainerImage(config, node.Type)
	deploymentName := buildDeploymentName(graph.ConnectionID, node.ID)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					LabelPipeline: labels[LabelPipeline],
					LabelNode:     labels[LabelNode],
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "component",
							Image: containerImage,
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: HealthCheckPort,
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "metrics",
									ContainerPort: MetricsPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: envVars,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(CPURequest),
									corev1.ResourceMemory: resource.MustParse(MemoryRequest),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(CPULimit),
									corev1.ResourceMemory: resource.MustParse(MemoryLimit),
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: HealthCheckPath,
										Port: intstr.FromInt(HealthCheckPort),
									},
								},
								InitialDelaySeconds: LivenessProbeInitialDelay,
								PeriodSeconds:       LivenessProbePeriod,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: HealthCheckPath,
										Port: intstr.FromInt(HealthCheckPort),
									},
								},
								InitialDelaySeconds: LivenessProbeInitialDelay,
								PeriodSeconds:       LivenessProbePeriod,
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
		},
	}
}

// buildDeploymentName builds the name for a Kubernetes Deployment.
// Format: vrsky-{connectionID}-{nodeID}
// Name is truncated to 63 characters (K8s limit) if necessary.
func buildDeploymentName(connectionID, nodeID string) string {
	name := fmt.Sprintf("vrsky-%s-%s", connectionID, nodeID)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// CreateAllDeploymentSpecs creates deployment specs for all nodes in the execution graph.
// Returns deployments in execution order (consumer first, producer last).
func CreateAllDeploymentSpecs(graph *ExecutionGraph, config *OrchestratorConfig) ([]*DeploymentSpec, error) {
	var deployments []*DeploymentSpec

	for _, nodeID := range graph.ExecutionOrder {
		node, err := graph.GetNodeByID(nodeID)
		if err != nil {
			return nil, err
		}

		spec, err := CreateDeploymentSpec(node, graph, config)
		if err != nil {
			return nil, err
		}

		deployments = append(deployments, spec)
	}

	return deployments, nil
}

// GetDeploymentLabelsForConnection returns the labels used to identify all deployments
// belonging to a connection. Used for listing/deleting all deployments for a connection.
func GetDeploymentLabelsForConnection(connectionID string) map[string]string {
	return map[string]string{
		LabelApp:      LabelAppValue,
		LabelPipeline: sanitizeLabelValue(connectionID),
	}
}

// GetDeploymentLabelsForTenant returns the labels used to identify all deployments
// belonging to a tenant. Used for listing/deleting all deployments for a tenant.
func GetDeploymentLabelsForTenant(tenantID string) map[string]string {
	return map[string]string{
		LabelApp:    LabelAppValue,
		LabelTenant: sanitizeLabelValue(tenantID),
	}
}

// BuildLabelSelector builds a Kubernetes label selector string from a map of labels.
// Example: "app=vrsky,pipeline=conn-123"
func BuildLabelSelector(labels map[string]string) string {
	var parts []string
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}
