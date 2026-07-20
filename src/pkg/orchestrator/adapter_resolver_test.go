package orchestrator

import (
	"context"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// deployedNATSURLs starts a 2-node connection through the adapter and returns
// the NATS_URLS env value stamped on every worker Deployment.
func deployedNATSURLs(t *testing.T, adapter *PipelineOrchestratorAdapter, k8sClient *fake.Clientset, conn *managementapi.Connection) []string {
	t.Helper()
	require.NoError(t, adapter.StartPipeline(context.Background(), conn))

	deps, err := k8sClient.AppsV1().Deployments(adapter.config.Namespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, deps.Items, "expected worker deployments")

	var urls []string
	for _, d := range deps.Items {
		for _, e := range d.Spec.Template.Spec.Containers[0].Env {
			if e.Name == EnvNATSURLs {
				urls = append(urls, e.Value)
			}
		}
	}
	return urls
}

func resolverTestConn() *managementapi.Connection {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{createEdge("edge-0", "consumer-0", "producer-0", 0)}
	return createTestConnection("tenant-acme", "conn-123", nodes, edges)
}

// A placed connection: the resolver's instance URL wins over the static config,
// so orchestrator-deployed workers dial the tenant's placed NATS instance (#19).
func TestAdapter_ResolverOverridesNATSURL(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	cfg := DefaultConfig()
	cfg.NATSURLs = "nats://platform:4222"

	const placed = "nats://tenant-acme-2.vrsky-tenants.svc.cluster.local:4222"
	adapter := NewPipelineOrchestratorAdapter(k8sClient, cfg, nil,
		WithNATSURLResolver(func(_ context.Context, tenantID, connID string) (string, bool) {
			assert.Equal(t, "tenant-acme", tenantID)
			assert.Equal(t, "conn-123", connID)
			return placed, true
		}))

	urls := deployedNATSURLs(t, adapter, k8sClient, resolverTestConn())
	require.Len(t, urls, 2)
	for _, u := range urls {
		assert.Equal(t, placed, u, "worker should dial the placed instance, not the static config")
	}
}

// An unplaced connection (resolver returns false) falls back to the static
// config NATS URL — the correct behavior for single-instance/compose tenants.
func TestAdapter_ResolverFalseFallsBackToConfig(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	cfg := DefaultConfig()
	cfg.NATSURLs = "nats://platform:4222"

	adapter := NewPipelineOrchestratorAdapter(k8sClient, cfg, nil,
		WithNATSURLResolver(func(_ context.Context, _, _ string) (string, bool) {
			return "", false
		}))

	urls := deployedNATSURLs(t, adapter, k8sClient, resolverTestConn())
	require.Len(t, urls, 2)
	for _, u := range urls {
		assert.Equal(t, "nats://platform:4222", u)
	}
}

// No resolver configured → static config NATS URL (unchanged legacy behavior).
func TestAdapter_NoResolverUsesConfig(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	cfg := DefaultConfig()
	cfg.NATSURLs = "nats://platform:4222"

	adapter := NewPipelineOrchestratorAdapter(k8sClient, cfg, nil)

	urls := deployedNATSURLs(t, adapter, k8sClient, resolverTestConn())
	require.Len(t, urls, 2)
	for _, u := range urls {
		assert.Equal(t, "nats://platform:4222", u)
	}
}
