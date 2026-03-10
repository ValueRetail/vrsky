// Package orchestrator - NATS topic naming for pipeline components
package orchestrator

import (
	"fmt"
	"strings"
)

// NATSTopicPattern defines the pattern for NATS topics.
// Format: {tenantID}.pipeline-{connectionID}.{nodeID}.output
const NATSTopicPattern = "%s.pipeline-%s.%s.output"

// GetOutputTopic returns the NATS output topic for a node.
// Format: {tenantID}.pipeline-{connectionID}.{nodeID}.output
//
// Example: tenant-acme.pipeline-conn-123.consumer-0.output
func GetOutputTopic(tenantID, connectionID, nodeID string) string {
	return fmt.Sprintf(NATSTopicPattern, sanitizeTopicPart(tenantID), sanitizeTopicPart(connectionID), sanitizeTopicPart(nodeID))
}

// GetInputTopicForNode returns the NATS input topic for a node based on the execution graph.
// The input topic is the output topic of the previous node in the execution order.
// Returns empty string for the consumer node (it has no input topic).
//
// Example for Filter node: "tenant-acme.pipeline-conn-123.consumer-0.output"
func GetInputTopicForNode(graph *ExecutionGraph, nodeID string) string {
	// Find the position of this node in execution order
	position := -1
	for i, id := range graph.ExecutionOrder {
		if id == nodeID {
			position = i
			break
		}
	}

	// If not found or is first node (consumer), return empty
	if position <= 0 {
		return ""
	}

	// Get the previous node's ID
	previousNodeID := graph.ExecutionOrder[position-1]

	// Return the previous node's output topic
	return GetOutputTopic(graph.TenantID, graph.ConnectionID, previousNodeID)
}

// GetOutputTopicForNode returns the NATS output topic for a node based on the execution graph.
// Returns empty string for the producer node (it has no output topic - it writes to external destination).
func GetOutputTopicForNode(graph *ExecutionGraph, nodeID string) string {
	// Producer has no output topic (writes to external destination)
	if nodeID == graph.ProducerNodeID {
		return ""
	}

	return GetOutputTopic(graph.TenantID, graph.ConnectionID, nodeID)
}

// ComputeAllTopics computes input and output topics for all nodes in the execution graph.
// Returns a map of nodeID -> TopicPair.
//
// Example output for pipeline: Consumer -> Filter -> Producer
//
//	Consumer: INPUT=""  OUTPUT="tenant.pipeline-conn.consumer.output"
//	Filter:   INPUT="tenant.pipeline-conn.consumer.output"  OUTPUT="tenant.pipeline-conn.filter.output"
//	Producer: INPUT="tenant.pipeline-conn.filter.output"  OUTPUT=""
func ComputeAllTopics(graph *ExecutionGraph) map[string]*TopicPair {
	topics := make(map[string]*TopicPair)

	for _, nodeID := range graph.ExecutionOrder {
		topics[nodeID] = &TopicPair{
			InputTopic:  GetInputTopicForNode(graph, nodeID),
			OutputTopic: GetOutputTopicForNode(graph, nodeID),
		}
	}

	return topics
}

// sanitizeTopicPart sanitizes a string for use in a NATS topic.
// Replaces spaces and special characters that are invalid in NATS subjects.
// NATS subjects cannot contain spaces, and '.' is used as delimiter.
func sanitizeTopicPart(s string) string {
	// Replace spaces with hyphens
	s = strings.ReplaceAll(s, " ", "-")

	// Replace dots (NATS delimiter) with hyphens
	s = strings.ReplaceAll(s, ".", "-")

	// Replace other potentially problematic characters
	s = strings.ReplaceAll(s, "*", "-")
	s = strings.ReplaceAll(s, ">", "-")

	// Convert to lowercase for consistency
	s = strings.ToLower(s)

	return s
}

// ValidateTopicName validates that a NATS topic name is valid.
// Returns an error if the topic name is invalid.
func ValidateTopicName(topic string) error {
	if topic == "" {
		return nil // Empty is valid (for consumer input, producer output)
	}

	// NATS subjects cannot contain spaces
	if strings.Contains(topic, " ") {
		return fmt.Errorf("topic name cannot contain spaces: %s", topic)
	}

	// Check for empty parts (consecutive dots)
	parts := strings.Split(topic, ".")
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("topic name cannot have empty parts: %s", topic)
		}
	}

	return nil
}

// BuildTopicPrefix returns the topic prefix for a connection.
// This can be used for subscribing to all topics for a connection.
// Format: {tenantID}.pipeline-{connectionID}.>
func BuildTopicPrefix(tenantID, connectionID string) string {
	return fmt.Sprintf("%s.pipeline-%s.>", sanitizeTopicPart(tenantID), sanitizeTopicPart(connectionID))
}

// BuildTenantTopicPrefix returns the topic prefix for all pipelines of a tenant.
// Format: {tenantID}.>
func BuildTenantTopicPrefix(tenantID string) string {
	return fmt.Sprintf("%s.>", sanitizeTopicPart(tenantID))
}
