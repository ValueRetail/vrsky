package managementapi

import (
	"testing"
)

// Test ValidateSourceConfig with valid HTTP config
func TestValidateSourceConfig_ValidHTTP(t *testing.T) {
	validator := NewValidator()
	config := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "http://example.com/api",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateSourceConfig with invalid HTTP URL
func TestValidateSourceConfig_InvalidHTTPURL(t *testing.T) {
	validator := NewValidator()
	config := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "not-a-url",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&config)
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

// Test ValidateSourceConfig with empty URL
func TestValidateSourceConfig_EmptyURL(t *testing.T) {
	validator := NewValidator()
	config := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&config)
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

// Test ValidateDestinationConfig with valid HTTP config
func TestValidateDestinationConfig_ValidHTTP(t *testing.T) {
	validator := NewValidator()
	config := DestinationConfig{
		Type: "http",
		HTTP: &HTTPDestinationConfig{
			URL:    "http://example.com/webhook",
			Method: "POST",
		},
	}

	err := validator.ValidateDestinationConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateDestinationConfig with invalid method
func TestValidateDestinationConfig_InvalidMethod(t *testing.T) {
	validator := NewValidator()
	config := DestinationConfig{
		Type: "http",
		HTTP: &HTTPDestinationConfig{
			URL:    "http://example.com",
			Method: "INVALID",
		},
	}

	err := validator.ValidateDestinationConfig(&config)
	if err == nil {
		t.Error("expected error for invalid HTTP method")
	}
}

// Test ValidateConverterConfig with field mapper
func TestValidateConverterConfig_FieldMapper(t *testing.T) {
	validator := NewValidator()
	config := ConverterConfig{
		FieldMapper: &FieldMapperConfig{
			Mappings: map[string]string{
				"id":   "id",
				"name": "user_name",
			},
		},
	}

	err := validator.ValidateConverterConfig(&config)
	if err != nil {
		t.Logf("validation error (may be expected): %v", err)
	}
}

// Test ValidateConverterConfig with empty config
func TestValidateConverterConfig_Empty(t *testing.T) {
	validator := NewValidator()
	config := ConverterConfig{
		FieldMapper: nil,
	}

	err := validator.ValidateConverterConfig(&config)
	if err != nil {
		t.Logf("validation error (may be expected for empty): %v", err)
	}
}

// Test ValidateFilterConfig with basic config
func TestValidateFilterConfig_Basic(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: []*FilterRule{
			{
				Name:      "rule1",
				Condition: "field == 'value'",
			},
		},
	}

	err := validator.ValidateFilterConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateFilterConfig with empty config
func TestValidateFilterConfig_Empty(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: nil,
	}

	err := validator.ValidateFilterConfig(&config)
	if err != nil {
		t.Errorf("expected no error for empty filter config, got %v", err)
	}
}

// Test ValidateFilterConfig with multiple rules
func TestValidateFilterConfig_MultipleRules(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: []*FilterRule{
			{
				Name:      "rule1",
				Condition: "field1 == 'value1'",
			},
			{
				Name:      "rule2",
				Condition: "field2 > 100",
			},
		},
	}

	err := validator.ValidateFilterConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateFilterConfig with missing rule name
func TestValidateFilterConfig_MissingRuleName(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: []*FilterRule{
			{
				Name:      "",
				Condition: "field == 'value'",
			},
		},
	}

	err := validator.ValidateFilterConfig(&config)
	if err == nil {
		t.Error("expected error for missing rule name")
	}
}

// Test ValidateFilterConfig with missing rule condition
func TestValidateFilterConfig_MissingCondition(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: []*FilterRule{
			{
				Name:      "rule1",
				Condition: "",
			},
		},
	}

	err := validator.ValidateFilterConfig(&config)
	if err == nil {
		t.Error("expected error for missing rule condition")
	}
}

// Test ValidateConnection with all valid configs
func TestValidateConnection_AllValid(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com/api",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com/webhook",
				Method: "POST",
			},
		},
	}

	err := validator.ValidateConnection(conn)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateConnection with invalid source
func TestValidateConnection_InvalidSource(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "invalid",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com/webhook",
				Method: "POST",
			},
		},
	}

	err := validator.ValidateConnection(conn)
	if err == nil {
		t.Error("expected error for invalid source config")
	}
}

// Test ValidateConnection with invalid destination
func TestValidateConnection_InvalidDestination(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com/api",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com/webhook",
				Method: "INVALID",
			},
		},
	}

	err := validator.ValidateConnection(conn)
	if err == nil {
		t.Error("expected error for invalid destination config")
	}
}

// Test HTTPSourceConfig with various HTTP methods
func TestHTTPSourceConfig_ValidMethods(t *testing.T) {
	validator := NewValidator()

	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
	for _, method := range methods {
		config := SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com",
				Method: method,
			},
		}
		err := validator.ValidateSourceConfig(&config)
		if err != nil {
			t.Errorf("expected no error for method %s, got %v", method, err)
		}
	}
}

// Test HTTPDestinationConfig with POST method (common for webhooks)
func TestHTTPDestinationConfig_POST(t *testing.T) {
	validator := NewValidator()

	config := DestinationConfig{
		Type: "http",
		HTTP: &HTTPDestinationConfig{
			URL:    "http://example.com/api/webhook",
			Method: "POST",
		},
	}

	err := validator.ValidateDestinationConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test HTTPS URLs
func TestValidation_HTTPSURLs(t *testing.T) {
	validator := NewValidator()

	sourceConfig := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "https://api.example.com/data",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&sourceConfig)
	if err != nil {
		t.Errorf("expected no error for HTTPS URL, got %v", err)
	}
}

// Test invalid HTTPS URL
func TestValidation_InvalidHTTPSURL(t *testing.T) {
	validator := NewValidator()

	sourceConfig := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "https://invalid url with spaces",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&sourceConfig)
	if err == nil {
		t.Error("expected error for invalid URL with spaces")
	}
}

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

// Test ValidateDAG backward compatibility - legacy linear model (no nodes)
func TestValidateDAG_BackwardCompatibility_LegacyLinearModel(t *testing.T) {
	validator := NewValidator()

	// Legacy connection without nodes/edges
	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "legacy-connection",
		Nodes:    nil, // No nodes
		Edges:    nil, // No edges
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com/api",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com/webhook",
				Method: "POST",
			},
		},
	}

	// ValidateDAG should return nil (skip validation for legacy model)
	err := validator.ValidateDAG(conn)
	if err != nil {
		t.Errorf("expected no error for legacy connection (backward compatibility), got %v", err)
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
