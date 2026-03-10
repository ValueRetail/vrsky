// Package orchestrator - Graph building and topological sort for pipeline execution
package orchestrator

import (
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
)

// BuildExecutionGraph builds and validates an execution graph from a connection.
// It validates the DAG structure and computes the topological execution order.
// The consumer is always first in the execution order, and the producer is always last.
//
// Returns an error if:
// - The connection has no nodes
// - The DAG validation fails (cycles, missing consumer/producer, etc.)
// - Topological sort fails
func BuildExecutionGraph(conn *managementapi.Connection, validator *managementapi.Validator) (*ExecutionGraph, error) {
	if conn == nil {
		return nil, NewOrchestratorError(ErrCodeInvalidGraph, "connection is nil", nil)
	}

	if len(conn.Nodes) == 0 {
		return nil, NewOrchestratorError(ErrCodeInvalidGraph, "connection has no nodes", nil)
	}

	// Validate the DAG structure using the existing validator
	if validator != nil {
		if err := validator.ValidateDAG(conn); err != nil {
			return nil, NewOrchestratorError(ErrCodeInvalidGraph, err.Error(), nil)
		}
	}

	// Build node map
	nodeMap := make(map[string]*managementapi.Node)
	var consumerID, producerID string

	for _, node := range conn.Nodes {
		if node == nil {
			continue
		}
		nodeMap[node.ID] = node

		switch node.Type {
		case "consumer":
			consumerID = node.ID
		case "producer":
			producerID = node.ID
		}
	}

	// Compute execution order via topological sort
	executionOrder, err := computeTopologicalOrder(conn.Nodes, conn.Edges, consumerID, producerID)
	if err != nil {
		return nil, err
	}

	return &ExecutionGraph{
		Nodes:          nodeMap,
		Edges:          conn.Edges,
		ExecutionOrder: executionOrder,
		ConsumerNodeID: consumerID,
		ProducerNodeID: producerID,
		TenantID:       conn.TenantID,
		ConnectionID:   conn.ID,
	}, nil
}

// computeTopologicalOrder computes the topological order of nodes using DFS.
// Ensures consumer is first and producer is last in the execution order.
func computeTopologicalOrder(nodes []*managementapi.Node, edges []*managementapi.Edge, consumerID, producerID string) ([]string, error) {
	if consumerID == "" || producerID == "" {
		return nil, NewOrchestratorError(ErrCodeTopologicalSort, "consumer or producer not found", nil)
	}

	// Build adjacency list (node -> list of nodes it points to)
	adj := make(map[string][]string)
	for _, edge := range edges {
		if edge != nil {
			adj[edge.Source] = append(adj[edge.Source], edge.Target)
		}
	}

	// Track visited nodes and result stack
	visited := make(map[string]bool)
	inStack := make(map[string]bool) // For cycle detection
	var result []string

	// DFS function for topological sort
	var dfs func(nodeID string) error
	dfs = func(nodeID string) error {
		if inStack[nodeID] {
			return NewOrchestratorError(ErrCodeTopologicalSort, fmt.Sprintf("cycle detected at node %s", nodeID), nil)
		}
		if visited[nodeID] {
			return nil
		}

		inStack[nodeID] = true
		visited[nodeID] = true

		// Visit all neighbors
		for _, neighbor := range adj[nodeID] {
			if err := dfs(neighbor); err != nil {
				return err
			}
		}

		inStack[nodeID] = false

		// Add to result (we'll reverse later)
		result = append(result, nodeID)
		return nil
	}

	// Start DFS from consumer to ensure we only include reachable nodes
	if err := dfs(consumerID); err != nil {
		return nil, err
	}

	// Reverse the result to get correct topological order
	// (DFS gives us reverse topological order)
	reverse(result)

	// Verify consumer is first
	if len(result) > 0 && result[0] != consumerID {
		// Move consumer to front if not already there
		result = moveToFront(result, consumerID)
	}

	// Verify producer is last
	if len(result) > 0 && result[len(result)-1] != producerID {
		// Move producer to end if not already there
		result = moveToEnd(result, producerID)
	}

	// Validate all nodes are accounted for
	nodeSet := make(map[string]bool)
	for _, node := range nodes {
		if node != nil {
			nodeSet[node.ID] = true
		}
	}

	// Check for unreachable nodes (orphaned nodes should have been caught by validator)
	for nodeID := range nodeSet {
		found := false
		for _, id := range result {
			if id == nodeID {
				found = true
				break
			}
		}
		if !found {
			// Node not in result means it's not reachable from consumer
			// This should already be caught by validator, but double-check
			return nil, NewOrchestratorError(ErrCodeTopologicalSort,
				fmt.Sprintf("node %s is not reachable from consumer", nodeID),
				map[string]string{"nodeID": nodeID})
		}
	}

	return result, nil
}

// reverse reverses a slice in place
func reverse(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// moveToFront moves an element to the front of the slice
func moveToFront(s []string, elem string) []string {
	result := []string{elem}
	for _, e := range s {
		if e != elem {
			result = append(result, e)
		}
	}
	return result
}

// moveToEnd moves an element to the end of the slice
func moveToEnd(s []string, elem string) []string {
	var result []string
	for _, e := range s {
		if e != elem {
			result = append(result, e)
		}
	}
	result = append(result, elem)
	return result
}

// GetNodeByID returns a node from the execution graph by ID.
func (g *ExecutionGraph) GetNodeByID(nodeID string) (*managementapi.Node, error) {
	node, ok := g.Nodes[nodeID]
	if !ok {
		return nil, NewOrchestratorError(ErrCodeNodeNotFound,
			fmt.Sprintf("node %s not found in graph", nodeID),
			map[string]string{"nodeID": nodeID})
	}
	return node, nil
}

// GetPreviousNode returns the node that precedes the given node in execution order.
// Returns nil for the consumer (first node).
func (g *ExecutionGraph) GetPreviousNode(nodeID string) *managementapi.Node {
	for i, id := range g.ExecutionOrder {
		if id == nodeID && i > 0 {
			prevID := g.ExecutionOrder[i-1]
			return g.Nodes[prevID]
		}
	}
	return nil
}

// GetNextNode returns the node that follows the given node in execution order.
// Returns nil for the producer (last node).
func (g *ExecutionGraph) GetNextNode(nodeID string) *managementapi.Node {
	for i, id := range g.ExecutionOrder {
		if id == nodeID && i < len(g.ExecutionOrder)-1 {
			nextID := g.ExecutionOrder[i+1]
			return g.Nodes[nextID]
		}
	}
	return nil
}

// GetNodePosition returns the position of a node in the execution order.
// Returns -1 if the node is not found.
func (g *ExecutionGraph) GetNodePosition(nodeID string) int {
	for i, id := range g.ExecutionOrder {
		if id == nodeID {
			return i
		}
	}
	return -1
}

// IsConsumer returns true if the given node is the consumer.
func (g *ExecutionGraph) IsConsumer(nodeID string) bool {
	return nodeID == g.ConsumerNodeID
}

// IsProducer returns true if the given node is the producer.
func (g *ExecutionGraph) IsProducer(nodeID string) bool {
	return nodeID == g.ProducerNodeID
}

// NodeCount returns the number of nodes in the execution graph.
func (g *ExecutionGraph) NodeCount() int {
	return len(g.ExecutionOrder)
}

// GetNodesByType returns all nodes of a given type.
func (g *ExecutionGraph) GetNodesByType(nodeType string) []*managementapi.Node {
	var nodes []*managementapi.Node
	for _, node := range g.Nodes {
		if node.Type == nodeType {
			nodes = append(nodes, node)
		}
	}
	return nodes
}
