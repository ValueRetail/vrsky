package sdk

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/health"
	"github.com/ValueRetail/vrsky/pkg/logging"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/natsdiscovery"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
	"github.com/ValueRetail/vrsky/pkg/tracing"
)

// shutdownGrace is how long the runner waits for in-flight work to finish on
// SIGTERM/SIGINT before exiting.
const shutdownGrace = 30 * time.Second

// kitAccess is satisfied by any connector embedding one of the Base* structs.
// The runner uses it to seed shared state and collect custom HTTP handlers.
type kitAccess interface {
	init(name string, log *slog.Logger)
	httpRoutes() []httpRoute
}

// startFunc begins kind-specific processing and returns a stop function. ctx
// is cancelled on shutdown before stop() is called.
type startFunc func(ctx context.Context, js nats.JetStreamContext, pub *messaging.Publisher, res *Resources) (stop func(), err error)

// RunOption tunes the runner. Production callers pass none (everything comes
// from the environment); the testing harness injects an embedded NATS conn,
// a mock DB, and disables the health server.
type RunOption func(*runOptions)

type runOptions struct {
	nc            *nats.Conn
	db            *sql.DB
	payloadStore  objectstore.ObjectStore
	disableHealth bool
}

// WithNATSConn supplies an already-connected NATS connection instead of
// dialing NATS_URL. The runner will not close a caller-supplied connection.
func WithNATSConn(nc *nats.Conn) RunOption { return func(o *runOptions) { o.nc = nc } }

// WithDB supplies a database handle instead of opening DATABASE_URL. The
// runner will not close a caller-supplied DB.
func WithDB(db *sql.DB) RunOption { return func(o *runOptions) { o.db = db } }

// WithPayloadStore supplies the large-payload offload store instead of building
// one from PAYLOAD_STORE_* env (used by tests). The runner will not close a
// caller-supplied store.
func WithPayloadStore(s objectstore.ObjectStore) RunOption {
	return func(o *runOptions) { o.payloadStore = s }
}

// WithoutHealthServer skips starting the health/metrics HTTP server (used in
// tests to avoid port conflicts).
func WithoutHealthServer() RunOption { return func(o *runOptions) { o.disableHealth = true } }

// RunProducer boots a producer connector: env config → NATS/DB/health →
// Configure → durable subscription dispatching to Deliver → block until
// SIGTERM/SIGINT → graceful stop.
func RunProducer(ctx context.Context, name string, p Producer, opts ...RunOption) error {
	return run(ctx, name, p,
		func(ctx context.Context, res *Resources) error { return p.Configure(ctx, res) },
		func(ctx context.Context, js nats.JetStreamContext, _ *messaging.Publisher, res *Resources) (func(), error) {
			return subscribeDispatch(js, name, res, func(c context.Context, env *envelope.Envelope) error {
				return p.Deliver(c, env)
			})
		}, opts...)
}

// RunConsumer boots a consumer connector: it drives its own ingestion loop in
// Run(ctx, publish); the SDK supplies the publish closure.
func RunConsumer(ctx context.Context, name string, c Consumer, opts ...RunOption) error {
	return run(ctx, name, c,
		func(ctx context.Context, res *Resources) error { return c.Configure(ctx, res) },
		func(ctx context.Context, js nats.JetStreamContext, pub *messaging.Publisher, res *Resources) (func(), error) {
			// last_payload preview writes are throttled per connection so a
			// high-throughput consumer doesn't UPDATE the row on every message
			// (write amplification). State is per consumer process.
			var lpMu sync.Mutex
			lpLastWrite := make(map[string]time.Time)
			const lpMinInterval = 30 * time.Second
			publish := func(pctx context.Context, env *envelope.Envelope) error {
				// Offload an over-threshold payload to object storage (claim-check)
				// so the published message stays under NATS's max_payload. No-op
				// for the common small-payload case.
				offloaded, err := offloadIfLarge(pctx, res.payloadStore, env, res.inlineMaxBytes, res.Logger)
				if err != nil {
					return err
				}
				body, err := envelope.Marshal(env)
				if err != nil {
					return fmt.Errorf("marshal envelope: %w", err)
				}
				if perr := pub.Publish(pctx, env.TenantID, env.IntegrationID, env.ID, body); perr != nil {
					return perr
				}
				// Store the last payload so the UI's "show data structure" preview
				// (filter + converter) can sample ANY source once it has passed
				// data through once — not just the handful of consumers that used
				// to write it themselves. Best-effort, throttled, and tenant-scoped:
				// never fail a publish over a preview write. Skipped when the
				// payload was offloaded — the wire body carries only a reference,
				// so there's no payload structure to preview.
				if !offloaded && res.DB != nil && env.IntegrationID != "" {
					now := time.Now()
					lpMu.Lock()
					last, seen := lpLastWrite[env.IntegrationID]
					due := !seen || now.Sub(last) >= lpMinInterval
					if due {
						lpLastWrite[env.IntegrationID] = now
					}
					lpMu.Unlock()
					if due {
						_, _ = res.DB.ExecContext(pctx, "UPDATE connections SET last_payload = $1 WHERE id = $2 AND tenant_id = $3", body, env.IntegrationID, env.TenantID)
					}
				}
				return nil
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if r := recover(); r != nil {
						res.Logger.Error("consumer Run panicked", "panic", r)
					}
				}()
				if err := c.Run(ctx, publish); err != nil && ctx.Err() == nil {
					res.Logger.Error("consumer Run exited with error", "error", err)
				}
			}()
			return func() { <-done }, nil
		}, opts...)
}

// RunFilter boots a filter connector: subscribe → Evaluate → forward kept
// envelopes (with a fresh ID to avoid JetStream dedup) or drop.
func RunFilter(ctx context.Context, name string, f Filter, opts ...RunOption) error {
	return run(ctx, name, f,
		func(ctx context.Context, res *Resources) error { return f.Configure(ctx, res) },
		func(ctx context.Context, js nats.JetStreamContext, pub *messaging.Publisher, res *Resources) (func(), error) {
			return subscribeDispatch(js, name, res, func(c context.Context, env *envelope.Envelope) error {
				keep, out, err := f.Evaluate(c, env)
				if err != nil {
					return err
				}
				if !keep || out == nil {
					return nil // dropped — ack
				}
				return republish(c, pub, res, out)
			})
		}, opts...)
}

// RunConverter boots a converter connector: subscribe → Convert → forward the
// transformed envelope (fresh ID).
func RunConverter(ctx context.Context, name string, cv Converter, opts ...RunOption) error {
	return run(ctx, name, cv,
		func(ctx context.Context, res *Resources) error { return cv.Configure(ctx, res) },
		func(ctx context.Context, js nats.JetStreamContext, pub *messaging.Publisher, res *Resources) (func(), error) {
			return subscribeDispatch(js, name, res, func(c context.Context, env *envelope.Envelope) error {
				out, err := cv.Convert(c, env)
				if err != nil {
					return err
				}
				if out == nil {
					return nil
				}
				return republish(c, pub, res, out)
			})
		}, opts...)
}

// run is the shared bootstrap + lifecycle for every connector kind.
func run(ctx context.Context, name string, c interface{}, configure func(context.Context, *Resources) error, start startFunc, opts ...RunOption) error {
	var o runOptions
	for _, opt := range opts {
		opt(&o)
	}
	logger := newLogger(name)

	// Distributed tracing (no-op unless OTEL_EXPORTER_OTLP_ENDPOINT is set).
	if shutdownTracing, terr := tracing.Init(ctx, name); terr != nil {
		logger.Warn("tracing init failed; continuing without tracing", "error", terr)
	} else {
		defer func() {
			sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer scancel()
			_ = shutdownTracing(sctx)
		}()
	}

	kit, ok := c.(kitAccess)
	if !ok {
		return fmt.Errorf("sdk: connector %q must embed one of sdk.Base{Producer,Consumer,Filter,Converter}", name)
	}
	kit.init(name, logger)

	// NATS — injected (harness) or dialed from env. Only close what we opened.
	nc := o.nc
	if nc == nil {
		var err error
		nc, err = connectNATS(logger)
		if err != nil {
			return fmt.Errorf("connect NATS: %w", err)
		}
		defer nc.Close()
	}
	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream context: %w", err)
	}
	pub := messaging.NewPublisher(js)

	// DB — injected (harness) or opened from env. Only close what we opened.
	db := o.db
	if db == nil {
		if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
			db, err = openDB(dsn)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			logger.Info("database connected")
		}
	}

	// Health/metrics server (skippable in tests). Registers default upstream
	// readiness checks (NATS connected, DB reachable) so /readyz reflects real
	// dependency state; flipped not-ready on shutdown to drain (see below).
	var (
		setReady func(bool)
		addCheck func(string, func(context.Context) error)
		drainFn  func()
	)
	if !o.disableHealth {
		hsrv := health.NewServer(health.Config{
			Port:        envInt("HEALTH_PORT", 8080),
			ComponentID: name,
			Logger:      logger,
		})
		hsrv.AddReadinessCheck("nats", func(context.Context) error {
			if nc == nil || !nc.IsConnected() {
				return fmt.Errorf("nats not connected")
			}
			return nil
		})
		if db != nil {
			hsrv.AddReadinessCheck("database", func(cctx context.Context) error {
				return db.PingContext(cctx)
			})
		}
		if err := hsrv.Start(ctx); err != nil {
			return fmt.Errorf("start health server: %w", err)
		}
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = hsrv.Stop(sctx)
		}()
		setReady = hsrv.SetReady
		addCheck = func(checkName string, fn func(context.Context) error) { hsrv.AddReadinessCheck(checkName, fn) }
		drainFn = func() { hsrv.SetReady(false) }
	}

	// Large-payload offload store — injected (harness) or built from
	// PAYLOAD_STORE_* env. nil when unconfigured; only close what we opened.
	payloadStore := o.payloadStore
	if payloadStore == nil {
		ps, perr := openPayloadStore(ctx, logger)
		if perr != nil {
			return fmt.Errorf("open payload store: %w", perr)
		}
		if ps != nil {
			payloadStore = ps
			defer func() { _ = ps.Close() }()
		}
	}

	res := &Resources{
		Logger:         logger,
		DB:             db,
		NATS:           nc,
		Health:         &healthToggle{setReady: setReady, addCheck: addCheck},
		payloadStore:   payloadStore,
		inlineMaxBytes: inlineMaxFromEnv(),
	}

	if err := configure(ctx, res); err != nil {
		return fmt.Errorf("configure: %w", err)
	}

	// Auxiliary HTTP server for any handlers the connector registered (e.g.
	// file-producer's /files management API).
	auxStop := startAuxHTTP(kit.httpRoutes(), logger)
	defer auxStop()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop, err := start(runCtx, js, pub, res)
	if err != nil {
		return fmt.Errorf("start processing: %w", err)
	}
	logger.Info("connector running", "name", name)

	// Block until signalled (or parent ctx cancelled).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	fromSignal := false
	select {
	case <-runCtx.Done():
	case <-sigCh:
		fromSignal = true
		logger.Info("shutdown signal received")
	}

	// Drain (signal-initiated shutdown only — i.e. a real SIGTERM from a K8s
	// rolling deploy; programmatic ctx-cancel is the caller's own lifecycle):
	// flip readiness to not-ready so Kubernetes removes this pod from Service
	// endpoints BEFORE we stop consuming — the key to losing zero in-flight
	// messages. Wait a short pre-stop window for that de-registration to land.
	if fromSignal && drainFn != nil {
		drainFn()
		if d := shutdownDrain(); d > 0 {
			logger.Info("draining before stop", "drain", d)
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-sigCh: // a second signal skips the drain wait
				t.Stop()
				logger.Info("second signal; skipping drain wait")
			}
		}
	}

	// Graceful stop: cancel work, then wait for stop() within the grace window.
	cancel()
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		logger.Warn("shutdown grace exceeded; exiting anyway", "grace", shutdownGrace)
	}

	// Once the subscription/ingestion loop has stopped, give the connector a
	// chance to release resources it opened in Configure (e.g. db-producer's
	// per-target SQL pools). Base* connectors inherit a no-op Stop, so this is
	// inert unless a connector overrides it.
	if st, ok := c.(interface{ Stop(context.Context) error }); ok {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := st.Stop(sctx); err != nil {
			logger.Warn("connector Stop returned an error", "error", err)
		}
		scancel()
	}

	logger.Info("connector stopped", "name", name)
	return nil
}

// ackWaitFromEnv reads the optional WORKER_ACK_WAIT override (a Go duration like
// "2m"). Returns 0 when unset/invalid, letting the messaging layer apply its
// default. AckWait is the crash-recovery window, not the handler time budget
// (heartbeats cover long handlers, #139); raise it only to slow the first
// redelivery after a true worker death.
func ackWaitFromEnv(logger *slog.Logger) time.Duration {
	raw := os.Getenv("WORKER_ACK_WAIT")
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Warn("ignoring invalid WORKER_ACK_WAIT", "value", raw, "error", err)
		return 0
	}
	return d
}

// subscribeDispatch wires a durable JetStream subscription whose handler
// unmarshals the envelope and calls deliver, mapping the SDK error classes
// onto messaging's NAK/DLQ semantics: nil → ack; Permanent → ack+log (poison);
// anything else → NAK (messaging retries with backoff, then DLQs).
func subscribeDispatch(js nats.JetStreamContext, durable string, res *Resources, deliver func(context.Context, *envelope.Envelope) error) (func(), error) {
	logger := res.Logger
	sub, err := messaging.Subscribe(js, messaging.SubscriberOpts{
		DurableName: durable,
		// AckWait left at the messaging default: the per-message budget is no
		// longer bounded by AckWait — the subscriber sends InProgress heartbeats
		// while the handler runs (#139), so even a slow producer target / bulk
		// API never triggers a duplicate. Operators can still raise the
		// crash-recovery window per worker via WORKER_ACK_WAIT (e.g. "2m").
		AckWait: ackWaitFromEnv(logger),
		Logger:  logger,
	}, func(ctx context.Context, msg *nats.Msg) error {
		env, err := envelope.Unmarshal(msg.Data)
		if err != nil {
			// Malformed payload will never parse — drop it (ack) rather than
			// burn the retry budget.
			logger.Error("drop unparseable envelope", "subject", msg.Subject, "error", err)
			return nil
		}

		// Continue the trace from the producer that published this message; the
		// per-stage span both links the chain and times this worker's handling.
		ctx = tracing.ExtractNATS(ctx, msg.Header)
		// Enrich the context so every log emitted while handling this message
		// carries tenant_id / pipeline_id / connection_id (+ trace_id) (#91).
		ctx = logging.ContextWith(ctx, env.TenantID, env.IntegrationID)
		ctx, span := tracing.Tracer("sdk").Start(ctx, "consume "+durable,
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("vrsky.worker", durable),
				attribute.String("vrsky.tenant_id", env.TenantID),
				attribute.String("vrsky.connection_id", env.IntegrationID),
				attribute.String("vrsky.message_id", env.ID),
				attribute.String("messaging.source", msg.Subject),
			),
		)
		defer span.End()

		// Rehydrate an offloaded payload (claim-check) before the connector sees
		// it. No-op for inline payloads. A transient store error is returned as
		// retriable so the message is redelivered rather than delivered empty.
		// Capture the ref first — rehydrate clears it — so we can reclaim the
		// object once this hop has consumed it.
		spillRef := env.PayloadRef
		if rerr := rehydrate(ctx, res.payloadStore, env); rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
			return rerr
		}

		derr := deliver(ctx, env)
		if derr == nil {
			// Delivered/republished — the inbound spill object is now orphaned;
			// reclaim it eagerly (best-effort; TTL backstops the rest).
			cleanupSpill(ctx, res.payloadStore, spillRef, logger)
			return nil
		}
		span.RecordError(derr)
		span.SetStatus(codes.Error, derr.Error())
		if IsPermanent(derr) {
			logger.Warn("permanent error; dropping message", "envelope_id", env.ID, "error", derr)
			return nil // ack — poison message
		}
		return derr // Retriable / bare → NAK → backoff → DLQ
	})
	if err != nil {
		return nil, err
	}
	return sub.Stop, nil
}

// republish forwards an envelope to the data stream with a FRESH envelope ID.
// Reusing the inbound ID would be dropped by JetStream's dedup window (and by
// downstream per-ID dedup) — the well-known filter/converter footgun.
func republish(ctx context.Context, pub *messaging.Publisher, res *Resources, env *envelope.Envelope) error {
	env.ID = uuid.NewString()
	// Re-offload if the transformed payload is over threshold (claim-check), so a
	// filter/converter output that grew large still stays under NATS max_payload.
	if _, err := offloadIfLarge(ctx, res.payloadStore, env, res.inlineMaxBytes, res.Logger); err != nil {
		return err
	}
	body, err := envelope.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	return pub.Publish(ctx, env.TenantID, env.IntegrationID, env.ID, body)
}

// --- env / wiring helpers ---

func newLogger(name string) *slog.Logger {
	// Structured JSON to stdout with the platform's standard fields (#91); the
	// context-aware handler stamps trace_id/tenant_id/pipeline_id/connection_id
	// when the call uses a ctx from logging.ContextWith / carrying a span.
	return logging.New(name)
}

func connectNATS(logger *slog.Logger) (*nats.Conn, error) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = "nats://localhost:4222"
	}
	// Service discovery (#21): if MGMT_API_URL + TENANT_ID are set, resolve the
	// tenant's NATS instances and dial all of them (comma-separated). nats.Connect
	// load-balances + fails over across the set and reconnects automatically.
	// Any discovery hiccup falls back to NATS_URL, so compose/dev is unaffected.
	if disc := natsdiscovery.New(os.Getenv("MGMT_API_URL"), os.Getenv("TENANT_ID"), os.Getenv("CONNECTION_ID"), os.Getenv("SERVICE_TOKEN")); disc.Enabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		joined, err := disc.ResolveJoined(ctx)
		cancel()
		if err != nil {
			logger.Warn("NATS discovery failed; falling back to NATS_URL", "error", err)
		} else if joined != "" {
			logger.Info("NATS discovery resolved tenant instances", "servers", joined)
			url = joined
		}
	}
	return nats.Connect(url,
		nats.ReconnectWait(100*time.Millisecond),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { logger.Warn("NATS disconnected", "error", err) }),
		nats.ReconnectHandler(func(nc *nats.Conn) { logger.Info("NATS reconnected", "url", nc.ConnectedUrl()) }),
	)
}

func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	// Pool sizing is lean by DEFAULT because of the pod-per-connection model:
	// each connection runs its own worker pod, and every worker holds its own
	// pool against the single shared Postgres. A worker's DB work is light and
	// sequential (load config on start, throttled last_payload writes), so a
	// small ceiling is plenty — and 6 open × N workers is what keeps the total
	// under Postgres `max_connections` as connection count grows. ConnMaxIdleTime
	// reaps connections that idle pollers would otherwise pin indefinitely.
	// All tunable via env for per-deployment sizing (e.g. heavier control-plane).
	db.SetMaxOpenConns(envInt("DB_MAX_OPEN_CONNS", 6))
	db.SetMaxIdleConns(envInt("DB_MAX_IDLE_CONNS", 2))
	db.SetConnMaxLifetime(time.Duration(envInt("DB_CONN_MAX_LIFETIME_SECONDS", 300)) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(envInt("DB_CONN_MAX_IDLE_SECONDS", 90)) * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// shutdownDrain is how long the runner waits after flipping readiness to
// not-ready (so K8s de-registers the pod) before it stops consuming. Tunable
// via SHUTDOWN_DRAIN_SECONDS (default 5s; set 0 to disable, e.g. in tests).
func shutdownDrain() time.Duration {
	return time.Duration(envInt("SHUTDOWN_DRAIN_SECONDS", 5)) * time.Second
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// startAuxHTTP serves any connector-registered handlers on WORKER_HTTP_PORT
// (or FILE_PRODUCER_HTTP_PORT as a back-compat fallback). Returns a stop func.
// No-op when nothing is registered.
func startAuxHTTP(routes []httpRoute, logger *slog.Logger) func() {
	if len(routes) == 0 {
		return func() {}
	}
	port := os.Getenv("WORKER_HTTP_PORT")
	if port == "" {
		port = os.Getenv("FILE_PRODUCER_HTTP_PORT")
	}
	if port == "" {
		port = "9900"
	}
	mux := http.NewServeMux()
	for _, r := range routes {
		// Wrap each route so an inbound request (e.g. the webhook ingress or a
		// file upload) starts/continues a trace; the SDK publish closure then
		// hangs the producer span under it. No-op when tracing is disabled.
		mux.Handle(r.pattern, otelhttp.NewHandler(r.handler, r.pattern))
	}
	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		logger.Info("auxiliary HTTP server started", "port", port, "routes", len(routes))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("auxiliary HTTP server error", "error", err)
		}
	}()
	return func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
}
