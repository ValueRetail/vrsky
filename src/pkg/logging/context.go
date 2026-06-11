package logging

import "context"

// ctxKey is a private context key type so these values can't collide with keys
// set by other packages.
type ctxKey int

const (
	tenantKey ctxKey = iota
	pipelineKey
	connectionKey
)

// ContextWith returns a context carrying the tenant and connection (pipeline)
// identity so that any log emitted with it via the *Context slog methods
// (InfoContext, ErrorContext, …) automatically includes tenant_id, pipeline_id
// and connection_id. In VRSky the connection IS the pipeline, so pipeline_id and
// connection_id are the same value. Empty values are not attached.
func ContextWith(ctx context.Context, tenantID, connectionID string) context.Context {
	if tenantID != "" {
		ctx = context.WithValue(ctx, tenantKey, tenantID)
	}
	if connectionID != "" {
		ctx = context.WithValue(ctx, connectionKey, connectionID)
		ctx = context.WithValue(ctx, pipelineKey, connectionID)
	}
	return ctx
}

func strFromCtx(ctx context.Context, k ctxKey) string {
	if v, ok := ctx.Value(k).(string); ok {
		return v
	}
	return ""
}
