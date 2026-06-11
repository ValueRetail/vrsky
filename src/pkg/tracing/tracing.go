// Package tracing wires OpenTelemetry distributed tracing for VRSky services
// (Phase 3D, #87). It is deliberately small and central: Init sets up the
// global tracer provider + W3C propagator from the environment, and the SDK /
// messaging layers use the helpers here so every connector is traced without
// per-worker boilerplate.
//
// Tracing is OFF unless OTEL_EXPORTER_OTLP_ENDPOINT is set (or
// OTEL_TRACES_ENABLED=true). When off, Init installs no exporter — the global
// provider stays OTel's no-op — so unit tests, the load harness, and bare
// `go run` pay nothing. The W3C propagator is always installed so trace context
// is still carried through a partially-enabled fleet.
//
// Services sample everything (AlwaysSample): the keep/drop decision is the
// OpenTelemetry Collector's job (tail-sampling — see docs/TRACING.md), which
// needs all spans centrally to retain whole error traces.
package tracing

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// scope is the instrumentation scope prefix for tracers created via Tracer.
const scope = "github.com/ValueRetail/vrsky"

// Init configures global tracing for serviceName and returns a shutdown func
// that flushes any pending spans. Safe to call once at process start; the
// returned shutdown should be deferred. When tracing is disabled it installs
// only the propagator and returns a no-op shutdown.
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	// Always install the propagator so incoming/outgoing W3C trace context is
	// honored even when this service doesn't export its own spans.
	otel.SetTextMapPropagator(propagator())

	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}

	if n := os.Getenv("OTEL_SERVICE_NAME"); n != "" {
		serviceName = n
	}

	// otlptracehttp reads OTEL_EXPORTER_OTLP_ENDPOINT / *_TRACES_ENDPOINT from
	// the environment and appends /v1/traces.
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", serviceName)),
	)
	if err != nil {
		// A bad resource shouldn't take the service down — fall back to a
		// minimal one rather than failing Init.
		res = resource.NewSchemaless(attribute.String("service.name", serviceName))
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Enabled reports whether tracing export is on. It's on when
// OTEL_EXPORTER_OTLP_ENDPOINT is set or OTEL_TRACES_ENABLED=true, and forced off
// by OTEL_TRACES_ENABLED=false/0 regardless of the endpoint.
func Enabled() bool {
	switch strings.ToLower(os.Getenv("OTEL_TRACES_ENABLED")) {
	case "false", "0", "no":
		return false
	case "true", "1", "yes":
		return true
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// Tracer returns a named tracer under the VRSky instrumentation scope.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(scope + "/" + name)
}

func propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}
