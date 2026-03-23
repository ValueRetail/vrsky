//go:build integration

// Package orchestrator - Integration tests for K8s deployment
// Run with: go test -tags=integration -v ./pkg/orchestrator/...
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// testNamespace is the namespace used for integration tests
	testNamespace = "vrsky-integration-test"

	// testTimeout is the timeout for K8s operations
	testTimeout = 60 * time.Second

	// deploymentWaitTime is how long to wait for deployments to be created
	deploymentWaitTime = 5 * time.Second
)

// getK8sClient creates a Kubernetes client for integration tests.
// It tries in-cluster config first, then falls back to kubeconfig.
func getK8sClient(t *testing.T) kubernetes.Interface {
	t.Helper()

	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		require.NoError(t, err, "failed to build kubeconfig")
	}

	client, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "failed to create K8s client")

	return client
}

// ensureNamespace creates the test namespace if it doesn't exist
func ensureNamespace(t *testing.T, client kubernetes.Interface, namespace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"purpose": "integration-test",
				"managed": "orchestrator-test",
			},
		},
	}

	_, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		// Namespace might already exist
		t.Logf("Namespace %s: %v (may already exist)", namespace, err)
	} else {
		t.Logf("Created namespace: %s", namespace)
	}
}

// cleanupNamespace removes all test deployments from the namespace
func cleanupNamespace(t *testing.T, client kubernetes.Interface, namespace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Delete all deployments with our test labels
	err := client.AppsV1().Deployments(namespace).DeleteCollection(ctx,
		metav1.DeleteOptions{},
		metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", LabelApp, LabelAppValue),
		},
	)
	if err != nil {
		t.Logf("Warning: failed to cleanup deployments: %v", err)
	} else {
		t.Logf("Cleaned up test deployments in namespace: %s", namespace)
	}
}

// createIntegrationTestConnection creates a test connection for integration tests
func createIntegrationTestConnection(tenantID, connID string) *managementapi.Connection {
	consumerConfig, _ := json.Marshal(map[string]interface{}{
		"url":    "http://example.com/webhook",
		"method": "POST",
	})
	filterConfig, _ := json.Marshal(map[string]interface{}{
		"rules": []map[string]string{
			{"field": "status", "operator": "eq", "value": "active"},
		},
	})
	producerConfig, _ := json.Marshal(map[string]interface{}{
		"url":    "http://destination.com/api",
		"method": "POST",
	})

	return &managementapi.Connection{
		ID:          connID,
		TenantID:    tenantID,
		Name:        "Integration Test Pipeline",
		Description: "Test pipeline for K8s integration tests",
		Status:      "stopped",
		Nodes: []*managementapi.Node{
			{
				ID:      "consumer-node",
				Type:    "consumer",
				Config:  consumerConfig,
				Enabled: true,
			},
			{
				ID:      "filter-node",
				Type:    "filter",
				Config:  filterConfig,
				Enabled: true,
			},
			{
				ID:      "producer-node",
				Type:    "producer",
				Config:  producerConfig,
				Enabled: true,
			},
		},
		Edges: []*managementapi.Edge{
			{
				ID:     "edge-1",
				Source: "consumer-node",
				Target: "filter-node",
				Order:  0,
			},
			{
				ID:     "edge-2",
				Source: "filter-node",
				Target: "producer-node",
				Order:  1,
			},
		},
	}
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestIntegration_OrchestratorDeploysPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup
	client := getK8sClient(t)
	ensureNamespace(t, client, testNamespace)
	defer cleanupNamespace(t, client, testNamespace)

	// Create test connection
	conn := createIntegrationTestConnection("tenant-integration", "conn-int-001")

	// Create orchestrator with test namespace config
	config := &OrchestratorConfig{
		Namespace:     testNamespace,
		ImageRegistry: "gcr.io/vrsky",
		ImageVersion:  "latest",
		NATSURLs:      "nats://nats.vrsky-platform:4222",
		NATSAccount:   "",
	}

	orch := New(conn, client, config, nil)

	// Build graph
	ctx := context.Background()
	graph, err := orch.BuildGraph(ctx)
	require.NoError(t, err, "BuildGraph should succeed")
	assert.NotNil(t, graph, "graph should not be nil")
	assert.Len(t, graph.ExecutionOrder, 3, "should have 3 nodes in execution order")

	t.Logf("Execution order: %v", graph.ExecutionOrder)
	t.Logf("Consumer node: %s", graph.ConsumerNodeID)
	t.Logf("Producer node: %s", graph.ProducerNodeID)

	// Deploy to K8s
	err = orch.StartConnection(ctx)
	require.NoError(t, err, "StartConnection should succeed")

	// Wait for deployments to be created using polling with a timeout
	timeout := time.After(deploymentWaitTime)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	deploymentsReady := false
	for !deploymentsReady {
		select {
		case <-timeout:
			t.Fatalf("timed out after %s waiting for deployments to be created", deploymentWaitTime)
		case <-ticker.C:
			deployments, err := client.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=conn-int-001", LabelPipeline),
			})
			require.NoError(t, err, "should list deployments while waiting for creation")
			if len(deployments.Items) == 3 {
				deploymentsReady = true
			}
		}
	}
	// Verify deployments were created
	deployments, err := client.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=conn-int-001", LabelPipeline),
	})
	require.NoError(t, err, "should list deployments")
	assert.Len(t, deployments.Items, 3, "should have created 3 deployments")

	// Verify each deployment
	for _, dep := range deployments.Items {
		t.Logf("Deployment: %s (node: %s, type: %s)",
			dep.Name,
			dep.Labels[LabelNode],
			dep.Labels[LabelType])

		// Verify labels
		assert.Equal(t, LabelAppValue, dep.Labels[LabelApp], "should have app label")
		assert.Equal(t, "conn-int-001", dep.Labels[LabelPipeline], "should have pipeline label")
		assert.Equal(t, "tenant-integration", dep.Labels[LabelTenant], "should have tenant label")

		// Verify container env vars
		require.Len(t, dep.Spec.Template.Spec.Containers, 1, "should have 1 container")
		container := dep.Spec.Template.Spec.Containers[0]

		envMap := make(map[string]string)
		for _, env := range container.Env {
			envMap[env.Name] = env.Value
		}

		assert.Equal(t, "tenant-integration", envMap[EnvTenantID], "should have TENANT_ID")
		assert.Equal(t, "conn-int-001", envMap[EnvConnectionID], "should have CONNECTION_ID")
		assert.NotEmpty(t, envMap[EnvNodeID], "should have NODE_ID")
		assert.NotEmpty(t, envMap[EnvNodeType], "should have NODE_TYPE")
		assert.Equal(t, "nats://nats.vrsky-platform:4222", envMap[EnvNATSURLs], "should have NATS_URLS")
	}

	t.Log("All deployments verified successfully")
}

func TestIntegration_OrchestratorStopsPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup
	client := getK8sClient(t)
	ensureNamespace(t, client, testNamespace)
	defer cleanupNamespace(t, client, testNamespace)

	// Create and deploy test connection
	conn := createIntegrationTestConnection("tenant-stop-test", "conn-stop-001")
	config := &OrchestratorConfig{
		Namespace:     testNamespace,
		ImageRegistry: "gcr.io/vrsky",
		ImageVersion:  "latest",
		NATSURLs:      "nats://nats:4222",
		NATSAccount:   "",
	}

	ctx := context.Background()
	orch := New(conn, client, config, nil)

	// Deploy
	_, err := orch.BuildGraph(ctx)
	require.NoError(t, err)
	err = orch.StartConnection(ctx)
	require.NoError(t, err)

	// Wait and verify deployments exist
	time.Sleep(deploymentWaitTime)
	deployments, err := client.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=conn-stop-001", LabelPipeline),
	})
	require.NoError(t, err)
	require.Len(t, deployments.Items, 3, "should have 3 deployments before stop")

	// Stop the pipeline
	err = orch.StopConnection(ctx)
	require.NoError(t, err, "StopConnection should succeed")

	// Wait for deletion
	time.Sleep(deploymentWaitTime)

	// Verify deployments were deleted
	deployments, err = client.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=conn-stop-001", LabelPipeline),
	})
	require.NoError(t, err)
	assert.Len(t, deployments.Items, 0, "all deployments should be deleted")

	t.Log("Pipeline stopped successfully")
}

func TestIntegration_OrchestratorNATSTopics(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup
	client := getK8sClient(t)
	ensureNamespace(t, client, testNamespace)
	defer cleanupNamespace(t, client, testNamespace)

	// Create and deploy test connection
	conn := createIntegrationTestConnection("tenant-nats", "conn-nats-001")
	config := &OrchestratorConfig{
		Namespace:     testNamespace,
		ImageRegistry: "gcr.io/vrsky",
		ImageVersion:  "latest",
		NATSURLs:      "nats://nats:4222",
		NATSAccount:   "",
	}

	ctx := context.Background()
	orch := New(conn, client, config, nil)

	// Build and deploy
	_, err := orch.BuildGraph(ctx)
	require.NoError(t, err)
	err = orch.StartConnection(ctx)
	require.NoError(t, err)
	defer func() {
		_ = orch.StopConnection(ctx)
	}()

	// Wait for deployments
	time.Sleep(deploymentWaitTime)

	// Verify NATS topics in deployments
	deployments, err := client.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=conn-nats-001", LabelPipeline),
	})
	require.NoError(t, err)

	expectedTopics := map[string]struct {
		inputTopic  string
		outputTopic string
	}{
		"consumer-node": {
			inputTopic:  "", // consumer has no input
			outputTopic: "tenant-nats.pipeline-conn-nats-001.consumer-node.output",
		},
		"filter-node": {
			inputTopic:  "tenant-nats.pipeline-conn-nats-001.consumer-node.output",
			outputTopic: "tenant-nats.pipeline-conn-nats-001.filter-node.output",
		},
		"producer-node": {
			inputTopic:  "tenant-nats.pipeline-conn-nats-001.filter-node.output",
			outputTopic: "", // producer has no output
		},
	}

	for _, dep := range deployments.Items {
		nodeID := dep.Labels[LabelNode]
		expected, ok := expectedTopics[nodeID]
		require.True(t, ok, "unexpected node: %s", nodeID)

		container := dep.Spec.Template.Spec.Containers[0]
		envMap := make(map[string]string)
		for _, env := range container.Env {
			envMap[env.Name] = env.Value
		}

		t.Logf("Node %s: INPUT=%s, OUTPUT=%s",
			nodeID, envMap[EnvInputNATSSubject], envMap[EnvOutputNATSSubject])

		assert.Equal(t, expected.inputTopic, envMap[EnvInputNATSSubject],
			"incorrect INPUT_NATS_SUBJECT for %s", nodeID)
		assert.Equal(t, expected.outputTopic, envMap[EnvOutputNATSSubject],
			"incorrect OUTPUT_NATS_SUBJECT for %s", nodeID)
	}

	t.Log("NATS topics verified successfully")
}

func TestIntegration_OrchestratorGetPipelineStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup
	client := getK8sClient(t)
	ensureNamespace(t, client, testNamespace)
	defer cleanupNamespace(t, client, testNamespace)

	// Create and deploy test connection
	conn := createIntegrationTestConnection("tenant-status", "conn-status-001")
	config := &OrchestratorConfig{
		Namespace:     testNamespace,
		ImageRegistry: "gcr.io/vrsky",
		ImageVersion:  "latest",
		NATSURLs:      "nats://nats:4222",
		NATSAccount:   "",
	}

	ctx := context.Background()
	orch := New(conn, client, config, nil)

	// Build and deploy
	_, err := orch.BuildGraph(ctx)
	require.NoError(t, err)
	err = orch.StartConnection(ctx)
	require.NoError(t, err)
	defer func() {
		_ = orch.StopConnection(ctx)
	}()

	// Wait for deployments
	time.Sleep(deploymentWaitTime)

	// Get status
	status, err := orch.GetDeploymentStatus(ctx)
	require.NoError(t, err, "GetDeploymentStatus should succeed")
	assert.Len(t, status, 3, "should have status for 3 nodes")

	for nodeID, nodeStatus := range status {
		t.Logf("Node %s: status=%s", nodeID, nodeStatus)
		// Status should be one of: running, starting, stopped
		assert.Contains(t, []string{"running", "starting", "stopped"}, nodeStatus,
			"invalid status for node %s", nodeID)
	}

	t.Log("Pipeline status retrieved successfully")
}

func TestIntegration_AdapterStartStopPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup
	client := getK8sClient(t)
	ensureNamespace(t, client, testNamespace)
	defer cleanupNamespace(t, client, testNamespace)

	// Create test connection
	conn := createIntegrationTestConnection("tenant-adapter", "conn-adapter-001")

	// Create adapter
	config := &OrchestratorConfig{
		Namespace:     testNamespace,
		ImageRegistry: "gcr.io/vrsky",
		ImageVersion:  "latest",
		NATSURLs:      "nats://nats:4222",
		NATSAccount:   "",
	}
	adapter := NewPipelineOrchestratorAdapter(client, config, nil)

	ctx := context.Background()

	// Start via adapter
	err := adapter.StartPipeline(ctx, conn)
	require.NoError(t, err, "StartPipeline should succeed")

	// Wait and verify
	time.Sleep(deploymentWaitTime)
	deployments, err := client.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=conn-adapter-001", LabelPipeline),
	})
	require.NoError(t, err)
	assert.Len(t, deployments.Items, 3, "should have 3 deployments")

	// Get status via adapter
	status, err := adapter.GetPipelineStatus(ctx, conn)
	require.NoError(t, err)
	assert.Len(t, status, 3, "should have status for 3 nodes")

	// Stop via adapter
	err = adapter.StopPipeline(ctx, conn)
	require.NoError(t, err, "StopPipeline should succeed")

	// Verify cleanup
	time.Sleep(deploymentWaitTime)
	deployments, err = client.AppsV1().Deployments(testNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=conn-adapter-001", LabelPipeline),
	})
	require.NoError(t, err)
	assert.Len(t, deployments.Items, 0, "all deployments should be deleted")

	t.Log("Adapter start/stop pipeline verified successfully")
}
