package sdk

import (
	"context"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Producer delivers pipeline envelopes to the outside world (HTTP, file, DB,
// …). The SDK runner owns the durable JetStream subscription and hands each
// envelope to Deliver; the connector implements only Configure + Deliver.
//
// Deliver's error is classified (see errors.go): nil acks, Retriable NAKs for
// redelivery, Permanent acks+logs (poison), and a bare error is Retriable.
type Producer interface {
	component.Component
	Configure(ctx context.Context, res *Resources) error
	Deliver(ctx context.Context, env *envelope.Envelope) error
}

// ConnectionScoped lets a producer on the shared data durable tell the dispatch
// loop whether a connection is its business BEFORE the payload is touched.
//
// Every producer's durable receives every message on vrsky.data.>, and most
// already begin Deliver with "look up my config for this connection; none →
// ack". That worked until the claim-check: the SDK rehydrates BEFORE Deliver,
// so a large offloaded payload was downloaded (and, past the rehydrate cap,
// NAK'd to the DLQ) by every bystander producer even though its true
// destination handled it fine. With this implemented, the dispatch loop acks
// foreign connections without rehydrating or delivering.
//
// Implementations MUST mirror their Deliver's own "not mine" semantics — in
// particular, if Deliver treats a config-lookup failure as retriable, return
// true on lookup failure so the retry still happens.
type ConnectionScoped interface {
	ServesConnection(ctx context.Context, tenantID, connectionID string) bool
}

// BaseProducer is embedded by producer connectors. It supplies Name/Version/
// Start/Stop/Health + the RegisterHTTPHandler hook, so an author writes only
// Configure + Deliver.
type BaseProducer struct {
	baseKit
}

// Type identifies this as a producer node.
func (b *BaseProducer) Type() component.ComponentType { return component.TypeProducer }
