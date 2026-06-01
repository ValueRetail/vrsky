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
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/health"
	"github.com/ValueRetail/vrsky/pkg/messaging"
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
	disableHealth bool
}

// WithNATSConn supplies an already-connected NATS connection instead of
// dialing NATS_URL. The runner will not close a caller-supplied connection.
func WithNATSConn(nc *nats.Conn) RunOption { return func(o *runOptions) { o.nc = nc } }

// WithDB supplies a database handle instead of opening DATABASE_URL. The
// runner will not close a caller-supplied DB.
func WithDB(db *sql.DB) RunOption { return func(o *runOptions) { o.db = db } }

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
			return subscribeDispatch(js, name, res.Logger, func(c context.Context, env *envelope.Envelope) error {
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
			publish := func(pctx context.Context, env *envelope.Envelope) error {
				body, err := envelope.Marshal(env)
				if err != nil {
					return fmt.Errorf("marshal envelope: %w", err)
				}
				return pub.Publish(pctx, env.TenantID, env.IntegrationID, env.ID, body)
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
			return subscribeDispatch(js, name, res.Logger, func(c context.Context, env *envelope.Envelope) error {
				keep, out, err := f.Evaluate(c, env)
				if err != nil {
					return err
				}
				if !keep || out == nil {
					return nil // dropped — ack
				}
				return republish(c, pub, out)
			})
		}, opts...)
}

// RunConverter boots a converter connector: subscribe → Convert → forward the
// transformed envelope (fresh ID).
func RunConverter(ctx context.Context, name string, cv Converter, opts ...RunOption) error {
	return run(ctx, name, cv,
		func(ctx context.Context, res *Resources) error { return cv.Configure(ctx, res) },
		func(ctx context.Context, js nats.JetStreamContext, pub *messaging.Publisher, res *Resources) (func(), error) {
			return subscribeDispatch(js, name, res.Logger, func(c context.Context, env *envelope.Envelope) error {
				out, err := cv.Convert(c, env)
				if err != nil {
					return err
				}
				if out == nil {
					return nil
				}
				return republish(c, pub, out)
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

	// Health/metrics server (skippable in tests).
	var setReady func(bool)
	if !o.disableHealth {
		hsrv := health.NewServer(health.Config{
			Port:        envInt("HEALTH_PORT", 8080),
			ComponentID: name,
			Logger:      logger,
		})
		if err := hsrv.Start(ctx); err != nil {
			return fmt.Errorf("start health server: %w", err)
		}
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = hsrv.Stop(sctx)
		}()
		setReady = hsrv.SetReady
	}

	res := &Resources{
		Logger: logger,
		DB:     db,
		Health: &healthToggle{setReady: setReady},
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
	select {
	case <-runCtx.Done():
	case <-sigCh:
		logger.Info("shutdown signal received")
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
	logger.Info("connector stopped", "name", name)
	return nil
}

// subscribeDispatch wires a durable JetStream subscription whose handler
// unmarshals the envelope and calls deliver, mapping the SDK error classes
// onto messaging's NAK/DLQ semantics: nil → ack; Permanent → ack+log (poison);
// anything else → NAK (messaging retries with backoff, then DLQs).
func subscribeDispatch(js nats.JetStreamContext, durable string, logger *slog.Logger, deliver func(context.Context, *envelope.Envelope) error) (func(), error) {
	sub, err := messaging.Subscribe(js, messaging.SubscriberOpts{
		DurableName: durable,
		AckWait:     45 * time.Second,
		Logger:      logger,
	}, func(ctx context.Context, msg *nats.Msg) error {
		env, err := envelope.Unmarshal(msg.Data)
		if err != nil {
			// Malformed payload will never parse — drop it (ack) rather than
			// burn the retry budget.
			logger.Error("drop unparseable envelope", "subject", msg.Subject, "error", err)
			return nil
		}
		derr := deliver(ctx, env)
		if derr == nil {
			return nil
		}
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
func republish(ctx context.Context, pub *messaging.Publisher, env *envelope.Envelope) error {
	env.ID = uuid.NewString()
	body, err := envelope.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	return pub.Publish(ctx, env.TenantID, env.IntegrationID, env.ID, body)
}

// --- env / wiring helpers ---

func newLogger(name string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})).
		With("service", name)
}

func connectNATS(logger *slog.Logger) (*nats.Conn, error) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = "nats://localhost:4222"
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
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
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
		mux.Handle(r.pattern, r.handler)
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
