package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ValueRetail/vrsky/pkg/managementapi"
)

// What used to be here, and why it is not.
//
// These tests covered per-connection NATS URL resolution for tenant placement
// (#19): a resolver returned the connection's placed instance and configForConn
// overrode OrchestratorConfig.NATSURLs with it. That value's only consumer was
// the NATS_URLS env on the per-connection worker Deployments, which ADR 0004
// stopped creating — so by #209 the resolution ran and nothing read the result.
//
// ADR 0005 removed the path rather than wiring it through, because a tenant
// instance could not have served the data in any case: they are provisioned as
// plain core NATS, and the data plane is JetStream throughout. Placement is
// accounting, not routing.
//
// The test kept below is the one that still means something: the adapter uses
// the static config, for every connection, with nothing in between. If a future
// change reintroduces per-connection NATS selection, it should fail here first
// and send the reader to ADR 0005.

func resolverTestConn() *managementapi.Connection {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{createEdge("edge-0", "consumer-0", "producer-0", 0)}
	return createTestConnection("tenant-acme", "conn-123", nodes, edges)
}

// Every connection gets the static config NATS URL — there is no per-connection
// override left to take.
func TestAdapter_UsesStaticNATSURLForEveryConnection(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NATSURLs = "nats://platform:4222"

	adapter := NewPipelineOrchestratorAdapter(fake.NewSimpleClientset(), cfg, nil)

	// Whatever the connection, the adapter's config is the one it was built
	// with — same pointer, not a per-connection clone.
	assert.Equal(t, "nats://platform:4222", adapter.config.NATSURLs)
	assert.Same(t, cfg, adapter.config,
		"the adapter must hold the config it was given; a clone per connection would mean "+
			"something is varying NATS per connection again (see ADR 0005)")

	// And starting a pipeline does not mutate it.
	_ = adapter.StartPipeline(context.Background(), resolverTestConn())
	assert.Equal(t, "nats://platform:4222", adapter.config.NATSURLs,
		"the shared base config must not be mutated")
}
