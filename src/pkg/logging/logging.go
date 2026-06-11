// Package logging is VRSky's shared structured-logging setup (Phase 3H, #91).
// Every long-running service builds its logger with New, which emits JSON to
// stdout (12-factor: the platform's Promtail/Alloy DaemonSet ships stdout to
// Loki — the app never logs over the network itself) and, via a context-aware
// handler, stamps every record with the platform's standard fields:
//
//	service       — the service name (baked in by New)
//	level, msg, time — slog JSON defaults
//	trace_id      — from the active OTel span (#87), when present
//	tenant_id, pipeline_id, connection_id — from the context (see ContextWith)
//
// Loki promotes these to labels so a support engineer can run
// {pipeline_id="abc-123"} and get every log touching that pipeline across
// services. NEVER log secret values — log identifiers/references instead.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// New returns a JSON structured logger for service. Level is driven by LOG_LEVEL
// (debug|info|warn|error, default info). The logger is context-aware: use the
// *Context methods (InfoContext, ErrorContext, …) with a ctx from ContextWith
// (and/or carrying an OTel span) to get tenant_id/pipeline_id/connection_id/
// trace_id stamped automatically.
func New(service string) *slog.Logger {
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: Level()})
	return slog.New(&contextHandler{Handler: base}).With("service", service)
}

// Level parses LOG_LEVEL into an slog.Level (default info).
func Level() slog.Level {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// contextHandler enriches every record with the standard context fields before
// delegating to the wrapped JSON handler.
type contextHandler struct{ slog.Handler }

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
			r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
		}
		if v := strFromCtx(ctx, tenantKey); v != "" {
			r.AddAttrs(slog.String("tenant_id", v))
		}
		if v := strFromCtx(ctx, pipelineKey); v != "" {
			r.AddAttrs(slog.String("pipeline_id", v))
		}
		if v := strFromCtx(ctx, connectionKey); v != "" {
			r.AddAttrs(slog.String("connection_id", v))
		}
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs / WithGroup re-wrap so the context enrichment survives logger.With.
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}
