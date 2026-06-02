// Package sdk is VRSky's connector SDK: the contract and runtime every
// connector builds on. It is a thin public surface over the internal building
// blocks (pkg/component, pkg/messaging, pkg/health, pkg/crypto, pkg/envelope)
// so a connector author imports one package and writes only domain logic.
//
// A connector embeds one of the Base structs and implements its role method(s)
// plus Configure:
//
//	type myProducer struct{ sdk.BaseProducer; cfg myConfig }
//
//	func (p *myProducer) Configure(ctx context.Context, res *sdk.Resources) error {
//	    // read env, open per-connection state, res.DB is ready if DATABASE_URL set
//	    return nil
//	}
//	func (p *myProducer) Deliver(ctx context.Context, env *sdk.Envelope) error {
//	    // deliver env to the outside world; return nil / sdk.Retriable / sdk.Permanent
//	    return nil
//	}
//
//	func main() { sdk.RunProducer(context.Background(), "my-producer", &myProducer{}) }
//
// RunProducer/RunConsumer/RunFilter/RunConverter own the boilerplate: NATS +
// JetStream wiring, the durable subscription (or ingestion loop for a
// consumer), the health/metrics server, DB connection, signal handling, and
// graceful shutdown. Connectors are tested without Docker via the
// sdk/harness subpackage's embedded JetStream.
package sdk
