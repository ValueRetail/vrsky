package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// bufLogger builds a logger with the same context-aware handler New uses, but
// writing JSON to buf so a test can inspect the emitted record.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	h := &contextHandler{Handler: slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
	return slog.New(h).With("service", "test-svc")
}

func lastLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no log output")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("log line is not valid JSON: %v (%q)", err, line)
	}
	return m
}

// TestMandatoryFields is the acceptance guard (#91): with a fully-populated
// context, a log line carries all of the platform's mandatory fields.
func TestMandatoryFields(t *testing.T) {
	var buf bytes.Buffer
	log := bufLogger(&buf)

	traceID, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	ctx = ContextWith(ctx, "tenant-1", "conn-9")

	log.InfoContext(ctx, "pipeline event")

	m := lastLine(t, &buf)
	for _, k := range []string{"service", "level", "msg", "time", "trace_id", "tenant_id", "pipeline_id", "connection_id"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing mandatory field %q in %v", k, m)
		}
	}
	if m["service"] != "test-svc" || m["tenant_id"] != "tenant-1" ||
		m["pipeline_id"] != "conn-9" || m["connection_id"] != "conn-9" {
		t.Errorf("unexpected field values: %v", m)
	}
	if m["trace_id"] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("trace_id = %v, want the span's trace id", m["trace_id"])
	}
}

// TestBaseFieldsWithoutContext: a context-less / bare log still always has the
// always-on fields (service/level/msg/time); the tenant/pipeline/trace fields
// are simply absent when not in context.
func TestBaseFieldsWithoutContext(t *testing.T) {
	var buf bytes.Buffer
	bufLogger(&buf).Info("startup")

	m := lastLine(t, &buf)
	for _, k := range []string{"service", "level", "msg", "time"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing always-on field %q in %v", k, m)
		}
	}
	for _, k := range []string{"tenant_id", "pipeline_id", "connection_id", "trace_id"} {
		if _, ok := m[k]; ok {
			t.Errorf("field %q should be absent without context, got %v", k, m[k])
		}
	}
}

func TestContextWithIgnoresEmpty(t *testing.T) {
	ctx := ContextWith(context.Background(), "", "")
	if strFromCtx(ctx, tenantKey) != "" || strFromCtx(ctx, connectionKey) != "" || strFromCtx(ctx, pipelineKey) != "" {
		t.Error("ContextWith attached empty values")
	}
}
