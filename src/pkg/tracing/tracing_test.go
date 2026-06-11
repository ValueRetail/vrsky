package tracing

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/trace"
)

func TestEnabled(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		flag     string
		want     bool
	}{
		{"off by default", "", "", false},
		{"on via endpoint", "http://otel-collector:4318", "", true},
		{"on via flag", "", "true", true},
		{"flag false overrides endpoint", "http://otel-collector:4318", "false", false},
		{"flag 0 overrides endpoint", "http://otel-collector:4318", "0", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", c.endpoint)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
			t.Setenv("OTEL_TRACES_ENABLED", c.flag)
			if got := Enabled(); got != c.want {
				t.Errorf("Enabled() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestInitNoopWhenDisabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_ENABLED", "")

	shutdown, err := Init(context.Background(), "test-svc")
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if shutdown == nil {
		t.Fatal("Init() returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown() error = %v, want nil", err)
	}
}

// TestInjectExtractRoundTrip proves trace context survives a round-trip through
// a nats.Header via the W3C propagator — the mechanism that links a producer
// span to the downstream consumer span across the NATS hop.
func TestInjectExtractRoundTrip(t *testing.T) {
	// Keep the test self-contained: disable export regardless of the shell's
	// OTEL_* vars. Init (disabled) still installs the propagator, which is all
	// this test needs.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_ENABLED", "false")
	if _, err := Init(context.Background(), "test-svc"); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	traceID, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	hdr := nats.Header{}
	InjectNATS(ctx, hdr)
	// nats.Header is case-sensitive (unlike net/http.Header); the W3C propagator
	// uses the lowercase "traceparent" key on both inject and extract.
	if hdr.Get("traceparent") == "" {
		t.Fatalf("InjectNATS wrote no traceparent header; got %v", hdr)
	}

	got := trace.SpanContextFromContext(ExtractNATS(context.Background(), hdr))
	if got.TraceID() != traceID {
		t.Errorf("extracted TraceID = %s, want %s", got.TraceID(), traceID)
	}
	if got.SpanID() != spanID {
		t.Errorf("extracted SpanID = %s, want %s", got.SpanID(), spanID)
	}
	if !got.IsSampled() {
		t.Error("extracted span context lost the sampled flag")
	}
}

func TestExtractNATSNilHeader(t *testing.T) {
	ctx := ExtractNATS(context.Background(), nil)
	if ctx == nil {
		t.Fatal("ExtractNATS(nil) returned nil context")
	}
}
