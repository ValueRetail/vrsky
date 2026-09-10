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
// accounting, not routing. The NATSURLs field itself went with it, so there is
// no longer any NATS setting on the orchestrator config to vary.
//
// What is left worth asserting is the shape that made the old bug possible: the
// adapter holding one config and handing it to every connection unchanged. A
// per-connection clone reappearing here is the signal that something is being
// varied per connection again, and the reader should start at ADR 0005.

func resolverTestConn() *managementapi.Connection {
	nodes := []*managementapi.Node{
		createNode("consumer-0", "consumer", nil),
		createNode("producer-0", "producer", nil),
	}
	edges := []*managementapi.Edge{createEdge("edge-0", "consumer-0", "producer-0", 0)}
	return createTestConnection("tenant-acme", "conn-123", nodes, edges)
}

// One config, shared, unmutated — for every connection.
func TestAdapter_HoldsOneConfigForEveryConnection(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Namespace = "vrsky-platform"

	adapter := NewPipelineOrchestratorAdapter(fake.NewSimpleClientset(), cfg, nil)

	assert.Same(t, cfg, adapter.config,
		"the adapter must hold the config it was given; a clone per connection would mean "+
			"something is varying orchestrator config per connection again (see ADR 0005)")

	_ = adapter.StartPipeline(context.Background(), resolverTestConn())

	assert.Same(t, cfg, adapter.config, "starting a pipeline must not swap the config")
	assert.Equal(t, "vrsky-platform", adapter.config.Namespace, "nor mutate it")
}
