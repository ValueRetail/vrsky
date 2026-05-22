package managementapi

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
)

// Audit middleware (Phase 1G / #72).
//
// Wraps every state-changing API call. The middleware:
//   1. Decides whether the call is auditable (POST/PUT/PATCH/DELETE on
//      /api/v1/*, with a tenant in context).
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

			// Skip recording when the call never identified a tenant —
			// these are pre-auth paths that have nowhere to be stored.
			tenantID, _ := TenantIDFromContextOptional(ctx)
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
//	- Always skip non-API paths and the audit endpoints themselves.
//	- Always audit POST/PUT/PATCH/DELETE on /api/v1/*.
//	- Additionally audit GET on /api/v1/secrets/* — the acceptance criterion
//	  for #72 includes "secret access logged", and reads of the secrets
//	  endpoints are the closest we have to access events.
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
	bag.mu.Unlock()

	e := &AuditEntry{
		TenantID:     tenantID,
		ActorKind:    "user", // session middleware or future SSO will override
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
	if u := GetUserFromContext(r.Context()); u != nil {
		uid := u.ID
		e.UserID = &uid
		e.ActorLabel = u.Email
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

// clientIP best-effort extracts the originating address, preferring
// X-Forwarded-For when behind a proxy. The return value is bracket-free
// and Postgres `inet`-compatible (empty string when unknown).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx > 0 {
			return cleanIP(xff[:idx])
		}
		return cleanIP(xff)
	}
	// net.SplitHostPort handles both IPv4 ("1.2.3.4:5") and IPv6
	// ("[::1]:5") forms — strips the port and bracket pair.
	host, _, err := splitHostPortLoose(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return cleanIP(host)
}

// splitHostPortLoose is like net.SplitHostPort but accepts inputs without a
// port — returning the whole string as host. Avoids importing net just for
// one helper.
func splitHostPortLoose(s string) (host, port string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", nil
	}
	if strings.HasPrefix(s, "[") {
		// "[::1]:5" or "[::1]"
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return s, "", nil
		}
		host = s[1:end]
		if end+1 < len(s) && s[end+1] == ':' {
			port = s[end+2:]
		}
		return host, port, nil
	}
	if idx := strings.LastIndexByte(s, ':'); idx > 0 && strings.Count(s, ":") == 1 {
		return s[:idx], s[idx+1:], nil
	}
	return s, "", nil
}

func cleanIP(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	return s
}
