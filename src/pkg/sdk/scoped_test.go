package sdk_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// scopedProbe is a Producer that only serves connection "mine". It records
// Deliver calls so the test can prove foreign envelopes never reach it.
type scopedProbe struct {
	sdk.BaseProducer
	delivered atomic.Int32
}

func (p *scopedProbe) Configure(ctx context.Context, res *sdk.Resources) error { return nil }

func (p *scopedProbe) Deliver(ctx context.Context, env *envelope.Envelope) error {
	p.delivered.Add(1)
	return nil
}

func (p *scopedProbe) ServesConnection(_ context.Context, _, connectionID string) bool {
	return connectionID == "mine"
}

// The bystander bug this guards (#201 activation finding): every producer's
// shared durable used to rehydrate — and, past the cap, NAK to the DLQ — large
// offloaded payloads destined for OTHER connections. A ConnectionScoped
// producer must ack a foreign ref envelope without touching the payload: no
// store is configured here, so if the dispatch tried to rehydrate, the message
// would NAK and land in the DLQ.
func TestConnectionScoped_ForeignRefEnvelopeAckedWithoutRehydrate(t *testing.T) {
	p := &scopedProbe{}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "scoped-probe"})

	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "not-mine"
	env.PayloadRef = "spill/tenant-x/not-mine/" + env.ID // offloaded, no store configured
	env.PayloadSize = 5 << 30                            // 5 GiB — far over any cap
	h.Publish(t, env)

	// The message must be acked quietly: no Deliver call, nothing in the DLQ.
	time.Sleep(2 * time.Second)
	if n := p.delivered.Load(); n != 0 {
		t.Errorf("Deliver called %d times for a foreign connection", n)
	}
	h.ExpectNoDLQ(t, time.Second)
}

// A connection the producer serves still gets delivered.
func TestConnectionScoped_OwnConnectionDelivers(t *testing.T) {
	p := &scopedProbe{}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "scoped-probe-own"})

	env := envelope.New()
	env.TenantID = "tenant-x"
	env.IntegrationID = "mine"
	env.Payload = []byte(`{"ok":true}`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "Deliver called for own connection", func() bool {
		return p.delivered.Load() == 1
	})
}
