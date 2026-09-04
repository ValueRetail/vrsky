package managementapi

import (
	"encoding/json"
	"testing"
)

// =============================================================================
// DAG Validation Tests (Phase 1b)
// =============================================================================

// Test ValidateDAG with valid consumer → producer pipeline
func TestValidateDAG_ValidConsumerToProducer(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "simple-pipeline",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "producer-0", Order: 0},
		},
	}

	err := validator.ValidateDAG(conn)
	if err != nil {
		t.Errorf("expected no error for valid pipeline, got %v", err)
	}
}

// #142: a file producer with a relative path / filename must be rejected at
// validation time (the worker would otherwise silently drop messages).
func TestValidateDAG_FileProducerPath(t *testing.T) {
	validator := NewValidator()

	mk := func(cfg string) *Connection {
		return &Connection{
			ID: "c", TenantID: "t", Name: "n",
			Nodes: []*Node{
				{ID: "consumer-0", Type: "consumer", Enabled: true},
				{ID: "producer-0", Type: "producer", Enabled: true, Config: json.RawMessage(cfg)},
			},
			Edges: []*Edge{{ID: "e0", Source: "consumer-0", Target: "producer-0", Order: 0}},
		}
	}

	cases := []struct {
		name      string
		config    string
		wantError bool
	}{
		{"absolute path ok", `{"type":"file","file":{"path":"/data/output"}}`, false},
		{"empty path ok (worker default)", `{"type":"file","file":{"path":""}}`, false},
		{"relative filename rejected", `{"type":"file","file":{"path":"customers_out.json"}}`, true},
		{"relative dir rejected", `{"type":"file","file":{"path":"output/sub"}}`, true},
		{"traversal rejected", `{"type":"file","file":{"path":"/data/../etc"}}`, true},
		{"non-file producer ignored", `{"type":"http","http":{"url":"https://x"}}`, false},
		{"no config ignored", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validator.ValidateDAG(mk(tc.config))
			if tc.wantError && err == nil {
				t.Fatalf("expected validation error for config %s", tc.config)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("expected no error for config %s, got %v", tc.config, err)
			}
		})
	}
}

// Test ValidateDAG with valid consumer → filter → producer pipeline
func TestValidateDAG_ValidWithFilter(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "pipeline-with-filter",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "filter-0", Type: "filter", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "filter-0", Order: 0},
			{ID: "edge-1", Source: "filter-0", Target: "producer-0", Order: 1},
		},
	}

	err := validator.ValidateDAG(conn)
	if err != nil {
		t.Errorf("expected no error for valid pipeline with filter, got %v", err)
	}
}

// Test ValidateDAG with valid consumer → filter → converter → producer pipeline
func TestValidateDAG_ValidWithFilterAndConverter(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "full-pipeline",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "filter-0", Type: "filter", Enabled: true},
			{ID: "converter-0", Type: "converter", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "filter-0", Order: 0},
			{ID: "edge-1", Source: "filter-0", Target: "converter-0", Order: 1},
			{ID: "edge-2", Source: "converter-0", Target: "producer-0", Order: 2},
		},
	}

	err := validator.ValidateDAG(conn)
	if err != nil {
		t.Errorf("expected no error for full valid pipeline, got %v", err)
	}
}

// Test ValidateDAG with no consumer
func TestValidateDAG_ErrorNoConsumer(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "no-consumer",
		Nodes: []*Node{
			{ID: "filter-0", Type: "filter", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "filter-0", Target: "producer-0", Order: 0},
		},
	}

	err := validator.ValidateDAG(conn)
	if err == nil {
		t.Error("expected error for missing consumer")
	}

	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Errorf("expected DAGValidationError, got %T", err)
		return
	}

	found := false
	for _, e := range dagErr.Errors {
		if contains(e, "no consumer") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about missing consumer, got %v", dagErr.Errors)
	}
}

// Test ValidateDAG with no producer
func TestValidateDAG_ErrorNoProducer(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "no-producer",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "filter-0", Type: "filter", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "filter-0", Order: 0},
		},
	}

	err := validator.ValidateDAG(conn)
	if err == nil {
		t.Error("expected error for missing producer")
	}

	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Errorf("expected DAGValidationError, got %T", err)
		return
	}

	found := false
	for _, e := range dagErr.Errors {
		if contains(e, "no producer") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about missing producer, got %v", dagErr.Errors)
	}
}

// Test ValidateDAG with multiple consumers
func TestValidateDAG_AllowMultipleConsumers(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "multiple-consumers",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "consumer-1", Type: "consumer", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "producer-0", Order: 0},
			{ID: "edge-1", Source: "consumer-1", Target: "producer-0", Order: 1},
		},
	}

	err := validator.ValidateDAG(conn)
	if err != nil {
		t.Errorf("expected no error for multiple consumers, got %v", err)
	}
}

// Test ValidateDAG allows multiple producers
func TestValidateDAG_AllowMultipleProducers(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "multiple-producers",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
			{ID: "producer-1", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "producer-0", Order: 0},
			{ID: "edge-1", Source: "consumer-0", Target: "producer-1", Order: 1},
		},
	}

	err := validator.ValidateDAG(conn)
	if err != nil {
		t.Errorf("expected no error for multiple producers, got %v", err)
	}
}

// Test ValidateDAG with cycle detected (A → B → C → A)
func TestValidateDAG_ErrorCycleDetected(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "cyclic-pipeline",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "filter-0", Type: "filter", Enabled: true},
			{ID: "filter-1", Type: "filter", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "filter-0", Order: 0},
			{ID: "edge-1", Source: "filter-0", Target: "filter-1", Order: 1},
			{ID: "edge-2", Source: "filter-1", Target: "filter-0", Order: 2}, // Cycle!
			{ID: "edge-3", Source: "filter-1", Target: "producer-0", Order: 3},
		},
	}

	err := validator.ValidateDAG(conn)
	if err == nil {
		t.Error("expected error for cycle detected")
	}

	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Errorf("expected DAGValidationError, got %T", err)
		return
	}

	found := false
	for _, e := range dagErr.Errors {
		if contains(e, "circular") || contains(e, "cycle") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about circular dependency, got %v", dagErr.Errors)
	}
}

// Test ValidateDAG with isolated consumer (no outgoing edges)
func TestValidateDAG_ErrorConsumerIsolated(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "isolated-consumer",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			// No edges - consumer is isolated
		},
	}

	err := validator.ValidateDAG(conn)
	if err == nil {
		t.Error("expected error for isolated consumer")
	}

	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Errorf("expected DAGValidationError, got %T", err)
		return
	}

	found := false
	for _, e := range dagErr.Errors {
		if contains(e, "isolated") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about isolated consumer, got %v", dagErr.Errors)
	}
}

// Test ValidateDAG with producer unreachable from consumer
func TestValidateDAG_ErrorProducerUnreachable(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "unreachable-producer",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "filter-0", Type: "filter", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "filter-0", Order: 0},
			// No edge from filter to producer!
		},
	}

	err := validator.ValidateDAG(conn)
	if err == nil {
		t.Error("expected error for unreachable producer")
	}

	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Errorf("expected DAGValidationError, got %T", err)
		return
	}

	found := false
	for _, e := range dagErr.Errors {
		if contains(e, "not reachable") || contains(e, "unreachable") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about unreachable producer, got %v", dagErr.Errors)
	}
}

// Test ValidateDAG with orphaned node (not on path from consumer to producer)
func TestValidateDAG_ErrorOrphanedNode(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "orphaned-node",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "filter-0", Type: "filter", Enabled: true}, // This one is orphaned
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "producer-0", Order: 0},
			// filter-0 is not connected to anything!
		},
	}

	err := validator.ValidateDAG(conn)
	if err == nil {
		t.Error("expected error for orphaned node")
	}

	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Errorf("expected DAGValidationError, got %T", err)
		return
	}

	found := false
	for _, e := range dagErr.Errors {
		if contains(e, "orphaned") && contains(e, "filter-0") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about orphaned filter-0, got %v", dagErr.Errors)
	}
}

// Test ValidateDAG with invalid edge (references non-existent node)
func TestValidateDAG_ErrorInvalidEdge(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "invalid-edge",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "nonexistent-node", Order: 0}, // Invalid target
		},
	}

	err := validator.ValidateDAG(conn)
	if err == nil {
		t.Error("expected error for invalid edge")
	}

	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Errorf("expected DAGValidationError, got %T", err)
		return
	}

	found := false
	for _, e := range dagErr.Errors {
		if contains(e, "invalid") && contains(e, "nonexistent-node") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about invalid edge target, got %v", dagErr.Errors)
	}
}

// A connection with no nodes yet is not an invalid pipeline, just an empty one
// — it is what the UI saves before anything is dragged onto the canvas. Failing
// it here would make a new connection unsaveable.
func TestValidateDAG_EmptyConnectionIsSaveable(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "brand-new-connection",
		Nodes:    nil,
		Edges:    nil,
	}

	if err := validator.ValidateDAG(conn); err != nil {
		t.Errorf("expected an empty connection to pass DAG validation, got %v", err)
	}
}

// Test ValidateDAG with disconnected graph (two separate components)
func TestValidateDAG_ErrorDisconnectedGraph(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "disconnected-graph",
		Nodes: []*Node{
			{ID: "consumer-0", Type: "consumer", Enabled: true},
			{ID: "filter-0", Type: "filter", Enabled: true},
			{ID: "filter-1", Type: "filter", Enabled: true}, // Disconnected from main path
			{ID: "producer-0", Type: "producer", Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-0", Source: "consumer-0", Target: "filter-0", Order: 0},
			{ID: "edge-1", Source: "filter-0", Target: "producer-0", Order: 1},
			// filter-1 is completely disconnected
		},
	}

	err := validator.ValidateDAG(conn)
	if err == nil {
		t.Error("expected error for disconnected graph (orphaned node)")
	}

	dagErr, ok := err.(*DAGValidationError)
	if !ok {
		t.Errorf("expected DAGValidationError, got %T", err)
		return
	}

	found := false
	for _, e := range dagErr.Errors {
		if contains(e, "orphaned") && contains(e, "filter-1") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error about orphaned filter-1, got %v", dagErr.Errors)
	}
}

// Helper function to check if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && containsIgnoreCase(s, substr)))
}

func containsIgnoreCase(s, substr string) bool {
	s = toLowerCase(s)
	substr = toLowerCase(substr)
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toLowerCase(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
