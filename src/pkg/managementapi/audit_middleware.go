package managementapi

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Audit middleware (Phase 1G / #72).
//
// Wraps every state-changing API call. The middleware:
//   1. Decides whether the call is auditable (POST/PUT/PATCH/DELETE on
//      /api/v1/*) and which workspace's log it goes into (auditBag.tenant).
//   2. Captures method, path, status, request_id, IP, user-agent.
//   3. Lets the handler enrich the entry by calling SetAuditDetail(ctx, k, v).
//   4. After the handler returns, builds an AuditEntry and writes it
//      asynchronously so the response path is never blocked by a slow DB.
//
// Audit records of writes from inside the audit table itself are not
// produced — the table is append-only, no API exposes mutation.

// auditCtxKey is the context-key used to attach the in-flight bag of
// detail key/values the handler wants recorded.
type auditCtxKey struct{}

// auditBag holds details a handler has set during request processing.
type auditBag struct {
	mu      sync.Mutex
	details map[string]interface{}
	// override gives handlers the chance to set a more specific action
	// or resource id than the URL-derived defaults.
	actionOverride   string
	resourceOverride string
	resourceIDValue  string
	// tenantOverride files the entry under the route's path tenant; see
	// AuditUnderPathTenant.
	tenantOverride string
	// verifiedTenant is the workspace the caller was verified against: a
	// membership check passed for a session user, or it is the workspace
	// that owns the API key (#695). Never an id taken from the request
	// without that check.
	verifiedTenant string
	// actor is the principal the session or API-key check verified.
	actor *auditActor
}

// auditActor is who made the request, as stored in actor_kind, user_id and
// actor_label.
type auditActor struct {
	kind   string // "user" or "api_key"
	userID string // empty for an API key
	label  string // the user's email, or which workspace's API key
}

// tenant is the workspace the entry is filed under, "" for none:
//
//  1. a route's explicit path override (AuditUnderPathTenant);
//  2. the workspace the caller was verified against;
//  3. nothing, when the caller authenticated but was verified against no
//     workspace: a signed-in non-member, or a key from another workspace,
//     must not file entries into the workspace named in X-Tenant-ID;
//  4. otherwise (no credential at all) the context tenant, as before #695
//     (backlog 186).
func (b *auditBag) tenant(contextTenant string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tenantOverride != "" {
		return b.tenantOverride
	}
	if b.verifiedTenant != "" {
		return b.verifiedTenant
	}
	if b.actor != nil {
		return ""
	}
	return contextTenant
}

// auditBagFrom returns the request's audit bag, or nil when the request did
// not go through AuditMiddleware (not audited, or a test).
func auditBagFrom(ctx context.Context) *auditBag {
	b, _ := ctx.Value(auditCtxKey{}).(*auditBag)
	return b
}

// recordAuditUser records a verified session user as the request's actor.
// Called by the session checks once the session is valid, before the route's
// own authorization, so a refused request still says who tried.
func recordAuditUser(ctx context.Context, u *User) {
	b := auditBagFrom(ctx)
	if b == nil || u == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.actor = &auditActor{kind: "user", userID: u.ID, label: u.Email}
}

// recordAuditAPIKey records a verified tenant API key as the request's actor.
// Keys have no name of their own (one per workspace), so the label names the
// workspace that owns it. Never the key or its hash.
func recordAuditAPIKey(ctx context.Context, keyTenant *Tenant) {
	b := auditBagFrom(ctx)
	if b == nil || keyTenant == nil {
		return
	}
	label := "API key"
	if keyTenant.Slug != "" {
		label = "API key (" + keyTenant.Slug + ")"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.actor = &auditActor{kind: "api_key", label: label}
}

// recordAuditVerifiedTenant files the request under a workspace the caller
// was just verified against: a membership check that passed, or the workspace
// that owns the API key used. Never call it with an id from the request
// before that check: it would let anyone file entries into any workspace.
func recordAuditVerifiedTenant(ctx context.Context, tenantID string) {
	b := auditBagFrom(ctx)
	if b == nil || tenantID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.verifiedTenant = tenantID
}

// SetAuditDetail merges a key/value into the request's audit bag. Safe to
// call from any handler; if the request did not go through AuditMiddleware
// (e.g. a test), the call is a no-op.
func SetAuditDetail(ctx context.Context, key string, value interface{}) {
	b, ok := ctx.Value(auditCtxKey{}).(*auditBag)
	if !ok || b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.details[key] = value
}

// SetAuditAction lets a handler override the action label when the
// URL-derived one is not specific enough. E.g. "secret.rotate" instead of
// "secret.update".
func SetAuditAction(ctx context.Context, action string) {
	b, ok := ctx.Value(auditCtxKey{}).(*auditBag)
	if !ok || b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.actionOverride = action
}

// SetAuditResource lets a handler override the resource_type and id.
func SetAuditResource(ctx context.Context, resourceType, resourceID string) {
	b, ok := ctx.Value(auditCtxKey{}).(*auditBag)
	if !ok || b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resourceOverride = resourceType
	b.resourceIDValue = resourceID
}

// AuditUnderPathTenant files the request's audit entry under the route's
// {tenant_id} path value instead of under the caller's context tenant (#685),
// and wins over every other source of the tenant. The actor comes from the
// session check, as on every route. It is for the platform operator's writes
// to a workspace (plan, quotas): the change belongs in that workspace's log,
// and the operator may have no workspace selected, or a different one. Mount
// it after RequireOperator, so only a caller already allowed to act on the
// path's workspace can file entries in its log.
func AuditUnderPathTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := r.Context().Value(auditCtxKey{}).(*auditBag); ok && b != nil {
			if tenantID := r.PathValue("tenant_id"); tenantID != "" {
				b.mu.Lock()
				b.tenantOverride = tenantID
				b.mu.Unlock()
			}
		}
		next.ServeHTTP(w, r)
	})
}

// auditResponseWriter captures the HTTP status code for the audit record.
// We deliberately do NOT capture the response body — could include user
// secrets, and audit detail lives in SetAuditDetail.
type auditResponseWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (rw *auditResponseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.status = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *auditResponseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.status = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

// Flush forwards to the underlying ResponseWriter's Flusher. Without it the
// wrapper hides http.Flusher, so SSE endpoints (metrics/ws, tenant
// status/stream) failed the w.(http.Flusher) assertion with 500 "streaming
// not supported" whenever the audit middleware was in the chain.
func (rw *auditResponseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter's Hijacker so WebSocket
// upgrade endpoints (metrics/ws) keep working through the audit middleware.
func (rw *auditResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// AuditWriter is the minimal surface AuditMiddleware needs. Implemented by
// PostgresRepository; tests can pass a stub.
type AuditWriter interface {
	CreateAuditEntry(ctx context.Context, e *AuditEntry) error
}

// AuditMiddleware returns an http middleware that writes one audit_log
// row per mutating request, after the handler runs.
//
// `logger` receives WARN-level messages if the DB write fails. The
// response path is never blocked or failed by a DB error here — a missed
// audit row is preferable to a refused request.
func AuditMiddleware(writer AuditWriter, logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !shouldAudit(r) {
				next.ServeHTTP(w, r)
				return
			}
			bag := &auditBag{details: map[string]interface{}{}}
			ctx := context.WithValue(r.Context(), auditCtxKey{}, bag)
			arw := &auditResponseWriter{ResponseWriter: w}

			next.ServeHTTP(arw, r.WithContext(ctx))

			// Skip recording when there is no workspace to file it under:
			// pre-auth paths, and callers who authenticated but were not
			// verified against any workspace (see auditBag.tenant).
			contextTenant, _ := TenantIDFromContextOptional(ctx)
			tenantID := bag.tenant(contextTenant)
			if tenantID == "" {
				return
			}

			entry := buildAuditEntry(r, arw.status, tenantID, bag)
			go func(e *AuditEntry) {
				// Detach from the request context so a client cancellation
				// after response doesn't cancel the audit write.
				bg := context.Background()
				if err := writer.CreateAuditEntry(bg, e); err != nil && logger != nil {
					logger.Printf("audit: failed to write %s %s: %v", e.Method, e.Path, err)
				}
			}(entry)
		})
	}
}

// TenantIDFromContextOptional is the non-erroring variant of
// GetTenantIDFromContext — used by the middleware to decide whether to
// audit at all.
func TenantIDFromContextOptional(ctx context.Context) (string, bool) {
	tenantID, ok := ctx.Value(TenantIDKey).(string)
	return tenantID, ok && tenantID != ""
}

// shouldAudit decides which requests get an audit entry.
//
//   - Always skip non-API paths and the audit endpoints themselves.
//   - Always audit POST/PUT/PATCH/DELETE on /api/v1/*.
//   - Additionally audit GET on /api/v1/secrets/* — the acceptance criterion
//     for #72 includes "secret access logged", and reads of the secrets
//     endpoints are the closest we have to access events.
func shouldAudit(r *http.Request) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/v1/") {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/audit") {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	case http.MethodGet:
		// Secret reads are sensitive; list view at /api/v1/secrets is
		// metadata-only so still worth recording.
		return strings.HasPrefix(path, "/api/v1/secrets")
	}
	return false
}

// buildAuditEntry materialises the AuditEntry that will be persisted.
// Path-based action/resource derivation lives in deriveAction so it can be
// unit-tested without spinning up an HTTP server.
func buildAuditEntry(r *http.Request, status int, tenantID string, bag *auditBag) *AuditEntry {
	action, resourceType, resourceID := deriveAction(r.Method, r.URL.Path)

	bag.mu.Lock()
	if bag.actionOverride != "" {
		action = bag.actionOverride
	}
	if bag.resourceOverride != "" {
		resourceType = bag.resourceOverride
	}
	if bag.resourceIDValue != "" {
		resourceID = bag.resourceIDValue
	}
	details := make(map[string]interface{}, len(bag.details))
	for k, v := range bag.details {
		details[k] = v
	}
	actor := bag.actor
	bag.mu.Unlock()

	e := &AuditEntry{
		TenantID:     tenantID,
		ActorKind:    "user", // unauthenticated requests keep the column default
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Method:       r.Method,
		Path:         r.URL.Path,
		StatusCode:   status,
		RequestID:    r.Header.Get("X-Request-Id"),
		IPAddress:    clientIP(r),
		UserAgent:    r.Header.Get("User-Agent"),
		Details:      details,
	}
	if actor != nil {
		e.ActorKind = actor.kind
		e.ActorLabel = actor.label
		if actor.userID != "" {
			uid := actor.userID
			e.UserID = &uid
		}
	}
	return e
}

// deriveAction maps (method, URL) → (action, resource_type, resource_id).
//
//	POST   /api/v1/connections             → connection.create
//	PUT    /api/v1/connections/{id}        → connection.update
//	DELETE /api/v1/connections/{id}        → connection.delete
//	POST   /api/v1/connections/{id}/start  → connection.start
//	POST   /api/v1/connections/{id}/stop   → connection.stop
//	POST   /api/v1/secrets                 → secret.create
//	DELETE /api/v1/secrets/{id}            → secret.delete
//	POST   /api/v1/secrets/{id}/rotate     → secret.rotate
//
// Anything that doesn't match a known pattern still gets logged with a
// best-effort action like "{resource}.write".
func deriveAction(method, path string) (action, resourceType, resourceID string) {
	trim := strings.TrimPrefix(path, "/api/v1/")
	parts := strings.Split(trim, "/")
	if len(parts) == 0 || parts[0] == "" {
		return method + ".unknown", "", ""
	}
	resourceType = singular(parts[0])

	// /{type}
	if len(parts) == 1 {
		switch method {
		case http.MethodPost:
			return resourceType + ".create", resourceType, ""
		default:
			return resourceType + "." + strings.ToLower(method), resourceType, ""
		}
	}
	// /{type}/{id}
	resourceID = parts[1]
	if len(parts) == 2 {
		switch method {
		case http.MethodGet:
			return resourceType + ".get", resourceType, resourceID
		case http.MethodPut, http.MethodPatch:
			return resourceType + ".update", resourceType, resourceID
		case http.MethodDelete:
			return resourceType + ".delete", resourceType, resourceID
		case http.MethodPost:
			return resourceType + ".write", resourceType, resourceID
		}
	}
	// /{type}/{id}/{verb} or /{type}/{id}/{sub}/{seq}/{verb}
	verb := parts[len(parts)-1]
	return resourceType + "." + verb, resourceType, resourceID
}

// singular converts the common plural REST collection names into the
// singular form used in the action label. Anything not in the table is
// left as-is.
func singular(name string) string {
	switch name {
	case "connections":
		return "connection"
	case "secrets":
		return "secret"
	case "tenants":
		return "tenant"
	case "auth":
		return "auth"
	default:
		return strings.TrimSuffix(name, "s")
	}
}

// clientIP is the originating address for audit rows (see clientIPAddr),
// bracket-free and Postgres `inet`-compatible; empty when unknown.
func clientIP(r *http.Request) string {
	return clientIPString(r)
}
