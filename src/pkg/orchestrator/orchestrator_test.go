package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// =============================================================================
// Test Helpers
// =============================================================================

// createTestConnection creates a test connection with given nodes and edges
func createTestConnection(tenantID, connID string, nodes []*managementapi.Node, edges []*managementapi.Edge) *managementapi.Connection {
	return &managementapi.Connection{
		ID:       connID,
		TenantID: tenantID,
		Name:     "Test Connection",
		Nodes:    nodes,
		Edges:    edges,
		Status:   "stopped",
	}
}

// createNode creates a test node with given parameters
func createNode(id, nodeType string, config map[string]interface{}) *managementapi.Node {
	configJSON, _ := json.Marshal(config)
	return &managementapi.Node{
		ID:      id,
		Type:    nodeType,
		Config:  configJSON,
		Enabled: true,
	}
}

// createEdge creates a test edge
func createEdge(id, source, target string, order int) *managementapi.Edge {
	return &managementapi.Edge{
		ID:     id,
		Source: source,
		Target: target,
		Order:  order,
	}
}

func TestIsValidNodeType(t *testing.T) {
	tests := []struct {
		nodeType string
		expected bool
	}{
		{"consumer", true},
		{"filter", true},
		{"converter", true},
		{"producer", true},
		{"invalid", false},
		{"", false},
		{"CONSUMER", false}, // Case sensitive
	}

	for _, tt := range tests {
		t.Run(tt.nodeType, func(t *testing.T) {
			result := IsValidNodeType(tt.nodeType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	assert.Equal(t, "vrsky", config.Namespace)
	assert.Equal(t, "nats://nats:4222", config.NATSURLs)
}

func TestOrchestratorError(t *testing.T) {
	err := NewOrchestratorError(ErrCodeInvalidGraph, "test error", map[string]string{"key": "value"})

	assert.Equal(t, ErrCodeInvalidGraph, err.Code)
	assert.Equal(t, "test error", err.Message)
	assert.Contains(t, err.Error(), "INVALID_GRAPH")
	assert.Contains(t, err.Error(), "test error")
	assert.Contains(t, err.Error(), "key")
}

// =============================================================================
// Graph Building Tests
// =============================================================================

func TestBuildExecutionGraph_ValidSimplePipeline(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)

	require.NoError(t, err)
	assert.NotNil(t, graph)
	assert.Equal(t, "consumer-0", graph.ConsumerNodeID)
	assert.Equal(t, "producer-0", graph.ProducerNodeID)
	assert.Equal(t, []string{"consumer-0", "producer-0"}, graph.ExecutionOrder)
	assert.Equal(t, "tenant-acme", graph.TenantID)
	assert.Equal(t, "conn-123", graph.ConnectionID)
}

func TestBuildExecutionGraph_ValidWithFilter(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("filter-1", "filter", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "filter-1", 0),
		createEdge("edge-1", "filter-1", "producer-0", 1),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)

	require.NoError(t, err)
	assert.NotNil(t, graph)
	assert.Equal(t, []string{"consumer-0", "filter-1", "producer-0"}, graph.ExecutionOrder)
}

func TestBuildExecutionGraph_ValidWithFilterAndConverter(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("filter-1", "filter", nil),
		createNode("converter-2", "converter", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "filter-1", 0),
		createEdge("edge-1", "filter-1", "converter-2", 1),
		createEdge("edge-2", "converter-2", "producer-0", 2),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)

	require.NoError(t, err)
	assert.NotNil(t, graph)
	assert.Equal(t, "consumer-0", graph.ExecutionOrder[0])
	assert.Equal(t, "producer-0", graph.ExecutionOrder[len(graph.ExecutionOrder)-1])
	assert.Len(t, graph.ExecutionOrder, 4)
}

func TestBuildExecutionGraph_NilConnection(t *testing.T) {
	graph, err := BuildExecutionGraph(nil, nil)

	assert.Error(t, err)
	assert.Nil(t, graph)
	assert.Contains(t, err.Error(), "nil")
}

func TestBuildExecutionGraph_NoNodes(t *testing.T) {
	conn := createTestConnection("tenant-acme", "conn-123", nil, nil)

	graph, err := BuildExecutionGraph(conn, nil)

	assert.Error(t, err)
	assert.Nil(t, graph)
	assert.Contains(t, err.Error(), "no nodes")
}

// An unrecognised node type has no standing service that would claim it, so the
// connection would start "successfully" and then silently do nothing. The graph
// build rejects it — this check moved here from the retired per-connection
// deployment builder (ADR 0004), where it was the last validation left.
func TestBuildExecutionGraph_InvalidNodeType(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("mystery-1", "transmogrifier", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "mystery-1", 0),
		createEdge("edge-1", "mystery-1", "producer-0", 1),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)

	require.Error(t, err)
	assert.Nil(t, graph)
	assert.Contains(t, err.Error(), "INVALID_NODE_TYPE")
	assert.Contains(t, err.Error(), "transmogrifier")
}

func TestBuildExecutionGraph_NoConsumer(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("filter-1", "filter", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "filter-1", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)

	assert.Error(t, err)
	assert.Nil(t, graph)
}

func TestBuildExecutionGraph_NoProducer(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("filter-1", "filter", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "filter-1", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)

	assert.Error(t, err)
	assert.Nil(t, graph)
}

func TestExecutionGraph_GetNodeByID(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)
	require.NoError(t, err)

	node, err := graph.GetNodeByID("consumer-0")
	assert.NoError(t, err)
	assert.Equal(t, "consumer-0", node.ID)
	assert.Equal(t, "consumer", node.Type)

	_, err = graph.GetNodeByID("nonexistent")
	assert.Error(t, err)
}

func TestExecutionGraph_IsConsumerProducer(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("filter-1", "filter", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "filter-1", 0),
		createEdge("edge-1", "filter-1", "producer-0", 1),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)
	require.NoError(t, err)

	assert.True(t, graph.IsConsumer("consumer-0"))
	assert.False(t, graph.IsConsumer("filter-1"))
	assert.False(t, graph.IsConsumer("producer-0"))

	assert.False(t, graph.IsProducer("consumer-0"))
	assert.False(t, graph.IsProducer("filter-1"))
	assert.True(t, graph.IsProducer("producer-0"))
}

func TestExecutionGraph_GetPreviousNextNode(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("filter-1", "filter", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "filter-1", 0),
		createEdge("edge-1", "filter-1", "producer-0", 1),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	graph, err := BuildExecutionGraph(conn, nil)
	require.NoError(t, err)

	// Consumer has no previous
	assert.Nil(t, graph.GetPreviousNode("consumer-0"))
	assert.Equal(t, "filter-1", graph.GetNextNode("consumer-0").ID)

	// Filter is in the middle
	assert.Equal(t, "consumer-0", graph.GetPreviousNode("filter-1").ID)
	assert.Equal(t, "producer-0", graph.GetNextNode("filter-1").ID)

	// Producer has no next
	assert.Equal(t, "filter-1", graph.GetPreviousNode("producer-0").ID)
	assert.Nil(t, graph.GetNextNode("producer-0"))
}

// A connection created before #201/#205 has leftover no-op worker Deployments.
// Starting it again must sweep them away rather than leave them running.
func TestStartConnection_PrunesLegacyWorkers(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{createEdge("edge-0", "consumer-0", "producer-0", 0)}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	config := DefaultConfig()
	legacy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildDeploymentName("conn-123", "consumer-0"),
			Namespace: config.Namespace,
			Labels:    buildLabels("conn-123", "consumer-0", "consumer", "tenant-acme"),
		},
	}
	legacyHPA := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      legacy.Name,
			Namespace: config.Namespace,
			Labels:    legacy.Labels,
		},
	}
	// A worker belonging to a DIFFERENT connection must survive the prune.
	other := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildDeploymentName("conn-999", "consumer-0"),
			Namespace: config.Namespace,
			Labels:    buildLabels("conn-999", "consumer-0", "consumer", "tenant-acme"),
		},
	}
	k8sClient := fake.NewSimpleClientset(legacy, legacyHPA, other)

	orch := New(conn, k8sClient, config, nil)
	_, err := orch.BuildGraph(context.Background())
	require.NoError(t, err)
	require.NoError(t, orch.StartConnection(context.Background()))

	deps, err := k8sClient.AppsV1().Deployments(config.Namespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, deps.Items, 1, "only the other connection's worker should remain")
	assert.Equal(t, other.Name, deps.Items[0].Name)

	hpas, err := k8sClient.AutoscalingV2().HorizontalPodAutoscalers(config.Namespace).
		List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, hpas.Items, "the legacy HPA should be pruned with its deployment")
}

func TestGetDeploymentLabelsForConnection(t *testing.T) {
	labels := GetDeploymentLabelsForConnection("conn-123")

	assert.Equal(t, "vrsky", labels[LabelApp])
	assert.Equal(t, "conn-123", labels[LabelPipeline])
}

func TestBuildLabelSelector(t *testing.T) {
	labels := map[string]string{
		"app":      "vrsky",
		"pipeline": "conn-123",
	}

	selector := BuildLabelSelector(labels)

	assert.Contains(t, selector, "app=vrsky")
	assert.Contains(t, selector, "pipeline=conn-123")
}

// =============================================================================
// Orchestrator Integration Tests
// =============================================================================

func TestOrchestrator_New(t *testing.T) {
	conn := createTestConnection("tenant-acme", "conn-123", nil, nil)
	k8sClient := fake.NewSimpleClientset()
	config := DefaultConfig()
	validator := managementapi.NewValidator()

	orch := New(conn, k8sClient, config, validator)

	assert.NotNil(t, orch)
	assert.Equal(t, conn, orch.Connection)
	assert.Equal(t, k8sClient, orch.K8sClient)
	assert.Equal(t, config, orch.Config)
	assert.Equal(t, validator, orch.Validator)
}

func TestOrchestrator_New_DefaultConfig(t *testing.T) {
	conn := createTestConnection("tenant-acme", "conn-123", nil, nil)
	k8sClient := fake.NewSimpleClientset()

	orch := New(conn, k8sClient, nil, nil)

	assert.NotNil(t, orch)
	assert.NotNil(t, orch.Config)
	assert.Equal(t, "vrsky", orch.Config.Namespace)
}

func TestOrchestrator_BuildGraph(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)
	k8sClient := fake.NewSimpleClientset()

	orch := New(conn, k8sClient, nil, nil)
	graph, err := orch.BuildGraph(context.Background())

	require.NoError(t, err)
	assert.NotNil(t, graph)
	assert.Equal(t, graph, orch.Graph)
}

func TestOrchestrator_StartConnection(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)
	k8sClient := fake.NewSimpleClientset()

	orch := New(conn, k8sClient, nil, nil)

	// Build graph first
	_, err := orch.BuildGraph(context.Background())
	require.NoError(t, err)

	// Start connection
	err = orch.StartConnection(context.Background())
	require.NoError(t, err)

	// No per-connection worker is deployed: consumer and producer nodes are
	// served by the standing SDK connector services (#205), which the
	// management API activates with a vrsky.commands.*.connection.start.
	deployments, err := k8sClient.AppsV1().Deployments("vrsky").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, deployments.Items)
}

func TestOrchestrator_StartConnection_NoGraph(t *testing.T) {
	conn := createTestConnection("tenant-acme", "conn-123", nil, nil)
	k8sClient := fake.NewSimpleClientset()

	orch := New(conn, k8sClient, nil, nil)

	// Don't build graph
	err := orch.StartConnection(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not built")
}

func TestOrchestrator_StartConnection_NoK8sClient(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	orch := New(conn, nil, nil, nil)
	_, _ = orch.BuildGraph(context.Background())

	err := orch.StartConnection(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestOrchestrator_StopConnection(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	// Nothing spawns per-connection workers any more (#201/#205), but teardown
	// must still remove the ones a pre-change start left behind, so seed them.
	seeded := []runtime.Object{}
	for _, nodeID := range []string{"consumer-0", "producer-0"} {
		seeded = append(seeded, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildDeploymentName("conn-123", nodeID),
				Namespace: "vrsky",
				Labels:    buildLabels("conn-123", nodeID, "consumer", "tenant-acme"),
			},
		})
	}
	k8sClient := fake.NewSimpleClientset(seeded...)

	orch := New(conn, k8sClient, nil, nil)
	_, err := orch.BuildGraph(context.Background())
	require.NoError(t, err)

	deployments, _ := k8sClient.AppsV1().Deployments("vrsky").List(context.Background(), metav1.ListOptions{})
	require.Len(t, deployments.Items, 2)

	// Stop connection
	err = orch.StopConnection(context.Background())
	require.NoError(t, err)

	// Verify deployments were deleted
	deployments, _ = k8sClient.AppsV1().Deployments("vrsky").List(context.Background(), metav1.ListOptions{})
	assert.Len(t, deployments.Items, 0)
}

func TestOrchestrator_StopConnection_NoK8sClient(t *testing.T) {
	conn := createTestConnection("tenant-acme", "conn-123", nil, nil)

	orch := New(conn, nil, nil, nil)

	err := orch.StopConnection(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// =============================================================================
// Adapter Tests
// =============================================================================

func TestPipelineOrchestratorAdapter_StartPipeline(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	k8sClient := fake.NewSimpleClientset()
	adapter := NewPipelineOrchestratorAdapter(k8sClient, nil, nil)

	err := adapter.StartPipeline(context.Background(), conn)
	require.NoError(t, err)

	// The adapter validates the graph and deploys nothing: every node kind is
	// served by a standing platform service (#201 transforms, #205 edges).
	deployments, _ := k8sClient.AppsV1().Deployments("vrsky").List(context.Background(), metav1.ListOptions{})
	assert.Empty(t, deployments.Items)

	hpas, _ := k8sClient.AutoscalingV2().HorizontalPodAutoscalers("vrsky").
		List(context.Background(), metav1.ListOptions{})
	assert.Empty(t, hpas.Items, "no per-connection HPA either")
}

func TestPipelineOrchestratorAdapter_StopPipeline(t *testing.T) {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{
		createEdge("edge-0", "consumer-0", "producer-0", 0),
	}
	conn := createTestConnection("tenant-acme", "conn-123", nodes, edges)

	// Pre-create deployments with proper labels
	deployment1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrsky-conn-123-consumer-0",
			Namespace: "vrsky",
			Labels: map[string]string{
				LabelApp:      LabelAppValue,
				LabelPipeline: "conn-123",
			},
		},
	}
	deployment2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrsky-conn-123-producer-0",
			Namespace: "vrsky",
			Labels: map[string]string{
				LabelApp:      LabelAppValue,
				LabelPipeline: "conn-123",
			},
		},
	}
	k8sClient := fake.NewSimpleClientset(deployment1, deployment2)
	adapter := NewPipelineOrchestratorAdapter(k8sClient, nil, nil)

	err := adapter.StopPipeline(context.Background(), conn)
	require.NoError(t, err)

	// Verify deployments were deleted
	deployments, _ := k8sClient.AppsV1().Deployments("vrsky").List(context.Background(), metav1.ListOptions{})
	assert.Len(t, deployments.Items, 0)
}

func TestNewOrchestratorFactory(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	factory := NewOrchestratorFactory(k8sClient, nil, nil)

	// Factory should return a PipelineOrchestrator
	conn := createTestConnection("tenant-acme", "conn-123", nil, nil)
	orch := factory(conn)
	assert.NotNil(t, orch)

	// Verify it implements the interface
	var _ managementapi.PipelineOrchestrator = orch
}
