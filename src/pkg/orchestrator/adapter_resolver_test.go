package orchestrator

import (
	"context"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes/fake"
)

// These tests cover the per-connection NATS URL resolution added for tenant
// NATS placement (#19). They assert on configForConn rather than on a deployed
// worker's env: since #201/#205 the orchestrator deploys no per-connection
// workers at all, so there is no pod spec left to read the value off.
//
// NOTE (#205 follow-up): resolution is therefore currently inert in prod — the
// standing connector services dial the NATS_URL in their OWN env, so a
// connection placed on a tenant instance is not actually served from it. Wiring
// placement through to the standing services is tracked separately.

func resolverTestConn() *managementapi.Connection {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{createEdge("edge-0", "consumer-0", "producer-0", 0)}
	return createTestConnection("tenant-acme", "conn-123", nodes, edges)
}

// A placed connection: the resolver's instance URL wins over the static config.
func TestAdapter_ResolverOverridesNATSURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NATSURLs = "nats://platform:4222"

	const placed = "nats://tenant-acme-2.vrsky-tenants.svc.cluster.local:4222"
	adapter := NewPipelineOrchestratorAdapter(fake.NewSimpleClientset(), cfg, nil,
		WithNATSURLResolver(func(_ context.Context, tenantID, connID string) (string, bool) {
			assert.Equal(t, "tenant-acme", tenantID)
			assert.Equal(t, "conn-123", connID)
			return placed, true
		}))

	resolved := adapter.configForConn(context.Background(), resolverTestConn())
	assert.Equal(t, placed, resolved.NATSURLs, "a placed connection should resolve to its instance")
	assert.Equal(t, "nats://platform:4222", adapter.config.NATSURLs,
		"the shared base config must not be mutated — concurrent connections would race")
}

// An unplaced connection (resolver returns false) falls back to the static
// config NATS URL — the correct behavior for single-instance/compose tenants.
func TestAdapter_ResolverFalseFallsBackToConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NATSURLs = "nats://platform:4222"

	adapter := NewPipelineOrchestratorAdapter(fake.NewSimpleClientset(), cfg, nil,
		WithNATSURLResolver(func(_ context.Context, _, _ string) (string, bool) {
			return "", false
		}))

	resolved := adapter.configForConn(context.Background(), resolverTestConn())
	assert.Equal(t, "nats://platform:4222", resolved.NATSURLs)
}

// No resolver configured → static config NATS URL (unchanged legacy behavior).
func TestAdapter_NoResolverUsesConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NATSURLs = "nats://platform:4222"

	adapter := NewPipelineOrchestratorAdapter(fake.NewSimpleClientset(), cfg, nil)

	resolved := adapter.configForConn(context.Background(), resolverTestConn())
	assert.Equal(t, "nats://platform:4222", resolved.NATSURLs)
}
