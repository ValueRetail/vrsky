// Package orchestrator - Kubernetes labelling for per-connection resources.
//
// This file used to build a Deployment + HorizontalPodAutoscaler per pipeline
// node. That machinery is gone (ADR 0004): the workers it produced were wired
// to {tenant}.pipeline-{conn}.{node}.output subjects nothing publishes to, and
// every node kind is served by a standing platform service instead. What is left
// is the labelling scheme those resources carried, which the teardown and orphan
// GC paths still need to find workers created by older versions.
package orchestrator

import (
	"fmt"
	"strings"
)

// buildLabels builds the Kubernetes labels a per-connection resource carries.
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

// buildDeploymentName builds the name of a per-connection worker Deployment.
// Format: vrsky-{connectionID}-{nodeID}
// Name is truncated to 63 characters (K8s limit) if necessary.
func buildDeploymentName(connectionID, nodeID string) string {
	name := fmt.Sprintf("vrsky-%s-%s", connectionID, nodeID)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// GetDeploymentLabelsForConnection returns the labels used to identify all resources
// belonging to a connection. Used for listing/deleting all resources for a connection.
func GetDeploymentLabelsForConnection(connectionID string) map[string]string {
	return map[string]string{
		LabelApp:      LabelAppValue,
		LabelPipeline: sanitizeLabelValue(connectionID),
	}
}

// GetDeploymentLabelsForTenant returns the labels used to identify all resources
// belonging to a tenant. Used for listing/deleting all resources for a tenant.
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
