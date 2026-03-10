package managementapi

import (
	"encoding/json"
	"testing"
)

// TestNewConnection_LegacyFormat verifies backward compatibility with the old linear pipeline model
func TestNewConnection_LegacyFormat(t *testing.T) {
	req := CreateConnectionRequest{
		Name:        "Legacy Connection",
		Description: "Testing backward compatibility",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "https://example.com/webhook",
				Method: "POST",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "file",
			File: &FileDestinationConfig{
				Path:   "/tmp/output.json",
				Format: "json",
			},
		},
	}

	conn := NewConnection("tenant-123", req)

	// Verify legacy fields are populated
	if conn.SourceConfig.Type != "http" {
		t.Errorf("Expected SourceConfig.Type = 'http', got '%s'", conn.SourceConfig.Type)
	}
	if conn.SourceConfig.HTTP == nil || conn.SourceConfig.HTTP.URL != "https://example.com/webhook" {
		t.Errorf("Expected SourceConfig.HTTP.URL = 'https://example.com/webhook'")
	}
	if conn.DestinationConfig.Type != "file" {
		t.Errorf("Expected DestinationConfig.Type = 'file', got '%s'", conn.DestinationConfig.Type)
	}

	// Verify new fields are empty (not populated for legacy format)
	if len(conn.Nodes) > 0 {
		t.Errorf("Expected Nodes to be empty for legacy format, got %d nodes", len(conn.Nodes))
	}
	if len(conn.Edges) > 0 {
		t.Errorf("Expected Edges to be empty for legacy format, got %d edges", len(conn.Edges))
	}

	// Verify basic fields
	if conn.Name != "Legacy Connection" {
		t.Errorf("Expected Name = 'Legacy Connection', got '%s'", conn.Name)
	}
	if conn.TenantID != "tenant-123" {
		t.Errorf("Expected TenantID = 'tenant-123', got '%s'", conn.TenantID)
	}
	if conn.Status != "stopped" {
		t.Errorf("Expected Status = 'stopped', got '%s'", conn.Status)
	}
}

// TestNewConnection_GraphFormat verifies the new graph-based pipeline model
func TestNewConnection_GraphFormat(t *testing.T) {
	consumerConfig, _ := json.Marshal(map[string]interface{}{
		"type": "http",
		"http": map[string]string{
			"url":    "https://example.com/webhook",
			"method": "POST",
		},
	})
	producerConfig, _ := json.Marshal(map[string]interface{}{
		"type": "file",
		"file": map[string]string{
			"path":   "/tmp/output.json",
			"format": "json",
		},
	})

	req := CreateConnectionRequest{
		Name:        "Graph Connection",
		Description: "Testing new graph-based model",
		Nodes: []*Node{
			{
				ID:      "consumer-0",
				Type:    "consumer",
				Config:  consumerConfig,
				Enabled: true,
			},
			{
				ID:      "producer-0",
				Type:    "producer",
				Config:  producerConfig,
				Enabled: true,
			},
		},
		Edges: []*Edge{
			{
				ID:     "edge-0",
				Source: "consumer-0",
				Target: "producer-0",
				Order:  0,
			},
		},
	}

	conn := NewConnection("tenant-456", req)

	// Verify new fields are populated
	if len(conn.Nodes) != 2 {
		t.Errorf("Expected 2 nodes, got %d", len(conn.Nodes))
	}
	if len(conn.Edges) != 1 {
		t.Errorf("Expected 1 edge, got %d", len(conn.Edges))
	}

	// Verify node details
	if conn.Nodes[0].ID != "consumer-0" {
		t.Errorf("Expected first node ID = 'consumer-0', got '%s'", conn.Nodes[0].ID)
	}
	if conn.Nodes[0].Type != "consumer" {
		t.Errorf("Expected first node Type = 'consumer', got '%s'", conn.Nodes[0].Type)
	}
	if conn.Nodes[1].ID != "producer-0" {
		t.Errorf("Expected second node ID = 'producer-0', got '%s'", conn.Nodes[1].ID)
	}

	// Verify edge details
	if conn.Edges[0].Source != "consumer-0" {
		t.Errorf("Expected edge Source = 'consumer-0', got '%s'", conn.Edges[0].Source)
	}
	if conn.Edges[0].Target != "producer-0" {
		t.Errorf("Expected edge Target = 'producer-0', got '%s'", conn.Edges[0].Target)
	}

	// Verify basic fields
	if conn.Name != "Graph Connection" {
		t.Errorf("Expected Name = 'Graph Connection', got '%s'", conn.Name)
	}
	if conn.TenantID != "tenant-456" {
		t.Errorf("Expected TenantID = 'tenant-456', got '%s'", conn.TenantID)
	}
}

// TestNodeJSONSerialization verifies Node structs serialize correctly
func TestNodeJSONSerialization(t *testing.T) {
	config, _ := json.Marshal(map[string]string{"key": "value"})
	node := &Node{
		ID:      "test-node",
		Type:    "filter",
		Config:  config,
		Enabled: true,
	}

	data, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("Failed to marshal Node: %v", err)
	}

	var unmarshaled Node
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal Node: %v", err)
	}

	if unmarshaled.ID != node.ID {
		t.Errorf("ID mismatch: expected '%s', got '%s'", node.ID, unmarshaled.ID)
	}
	if unmarshaled.Type != node.Type {
		t.Errorf("Type mismatch: expected '%s', got '%s'", node.Type, unmarshaled.Type)
	}
	if unmarshaled.Enabled != node.Enabled {
		t.Errorf("Enabled mismatch: expected %v, got %v", node.Enabled, unmarshaled.Enabled)
	}
}

// TestEdgeJSONSerialization verifies Edge structs serialize correctly
func TestEdgeJSONSerialization(t *testing.T) {
	edge := &Edge{
		ID:     "edge-1",
		Source: "node-a",
		Target: "node-b",
		Order:  1,
	}

	data, err := json.Marshal(edge)
	if err != nil {
		t.Fatalf("Failed to marshal Edge: %v", err)
	}

	var unmarshaled Edge
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal Edge: %v", err)
	}

	if unmarshaled.ID != edge.ID {
		t.Errorf("ID mismatch: expected '%s', got '%s'", edge.ID, unmarshaled.ID)
	}
	if unmarshaled.Source != edge.Source {
		t.Errorf("Source mismatch: expected '%s', got '%s'", edge.Source, unmarshaled.Source)
	}
	if unmarshaled.Target != edge.Target {
		t.Errorf("Target mismatch: expected '%s', got '%s'", edge.Target, unmarshaled.Target)
	}
	if unmarshaled.Order != edge.Order {
		t.Errorf("Order mismatch: expected %d, got %d", edge.Order, unmarshaled.Order)
	}
}

// TestConnectionWithBothFormats verifies that Connection struct handles mixed data correctly
func TestConnectionWithBothFormats(t *testing.T) {
	config, _ := json.Marshal(map[string]string{"key": "value"})
	conn := &Connection{
		ID:          "test-id",
		TenantID:    "tenant-1",
		Name:        "Mixed Connection",
		Description: "Has both old and new fields",
		// New graph-based fields
		Nodes: []*Node{
			{ID: "node-1", Type: "consumer", Config: config, Enabled: true},
		},
		Edges: []*Edge{
			{ID: "edge-1", Source: "node-1", Target: "node-2", Order: 0},
		},
		// Legacy fields (deprecated but still present)
		SourceConfig: SourceConfig{Type: "http"},
		Status:       "stopped",
	}

	// Verify all fields are accessible
	if len(conn.Nodes) != 1 {
		t.Errorf("Expected 1 node, got %d", len(conn.Nodes))
	}
	if len(conn.Edges) != 1 {
		t.Errorf("Expected 1 edge, got %d", len(conn.Edges))
	}
	if conn.SourceConfig.Type != "http" {
		t.Errorf("Expected SourceConfig.Type = 'http', got '%s'", conn.SourceConfig.Type)
	}

	// Test JSON serialization/deserialization
	data, err := json.Marshal(conn)
	if err != nil {
		t.Fatalf("Failed to marshal Connection: %v", err)
	}

	var unmarshaled Connection
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal Connection: %v", err)
	}

	if len(unmarshaled.Nodes) != 1 {
		t.Errorf("Unmarshaled Nodes count mismatch: expected 1, got %d", len(unmarshaled.Nodes))
	}
	if len(unmarshaled.Edges) != 1 {
		t.Errorf("Unmarshaled Edges count mismatch: expected 1, got %d", len(unmarshaled.Edges))
	}
}

// TestCreateConnectionRequest_BothFormats verifies CreateConnectionRequest handles both formats
func TestCreateConnectionRequest_BothFormats(t *testing.T) {
	// Test legacy format JSON
	legacyJSON := `{
		"name": "Legacy Test",
		"description": "Legacy format",
		"source_config": {"type": "http"},
		"destination_config": {"type": "file"}
	}`

	var legacyReq CreateConnectionRequest
	if err := json.Unmarshal([]byte(legacyJSON), &legacyReq); err != nil {
		t.Fatalf("Failed to unmarshal legacy request: %v", err)
	}

	if legacyReq.Name != "Legacy Test" {
		t.Errorf("Expected Name = 'Legacy Test', got '%s'", legacyReq.Name)
	}
	if legacyReq.SourceConfig.Type != "http" {
		t.Errorf("Expected SourceConfig.Type = 'http', got '%s'", legacyReq.SourceConfig.Type)
	}
	if len(legacyReq.Nodes) != 0 {
		t.Errorf("Expected empty Nodes for legacy request, got %d", len(legacyReq.Nodes))
	}

	// Test new format JSON
	newJSON := `{
		"name": "Graph Test",
		"description": "New format",
		"nodes": [
			{"id": "consumer-0", "type": "consumer", "config": {}, "enabled": true}
		],
		"edges": [
			{"id": "edge-0", "source": "consumer-0", "target": "producer-0", "order": 0}
		]
	}`

	var newReq CreateConnectionRequest
	if err := json.Unmarshal([]byte(newJSON), &newReq); err != nil {
		t.Fatalf("Failed to unmarshal new format request: %v", err)
	}

	if newReq.Name != "Graph Test" {
		t.Errorf("Expected Name = 'Graph Test', got '%s'", newReq.Name)
	}
	if len(newReq.Nodes) != 1 {
		t.Errorf("Expected 1 node, got %d", len(newReq.Nodes))
	}
	if len(newReq.Edges) != 1 {
		t.Errorf("Expected 1 edge, got %d", len(newReq.Edges))
	}
}
