package managementapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Validator provides configuration validation
type Validator struct {
	schemaCompiler *jsonschema.Compiler
}

// NewValidator creates a new validator
func NewValidator() *Validator {
	return &Validator{
		schemaCompiler: jsonschema.NewCompiler(),
	}
}

// =============================================================================
// DAG Validation for Graph-Based Pipeline Model
// =============================================================================

// ValidateDAG validates the graph-based connection model (nodes and edges).
// Returns a DAGValidationError with all validation errors found, or nil if valid.
// This validates:
// - Exactly 1 consumer node
// - Exactly 1 producer node
// - All edges reference existing nodes
// - No circular dependencies (cycles)
// - Consumer has outgoing edges
// - Producer is reachable from consumer
// - No orphaned nodes (all nodes on path from consumer to producer)
func (v *Validator) ValidateDAG(conn *Connection) error {
	if conn == nil {
		return &BadRequestError{Message: "connection cannot be nil"}
	}

	// If no nodes, this is a legacy connection - skip DAG validation
	if len(conn.Nodes) == 0 {
		return nil
	}

	var errors []string

	// Build node ID set for quick lookup
	nodeIDs := make(map[string]*Node)
	for _, node := range conn.Nodes {
		if node != nil {
			nodeIDs[node.ID] = node
		}
	}

	// 1. Validate node counts (at least 1 consumer, 1 producer)
	consumerIDs, producerIDs, countErrors := v.validateNodeCounts(conn.Nodes)
	errors = append(errors, countErrors...)

	// 2. Validate all edges reference existing nodes
	edgeErrors := v.validateEdgesReference(nodeIDs, conn.Edges)
	errors = append(errors, edgeErrors...)

	// If we have edge reference errors or missing nodes, skip graph traversal checks
	if len(edgeErrors) > 0 || len(consumerIDs) == 0 || len(producerIDs) == 0 {
		if len(errors) > 0 {
			return &DAGValidationError{Errors: errors}
		}
		return nil
	}

	// 3. Detect cycles
	if v.hasCycle(conn.Nodes, conn.Edges) {
		errors = append(errors, (&CircularDependencyError{
			Message: "circular dependency detected: pipeline contains a cycle",
		}).Error())
	}

	// 4. Check each consumer has outgoing edges
	for _, cID := range consumerIDs {
		outgoing := v.getOutgoingEdges(cID, conn.Edges)
		if len(outgoing) == 0 {
			errors = append(errors, (&ConsumerIsolatedError{
				ConsumerID: cID,
			}).Error())
		}
	}

	// 5. Check each producer is reachable from at least one consumer
	for _, pID := range producerIDs {
		reachable := false
		for _, cID := range consumerIDs {
			if v.isReachable(cID, pID, conn.Edges) {
				reachable = true
				break
			}
		}
		if !reachable {
			errors = append(errors, (&ProducerUnreachableError{
				ConsumerID: consumerIDs[0],
				ProducerID: pID,
			}).Error())
		}
	}

	// 6. Find orphaned nodes (nodes not on any consumer→producer path)
	reachableFromConsumers := make(map[string]bool)
	for _, cID := range consumerIDs {
		visited := make(map[string]bool)
		queue := []string{cID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if visited[cur] {
				continue
			}
			visited[cur] = true
			reachableFromConsumers[cur] = true
			for _, e := range conn.Edges {
				if e != nil && e.Source == cur {
					queue = append(queue, e.Target)
				}
			}
		}
	}
	canReachProducers := make(map[string]bool)
	for _, pID := range producerIDs {
		visited := make(map[string]bool)
		queue := []string{pID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if visited[cur] {
				continue
			}
			visited[cur] = true
			canReachProducers[cur] = true
			for _, e := range conn.Edges {
				if e != nil && e.Target == cur {
					queue = append(queue, e.Source)
				}
			}
		}
	}
	var orphaned []string
	for _, node := range conn.Nodes {
		if node != nil && (!reachableFromConsumers[node.ID] || !canReachProducers[node.ID]) {
			orphaned = append(orphaned, node.ID)
		}
	}
	if len(orphaned) > 0 {
		errors = append(errors, (&OrphanedNodesError{
			Nodes: orphaned,
		}).Error())
	}

	// 7. Per-node config sanity that the structural checks don't cover.
	// A file producer's output path must be an absolute directory: the worker
	// rejects relative paths / filenames at runtime and would silently drop
	// every message, so we catch it here at create/deploy (#142).
	//
	// Note this is a check on a value the user HAS supplied. Whether a node is
	// configured completely enough for a connector to claim it is checked by
	// ValidateNodeConfigs on start, not here — ValidateDAG also runs on save,
	// and a half-built pipeline on the canvas must stay saveable (#212).
	for _, node := range conn.Nodes {
		if node == nil || node.Type != "producer" {
			continue
		}
		if msg := validateFileProducerPath(node); msg != "" {
			errors = append(errors, msg)
		}
	}

	if len(errors) > 0 {
		return &DAGValidationError{Errors: errors}
	}

	return nil
}

// validateFileProducerPath returns a non-empty error string if a producer node
// is a file sink with an invalid output path. An empty path is allowed (the
// worker falls back to its default mounted output dir); a non-empty path must
// be absolute and free of ".." traversal, matching the worker's runtime guard
// (#142). Non-file producers and unparseable config are ignored here.
func validateFileProducerPath(node *Node) string {
	var nc struct {
		Type string `json:"type"`
		File struct {
			Path string `json:"path"`
		} `json:"file"`
	}
	if len(node.Config) == 0 || json.Unmarshal(node.Config, &nc) != nil {
		return ""
	}
	// A file producer is identified the same way the worker does: type "file"
	// or a non-empty file.path.
	if nc.Type != "file" && nc.File.Path == "" {
		return ""
	}
	path := strings.TrimSpace(nc.File.Path)
	if path == "" {
		return "" // worker uses its default output dir
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Sprintf("node %s: file output path %q must be an absolute directory (e.g. /data/output) — a relative path or filename is rejected at runtime and messages would be dropped", node.ID, path)
	}
	if strings.Contains(path, "..") {
		return fmt.Sprintf("node %s: file output path %q must not contain '..'", node.ID, path)
	}
	return ""
}

// validateNodeCounts validates that there is at least 1 consumer and 1 producer.
// Returns the consumer IDs, producer IDs, and any errors found.
func (v *Validator) validateNodeCounts(nodes []*Node) (consumerIDs, producerIDs []string, errors []string) {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		switch node.Type {
		case "consumer", "input":
			consumerIDs = append(consumerIDs, node.ID)
		case "producer", "output":
			producerIDs = append(producerIDs, node.ID)
		}
	}

	if len(consumerIDs) == 0 {
		errors = append(errors, (&ConsumerCountError{
			Found:    0,
			Expected: 1,
		}).Error())
	}

	if len(producerIDs) == 0 {
		errors = append(errors, (&ProducerCountError{
			Found:    0,
			Expected: 1,
		}).Error())
	}

	return consumerIDs, producerIDs, errors
}

// validateEdgesReference validates that all edges reference existing nodes.
func (v *Validator) validateEdgesReference(nodeIDs map[string]*Node, edges []*Edge) []string {
	var errors []string

	for _, edge := range edges {
		if edge == nil {
			continue
		}
		if _, exists := nodeIDs[edge.Source]; !exists {
			errors = append(errors, (&InvalidEdgeError{
				EdgeID:     edge.ID,
				InvalidRef: edge.Source,
				RefType:    "source",
			}).Error())
		}
		if _, exists := nodeIDs[edge.Target]; !exists {
			errors = append(errors, (&InvalidEdgeError{
				EdgeID:     edge.ID,
				InvalidRef: edge.Target,
				RefType:    "target",
			}).Error())
		}
	}

	return errors
}

// hasCycle detects if the graph contains a cycle using DFS with 3-state coloring.
// WHITE (0) = unvisited, GRAY (1) = in progress, BLACK (2) = finished
func (v *Validator) hasCycle(nodes []*Node, edges []*Edge) bool {
	const (
		WHITE = 0
		GRAY  = 1
		BLACK = 2
	)

	// Build adjacency list
	adj := make(map[string][]string)
	for _, edge := range edges {
		if edge != nil {
			adj[edge.Source] = append(adj[edge.Source], edge.Target)
		}
	}

	color := make(map[string]int)
	for _, node := range nodes {
		if node != nil {
			color[node.ID] = WHITE
		}
	}

	var dfs func(nodeID string) bool
	dfs = func(nodeID string) bool {
		color[nodeID] = GRAY

		for _, neighbor := range adj[nodeID] {
			if color[neighbor] == GRAY {
				// Found a back edge - cycle detected
				return true
			}
			if color[neighbor] == WHITE {
				if dfs(neighbor) {
					return true
				}
			}
		}

		color[nodeID] = BLACK
		return false
	}

	// Run DFS from each unvisited node
	for _, node := range nodes {
		if node != nil && color[node.ID] == WHITE {
			if dfs(node.ID) {
				return true
			}
		}
	}

	return false
}

// isReachable checks if targetID is reachable from sourceID using BFS.
func (v *Validator) isReachable(sourceID, targetID string, edges []*Edge) bool {
	if sourceID == targetID {
		return true
	}

	// Build adjacency list
	adj := make(map[string][]string)
	for _, edge := range edges {
		if edge != nil {
			adj[edge.Source] = append(adj[edge.Source], edge.Target)
		}
	}

	// BFS
	visited := make(map[string]bool)
	queue := []string{sourceID}
	visited[sourceID] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, neighbor := range adj[current] {
			if neighbor == targetID {
				return true
			}
			if !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}

	return false
}

// getOutgoingEdges returns all edges originating from the given node.
func (v *Validator) getOutgoingEdges(nodeID string, edges []*Edge) []*Edge {
	var outgoing []*Edge
	for _, edge := range edges {
		if edge != nil && edge.Source == nodeID {
			outgoing = append(outgoing, edge)
		}
	}
	return outgoing
}
