package sdk

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/component"
)

// Resources are the wired dependencies the SDK runner hands a connector at
// Configure time. DB is nil when DATABASE_URL is unset; connectors that need
// per-connection config from the management database use it. Health is the
// running health/metrics server (already started) so a connector can flip
// readiness if it wants.
//
// NATS is the live connection (never nil). Most connectors never touch it —
// producers/filters/converters get their subscription from the runner, and a
// simple consumer just calls the injected publish func. It's exposed for
// fleet-style consumers that need the control plane directly: subscribing to
// command subjects (vrsky.commands.*.connection.{start,stop}) or running their
// own durable JetStream subscriptions (e.g. tenant-consumer's cross-tenant
// bridge). Prefer the injected publish func over NATS for emitting data.
type Resources struct {
	Logger *slog.Logger
	DB     *sql.DB
	NATS   *nats.Conn
	Health *healthToggle
}

// healthToggle is the narrow slice of the health server connectors may touch.
type healthToggle struct {
	setReady func(bool)
}

// SetReady marks the worker ready/not-ready for traffic (Kubernetes readiness).
func (h *healthToggle) SetReady(ready bool) {
	if h != nil && h.setReady != nil {
		h.setReady(ready)
	}
}

type httpRoute struct {
	pattern string
	handler http.Handler
}

// baseKit is the shared embeddable state behind BaseProducer / BaseConsumer /
// BaseFilter / BaseConverter. It satisfies the bulk of component.Component
// (Name/Version/Start/Stop/Health) so connector authors implement only their
// role method(s) + Configure. Each Base* supplies its own Type().
type baseKit struct {
	name    string
	version string
	log     *slog.Logger

	mu     sync.Mutex
	routes []httpRoute

	healthy atomic.Bool
}

// init is called by the runner to seed the shared fields.
func (b *baseKit) init(name string, log *slog.Logger) {
	b.name = name
	if b.version == "" {
		b.version = "1.0.0"
	}
	b.log = log
	b.healthy.Store(true)
}

// Name returns the connector's name (used as the JetStream durable name).
func (b *baseKit) Name() string { return b.name }

// Version returns the connector version (default "1.0.0").
func (b *baseKit) Version() string {
	if b.version == "" {
		return "1.0.0"
	}
	return b.version
}

// Log returns the connector's structured logger (never nil).
func (b *baseKit) Log() *slog.Logger {
	if b.log != nil {
		return b.log
	}
	return slog.Default()
}

// Start / Stop are no-ops by default — the SDK runner owns lifecycle. A
// connector may override them if it has its own resources to open/close.
func (b *baseKit) Start(ctx context.Context) error { return nil }
func (b *baseKit) Stop(ctx context.Context) error  { return nil }

// Health reports liveness. Defaults to healthy; SetUnhealthy flips it.
func (b *baseKit) Health() component.HealthStatus {
	if b.healthy.Load() {
		return component.HealthHealthy
	}
	return component.HealthUnhealthy
}

// SetUnhealthy marks the connector unhealthy (liveness probe will fail).
func (b *baseKit) SetUnhealthy() { b.healthy.Store(false) }

// RegisterHTTPHandler registers an extra HTTP handler the runner will serve on
// the worker's auxiliary HTTP port (WORKER_HTTP_PORT). This is the hook
// file-producer uses to keep its /files management API on the same binary
// without the SDK needing to know about it.
func (b *baseKit) RegisterHTTPHandler(pattern string, h http.Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.routes = append(b.routes, httpRoute{pattern: pattern, handler: h})
}

func (b *baseKit) httpRoutes() []httpRoute {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]httpRoute, len(b.routes))
	copy(out, b.routes)
	return out
}
