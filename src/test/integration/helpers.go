//go:build integration
// +build integration

// Package integration provides helper functions for E2E and integration tests.
// These helpers wrap common operations like API calls, K8s operations,
// database queries, and message verification.
package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"github.com/nats-io/nats.go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// =============================================================================
// Constants & Configuration
// =============================================================================

const (
	// DefaultAPIEndpoint is the default Management API endpoint
	DefaultAPIEndpoint = "http://localhost:8080"

	// DefaultNATSURL is the default NATS server URL
	DefaultNATSURL = "nats://localhost:4222"

	// DefaultPostgresURL is the default PostgreSQL connection string
	DefaultPostgresURL = "postgres://postgres:password@localhost:5432/vrsky?sslmode=disable"

	// DefaultK8sNamespace is the namespace for E2E tests
	DefaultK8sNamespace = "vrsky-e2e-test"

	// DefaultTimeout is the default timeout for operations
	DefaultTimeout = 2 * time.Minute

	// PodReadyTimeout is the timeout for waiting for pods to be ready
	PodReadyTimeout = 5 * time.Minute

	// PollingInterval is the interval for polling operations
	PollingInterval = 2 * time.Second
)

// =============================================================================
// E2E Test Context
// =============================================================================

// E2EContext holds all resources needed for E2E tests
type E2EContext struct {
	T            *testing.T
	K8sClient    kubernetes.Interface
	DB           *sql.DB
	NATSConn     *nats.Conn
	APIEndpoint  string
	TenantID     string
	Namespace    string
	CleanupFuncs []func()
}

// NewE2EContext creates a new E2E test context with all required resources
func NewE2EContext(t *testing.T) (*E2EContext, error) {
	t.Helper()

	ctx := &E2EContext{
		T:            t,
		APIEndpoint:  getEnvOrDefault("API_ENDPOINT", DefaultAPIEndpoint),
		TenantID:     fmt.Sprintf("e2e-tenant-%d", time.Now().UnixNano()),
		Namespace:    getEnvOrDefault("K8S_NAMESPACE", DefaultK8sNamespace),
		CleanupFuncs: []func(){},
	}

	// Initialize Kubernetes client
	k8sClient, err := GetK8sClient(t)
	if err != nil {
		return nil, fmt.Errorf("failed to create K8s client: %w", err)
	}
	ctx.K8sClient = k8sClient

	// Initialize PostgreSQL connection
	dbURL := getEnvOrDefault("DATABASE_URL", DefaultPostgresURL)
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	ctx.DB = db
	ctx.CleanupFuncs = append(ctx.CleanupFuncs, func() { db.Close() })

	// Initialize NATS connection
	natsURL := getEnvOrDefault("NATS_URL", DefaultNATSURL)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Logf("Warning: NATS not available at %s: %v", natsURL, err)
		// Don't fail - NATS may be in K8s cluster only
	} else {
		ctx.NATSConn = nc
		ctx.CleanupFuncs = append(ctx.CleanupFuncs, func() { nc.Close() })
	}

	// Ensure test namespace exists
	if err := EnsureNamespace(t, ctx.K8sClient, ctx.Namespace); err != nil {
		t.Logf("Warning: Could not ensure namespace: %v", err)
	}

	return ctx, nil
}

// Cleanup runs all cleanup functions in reverse order
func (ctx *E2EContext) Cleanup() {
	for i := len(ctx.CleanupFuncs) - 1; i >= 0; i-- {
		ctx.CleanupFuncs[i]()
	}
}

// AddCleanup adds a cleanup function to be called during teardown
func (ctx *E2EContext) AddCleanup(f func()) {
	ctx.CleanupFuncs = append(ctx.CleanupFuncs, f)
}

// =============================================================================
// Kubernetes Helpers
// =============================================================================

// GetK8sClient creates a Kubernetes client for integration tests
// It tries in-cluster config first, then falls back to kubeconfig
func GetK8sClient(t *testing.T) (kubernetes.Interface, error) {
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
		if err != nil {
			return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create K8s client: %w", err)
	}

	return client, nil
}

// EnsureNamespace creates a namespace if it doesn't exist
func EnsureNamespace(t *testing.T, client kubernetes.Interface, namespace string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"purpose": "e2e-test",
				"managed": "vrsky-e2e",
			},
		},
	}

	_, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		// Namespace might already exist - that's OK
		t.Logf("Namespace %s: %v (may already exist)", namespace, err)
	} else {
		t.Logf("Created namespace: %s", namespace)
	}

	return nil
}

// CleanupNamespace deletes all test resources from a namespace
func CleanupNamespace(t *testing.T, client kubernetes.Interface, namespace string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	// Delete all deployments with vrsky label
	err := client.AppsV1().Deployments(namespace).DeleteCollection(ctx,
		metav1.DeleteOptions{},
		metav1.ListOptions{
			LabelSelector: "app=vrsky",
		},
	)
	if err != nil {
		t.Logf("Warning: failed to cleanup deployments: %v", err)
	}

	return nil
}

// WaitForPodsReady waits for all pods with the given connection ID to be ready
func WaitForPodsReady(t *testing.T, client kubernetes.Interface, namespace, connectionID string, expectedCount int) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), PodReadyTimeout)
	defer cancel()

	labelSelector := fmt.Sprintf("app=vrsky,pipeline=%s", connectionID)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %d pods to be ready", expectedCount)
		default:
			pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				t.Logf("Error listing pods: %v", err)
				time.Sleep(PollingInterval)
				continue
			}

			readyCount := 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning {
					for _, cond := range pod.Status.Conditions {
						if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
							readyCount++
							break
						}
					}
				}
			}

			t.Logf("Pods ready: %d/%d (total pods: %d)", readyCount, expectedCount, len(pods.Items))

			if readyCount >= expectedCount {
				return nil
			}

			time.Sleep(PollingInterval)
		}
	}
}

// KillPod deletes a pod and waits for it to be replaced
func KillPod(t *testing.T, client kubernetes.Interface, namespace, podName string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	t.Logf("Killing pod: %s", podName)

	err := client.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete pod %s: %w", podName, err)
	}

	// Wait a moment for the pod to be scheduled again
	time.Sleep(5 * time.Second)

	return nil
}

// GetPodByLabel returns the first pod matching the given labels
func GetPodByLabel(t *testing.T, client kubernetes.Interface, namespace, labelSelector string) (*corev1.Pod, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found matching labels: %s", labelSelector)
	}

	return &pods.Items[0], nil
}

// GetPodIP returns the IP address of a pod
func GetPodIP(t *testing.T, client kubernetes.Interface, namespace, podName string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get pod %s: %w", podName, err)
	}

	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod %s has no IP address", podName)
	}

	return pod.Status.PodIP, nil
}

// =============================================================================
// API Helpers
// =============================================================================

// CreateConnection creates a new connection via the Management API
func CreateConnection(apiEndpoint, tenantID, name string, nodes []*managementapi.Node, edges []*managementapi.Edge) (*managementapi.Connection, error) {
	reqBody := map[string]interface{}{
		"tenant_id":   tenantID,
		"name":        name,
		"description": "E2E test pipeline",
		"nodes":       nodes,
		"edges":       edges,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := http.Post(
		fmt.Sprintf("%s/api/v1/connections", apiEndpoint),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var conn managementapi.Connection
	if err := json.NewDecoder(resp.Body).Decode(&conn); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &conn, nil
}

// StartConnection starts a connection via the Management API
func StartConnection(apiEndpoint, connectionID string) error {
	resp, err := http.Post(
		fmt.Sprintf("%s/api/v1/connections/%s/start", apiEndpoint, connectionID),
		"application/json",
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to start connection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// StopConnection stops a connection via the Management API
func StopConnection(apiEndpoint, connectionID string) error {
	resp, err := http.Post(
		fmt.Sprintf("%s/api/v1/connections/%s/stop", apiEndpoint, connectionID),
		"application/json",
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to stop connection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// DeleteConnection deletes a connection via the Management API
func DeleteConnection(apiEndpoint, connectionID string) error {
	req, err := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/api/v1/connections/%s", apiEndpoint, connectionID),
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete connection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// =============================================================================
// Messaging Helpers
// =============================================================================

// SendTestMessage sends a test message to a webhook endpoint
func SendTestMessage(webhookURL string, payload []byte) error {
	resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// SendEnvelope sends an envelope via NATS
func SendEnvelope(nc *nats.Conn, subject string, env *envelope.Envelope) error {
	data, err := envelope.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}

	if err := nc.Publish(subject, data); err != nil {
		return fmt.Errorf("failed to publish to %s: %w", subject, err)
	}

	return nc.Flush()
}

// ReceiveEnvelope subscribes to a NATS subject and waits for an envelope
func ReceiveEnvelope(nc *nats.Conn, subject string, timeout time.Duration) (*envelope.Envelope, error) {
	msgChan := make(chan *nats.Msg, 1)

	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		msgChan <- msg
	})
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to %s: %w", subject, err)
	}
	defer sub.Unsubscribe()

	select {
	case msg := <-msgChan:
		return envelope.Unmarshal(msg.Data)
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for message on %s", subject)
	}
}

// =============================================================================
// Database Helpers
// =============================================================================

// GetCheckpoint retrieves a checkpoint from the database
func GetCheckpoint(db *sql.DB, tenantID, connectionID, nodeID string) (*checkpoint.Checkpoint, error) {
	query := `
		SELECT tenant_id, connection_id, node_id, 
		       last_processed_message_id, last_processed_at, message_count, updated_at
		FROM connection_node_checkpoints
		WHERE tenant_id = $1 AND connection_id = $2 AND node_id = $3
	`

	cp := &checkpoint.Checkpoint{}
	err := db.QueryRow(query, tenantID, connectionID, nodeID).Scan(
		&cp.TenantID,
		&cp.ConnectionID,
		&cp.NodeID,
		&cp.LastProcessedMessageID,
		&cp.LastProcessedAt,
		&cp.MessageCount,
		&cp.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil // No checkpoint exists
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint: %w", err)
	}

	return cp, nil
}

// DeleteCheckpoints deletes all checkpoints for a connection
func DeleteCheckpoints(db *sql.DB, tenantID, connectionID string) error {
	query := `DELETE FROM connection_node_checkpoints WHERE tenant_id = $1 AND connection_id = $2`
	_, err := db.Exec(query, tenantID, connectionID)
	return err
}

// =============================================================================
// Metrics Helpers
// =============================================================================

// PrometheusMetrics holds parsed Prometheus metrics
type PrometheusMetrics struct {
	MessagesReceived  float64
	MessagesProcessed float64
	MessagesFailed    float64
	RawMetrics        string
}

// GetComponentMetrics fetches and parses Prometheus metrics from a component
func GetComponentMetrics(podIP string, port int) (*PrometheusMetrics, error) {
	url := fmt.Sprintf("http://%s:%d/metrics", podIP, port)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics from %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read metrics: %w", err)
	}

	metrics := &PrometheusMetrics{
		RawMetrics: string(body),
	}

	// Parse simple counters (this is a basic parser - production would use proper parsing)
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}

		if strings.Contains(line, "messages_received_total") {
			parseMetricValue(line, &metrics.MessagesReceived)
		} else if strings.Contains(line, "messages_processed_total") {
			parseMetricValue(line, &metrics.MessagesProcessed)
		} else if strings.Contains(line, "messages_failed_total") {
			parseMetricValue(line, &metrics.MessagesFailed)
		}
	}

	return metrics, nil
}

func parseMetricValue(line string, target *float64) {
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		var val float64
		fmt.Sscanf(parts[len(parts)-1], "%f", &val)
		*target += val
	}
}

// =============================================================================
// Test Data Helpers
// =============================================================================

// TestOrder represents a sample order for testing
type TestOrder struct {
	ID        string                 `json:"id"`
	Customer  string                 `json:"customer"`
	Amount    float64                `json:"amount"`
	Status    string                 `json:"status"`
	Items     []TestOrderItem        `json:"items"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Timestamp int64                  `json:"timestamp"`
}

// TestOrderItem represents a line item in an order
type TestOrderItem struct {
	SKU   string  `json:"sku"`
	Name  string  `json:"name"`
	Qty   int     `json:"qty"`
	Price float64 `json:"price"`
}

// GenerateTestOrder creates a test order with the given parameters
func GenerateTestOrder(id string, amount float64, status string) *TestOrder {
	return &TestOrder{
		ID:        id,
		Customer:  "Test Customer",
		Amount:    amount,
		Status:    status,
		Timestamp: time.Now().Unix(),
		Items: []TestOrderItem{
			{SKU: "WIDGET-1", Name: "Widget", Qty: 2, Price: 50.0},
			{SKU: "GADGET-2", Name: "Gadget", Qty: 1, Price: amount - 100},
		},
	}
}

// GenerateTestOrders creates multiple test orders
func GenerateTestOrders(count int) []*TestOrder {
	orders := make([]*TestOrder, count)
	for i := 0; i < count; i++ {
		status := "active"
		if i%3 == 0 {
			status = "inactive"
		}
		amount := 100.0 + float64(i*50)
		orders[i] = GenerateTestOrder(fmt.Sprintf("order-%d", i+1), amount, status)
	}
	return orders
}

// =============================================================================
// Pipeline Builder Helpers
// =============================================================================

// Build2NodePipeline creates a simple consumer → producer pipeline
func Build2NodePipeline(consumerConfig, producerConfig json.RawMessage) ([]*managementapi.Node, []*managementapi.Edge) {
	nodes := []*managementapi.Node{
		{
			ID:      "consumer-node",
			Type:    "consumer",
			Config:  consumerConfig,
			Enabled: true,
		},
		{
			ID:      "producer-node",
			Type:    "producer",
			Config:  producerConfig,
			Enabled: true,
		},
	}

	edges := []*managementapi.Edge{
		{
			ID:     "edge-1",
			Source: "consumer-node",
			Target: "producer-node",
			Order:  0,
		},
	}

	return nodes, edges
}

// Build3NodePipeline creates a consumer → filter → producer pipeline
func Build3NodePipeline(consumerConfig, filterConfig, producerConfig json.RawMessage) ([]*managementapi.Node, []*managementapi.Edge) {
	nodes := []*managementapi.Node{
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
	}

	edges := []*managementapi.Edge{
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
	}

	return nodes, edges
}

// Build4NodePipeline creates a consumer → filter → converter → producer pipeline
func Build4NodePipeline(consumerConfig, filterConfig, converterConfig, producerConfig json.RawMessage) ([]*managementapi.Node, []*managementapi.Edge) {
	nodes := []*managementapi.Node{
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
			ID:      "converter-node",
			Type:    "converter",
			Config:  converterConfig,
			Enabled: true,
		},
		{
			ID:      "producer-node",
			Type:    "producer",
			Config:  producerConfig,
			Enabled: true,
		},
	}

	edges := []*managementapi.Edge{
		{
			ID:     "edge-1",
			Source: "consumer-node",
			Target: "filter-node",
			Order:  0,
		},
		{
			ID:     "edge-2",
			Source: "filter-node",
			Target: "converter-node",
			Order:  1,
		},
		{
			ID:     "edge-3",
			Source: "converter-node",
			Target: "producer-node",
			Order:  2,
		},
	}

	return nodes, edges
}

// =============================================================================
// Utility Functions
// =============================================================================

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// WaitForCondition polls until a condition is met or timeout occurs
func WaitForCondition(t *testing.T, timeout time.Duration, interval time.Duration, condition func() (bool, error)) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for condition")
		case <-ticker.C:
			done, err := condition()
			if err != nil {
				t.Logf("Condition check error: %v", err)
				continue
			}
			if done {
				return nil
			}
		}
	}
}

// RetryWithBackoff retries a function with exponential backoff
func RetryWithBackoff(t *testing.T, maxAttempts int, initialDelay time.Duration, fn func() error) error {
	t.Helper()
	delay := initialDelay

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		t.Logf("Attempt %d/%d failed: %v", attempt, maxAttempts, err)

		if attempt < maxAttempts {
			time.Sleep(delay)
			delay *= 2 // Exponential backoff
		}
	}

	return fmt.Errorf("all %d attempts failed", maxAttempts)
}
