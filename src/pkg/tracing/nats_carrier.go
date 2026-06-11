package tracing

import (
	"context"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// natsHeaderCarrier adapts a nats.Header to OTel's TextMapCarrier so the W3C
// propagator can inject/extract traceparent + tracestate across the NATS hop.
type natsHeaderCarrier nats.Header

func (c natsHeaderCarrier) Get(key string) string { return nats.Header(c).Get(key) }
func (c natsHeaderCarrier) Set(key, value string) { nats.Header(c).Set(key, value) }

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// InjectNATS writes the active span's W3C trace context into h so a downstream
// consumer can continue the trace. h must be non-nil.
func InjectNATS(ctx context.Context, h nats.Header) {
	otel.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier(h))
}

// ExtractNATS returns ctx enriched with any W3C trace context found in h, so a
// span started from it becomes a child of the upstream producer span.
func ExtractNATS(ctx context.Context, h nats.Header) context.Context {
	if h == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, natsHeaderCarrier(h))
}

var _ propagation.TextMapCarrier = natsHeaderCarrier{}
