package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v4/stdlib"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/logging"
	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"github.com/ValueRetail/vrsky/pkg/oauth"
	"github.com/ValueRetail/vrsky/pkg/orchestrator"
	"github.com/ValueRetail/vrsky/pkg/promquery"
	"github.com/ValueRetail/vrsky/pkg/tracing"
)

// fatal logs a fatal startup error at ERROR level (so {level="error"} queries
// and panels catch it) and exits. Used instead of log.Fatalf, whose records
// would otherwise be tagged level=info by the slog-backed adapter.
func fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "error", err)
	os.Exit(1)
}

func main() {
	// Structured JSON logging (#91): back the existing *log.Logger threading
	// with a slog JSON handler so every log.Printf/Fatalf call site emits
	// JSON tagged service=management-api (and trace_id via the context handler
	// where a ctx is available), shippable to Loki. The HTTP access log is
	// fully structured separately in LoggingMiddleware.
	appLog := logging.New("management-api")
	logger := slog.NewLogLogger(appLog.Handler(), slog.LevelInfo)

	// Load configuration
	config := LoadConfig()
	if err := config.Validate(); err != nil {
		fatal(appLog, "invalid configuration", err)
	}

	// Fail fast if the secrets master key is missing or malformed.
	// All credential encrypt/decrypt operations require ENCRYPTION_KEY.
	keyHex, err := crypto.Key()
	if err != nil {
		fatal(appLog, "ENCRYPTION_KEY is not configured", err)
	}
	// Warn loudly when the dev key is in use — easy to miss in a real
	// deployment that copied docker-compose.yml as a starting point. The
	// canonical dev key is the literal repeated nibbles "0123456789abcdef".
	if keyHex == "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		appLog.Warn("ENCRYPTION_KEY is the documented dev value; generate a real key for any non-local environment (openssl rand -hex 32)")
	}

	logger.Printf("Starting %s v%s", config.ServiceName, config.Version)
	logger.Printf("Listening on %s", config.ListenAddr)

	// Distributed tracing (no-op unless OTEL_EXPORTER_OTLP_ENDPOINT is set).
	if shutdownTracing, terr := tracing.Init(context.Background(), config.ServiceName); terr != nil {
		appLog.Warn("tracing init failed; continuing without tracing", "error", terr)
	} else {
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownTracing(sctx)
		}()
	}

	// Initialize database connection
	db, err := initDatabase(config.DatabaseURL, logger)
	if err != nil {
		fatal(appLog, "failed to initialize database", err)
	}
	defer db.Close()

	// Initialize NATS connection
	nc, err := initNATS(config.NATSUrl, logger)
	if err != nil {
		fatal(appLog, "failed to initialize NATS", err)
	}
	defer nc.Close()

	// Setup HTTP server with graceful shutdown
	var shuttingDown atomic.Bool
	server, tenantProvisioner := setupServer(config, db, nc, logger, &shuttingDown)

	// TLS cert-expiry gauge for the CertExpirySoon alert (#84). No-op unless
	// TLS_CERT_PATHS is set.
	watchCertExpiry(logger)

	// Start server in a goroutine
	serverErrs := make(chan error, 1)
	go func() {
		logger.Printf("HTTP server started on %s", config.ListenAddr)
		serverErrs <- server.ListenAndServe()
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrs:
		if err != nil && err != http.ErrServerClosed {
			fatal(appLog, "server error", err)
		}
	case sig := <-sigChan:
		logger.Printf("Received signal: %v, shutting down...", sig)

		// Flip /readyz to not-ready and wait a short drain window so Kubernetes
		// removes this pod from Service endpoints before the HTTP server stops
		// accepting connections (zero dropped in-flight requests on rolling
		// deploy). Tunable via SHUTDOWN_DRAIN_SECONDS (default 5s).
		shuttingDown.Store(true)
		if d := shutdownDrainSeconds(); d > 0 {
			logger.Printf("draining for %s before shutdown", d)
			time.Sleep(d)
		}

		// Graceful shutdown with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			appLog.Error("error during shutdown", "error", err)
		}

		// Stop the tenant provisioner after the HTTP server has drained, so no
		// new provisioning jobs are enqueued mid-drain. Stop() blocks until the
		// worker finishes any in-flight job and empties the queue.
		tenantProvisioner.Stop()

		logger.Printf("Server stopped")
	}
}

// initDatabase initializes PostgreSQL connection pool
func initDatabase(dbURL string, logger *log.Logger) (*sql.DB, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	logger.Printf("Database connected successfully")
	return db, nil
}

// initNATS initializes NATS connection
func initNATS(natsURL string, logger *log.Logger) (*nats.Conn, error) {
	nc, err := nats.Connect(
		natsURL,
		nats.ReconnectWait(100*time.Millisecond),
		nats.MaxReconnects(-1), // Infinite reconnect attempts
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			logger.Printf("NATS disconnected: %v", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Printf("NATS reconnected to %s", nc.ConnectedUrl())
		}),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	logger.Printf("NATS connected to %s", nc.ConnectedUrl())
	return nc, nil
}

// setupServer creates and configures the HTTP server. It also returns the
// tenant provisioner so main() can drive its lifecycle (Stop on shutdown);
// its background worker must outlive this function, so it must not be stopped
// via defer here.
func setupServer(config *Config, db *sql.DB, nc *nats.Conn, logger *log.Logger, shuttingDown *atomic.Bool) (*http.Server, *managementapi.TenantProvisioner) {
	mux := http.NewServeMux()

	// Health check endpoints. /healthz + /readyz are the canonical Kubernetes
	// probe paths; /health + /ready remain as backward-compatible aliases.
	liveness := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "healthy",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	}

	readiness := func(w http.ResponseWriter, r *http.Request) {
		// Check dependencies
		deps := map[string]string{
			"database": "ok",
			"nats":     "ok",
		}

		// Determine HTTP status based on dependencies
		statusCode := http.StatusOK
		status := "ready"

		// During graceful shutdown, report not-ready so Kubernetes removes this
		// pod from Service endpoints before the HTTP server drains.
		if shuttingDown.Load() {
			statusCode = http.StatusServiceUnavailable
			status = "shutting down"
		}

		// Verify database connectivity
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			deps["database"] = "error: " + err.Error()
			statusCode = http.StatusServiceUnavailable
			status = "not ready"
		}

		// Verify NATS connectivity
		if !nc.IsConnected() {
			deps["nats"] = "error: not connected"
			statusCode = http.StatusServiceUnavailable
			status = "not ready"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       status,
			"timestamp":    time.Now().UTC().Format(time.RFC3339),
			"dependencies": deps,
			"service_name": config.ServiceName,
			"version":      config.Version,
		})
	}

	mux.HandleFunc("/health", liveness)
	mux.HandleFunc("/healthz", liveness)
	mux.HandleFunc("/ready", readiness)
	mux.HandleFunc("/readyz", readiness)

	// Prometheus scrape endpoint (#84) — request counters/durations from
	// MetricsMiddleware + the TLS cert-expiry gauge. Exempt from tenant
	// middleware in cors.go (Prometheus sends no X-Tenant-ID).
	mux.Handle("GET /metrics", promhttp.Handler())

	// Initialize repository and validator
	repo := managementapi.NewPostgresRepository(db)
	validator := managementapi.NewValidator()
	publisher := managementapi.NewNATSPublisher(nc, logger)

	// Initialize handler and register REST routes
	restHandler := managementapi.NewHandler(repo, validator)
	restHandler.SetPublisher(publisher)
	restHandler.SetDB(db)

	// JetStream context for DLQ endpoints (#70). Optional — DLQ handlers
	// return 503 if JS is unconfigured. Stream creation is lazy (workers
	// call EnsureStreams on first publish/subscribe).
	if js, err := nc.JetStream(); err != nil {
		logger.Printf("WARNING: JetStream context unavailable: %v", err)
	} else {
		restHandler.SetJetStream(js)
	}

	// Initialize tenant NATS provisioning (Phase 2). The K8s client is shared
	// with the per-connection orchestrator below (#157) so we build it once.
	k8sClient := initK8sClient(logger)
	k8sProvisioner := initK8sNATSProvisioner(k8sClient, logger)
	tenantSSEHub := managementapi.NewTenantSSEHub()
	tenantProvisioner := managementapi.NewTenantProvisioner(repo, k8sProvisioner, tenantSSEHub, logger)
	// NB: no `defer Stop()` here — setupServer returns long before the process
	// exits, so a deferred Stop would kill the provisioner immediately (it
	// started and stopped in the same instant, leaving provisioning dead on
	// arrival — same bug class as the OAuth refresher below, fixed in cf534c8).
	// Instead the provisioner is returned to main(), which calls Stop() on the
	// graceful-shutdown path so the worker can drain in-flight jobs.
	tenantProvisioner.Start()
	restHandler.SetTenantProvisioner(tenantProvisioner)
	restHandler.SetTenantSSEHub(tenantSSEHub)

	// #157: per-connection K8s orchestrator. When enabled, starting a graph
	// connection deploys a Deployment + HorizontalPodAutoscaler per node (the
	// autoscaling generated by #135); stopping it tears them down. Without this
	// the handler's orchestratorFactory is nil and StartConnection only
	// publishes a NATS start command — i.e. no worker pods in K8s.
	//
	// Opt-in via ORCHESTRATOR_MODE=k8s so the single-host compose stack keeps
	// using its static workers + NATS start/stop commands even on a developer
	// machine that happens to have a kubeconfig. In k8s, set it in the
	// deployment env.
	if os.Getenv("ORCHESTRATOR_MODE") == "k8s" {
		if k8sClient == nil {
			logger.Printf("WARNING: ORCHESTRATOR_MODE=k8s but no K8s client is available; per-connection orchestrator disabled")
		} else {
			orchConfig := orchestratorConfigFromEnv(config)
			// Point per-connection workers at the connection's placed NATS
			// instance (#19) when one exists; otherwise the static config NATS
			// URL above. The pkg/runtime workers the orchestrator deploys can't
			// self-discover, so this resolution happens at deploy time.
			natsResolver := func(ctx context.Context, tenantID, connID string) (string, bool) {
				inst, err := repo.GetConnectionInstance(ctx, tenantID, connID)
				if err != nil || inst == nil {
					return "", false
				}
				return inst.NATSURL(), true
			}
			restHandler.SetOrchestratorFactory(orchestrator.NewOrchestratorFactory(
				k8sClient, orchConfig, validator, orchestrator.WithNATSURLResolver(natsResolver)))
			logger.Printf("per-connection K8s orchestrator enabled (namespace=%s registry=%s version=%s nats=%s)",
				orchConfig.Namespace, orchConfig.ImageRegistry, orchConfig.ImageVersion, orchConfig.NATSURLs)
			// Safety net: periodically delete worker Deployments/HPAs whose
			// connection no longer exists (teardown failed or predates #176).
			go runOrphanedWorkerGC(context.Background(), k8sClient, orchConfig.Namespace, repo, logger)
		}
	}

	// Phase 3: Data sharing rate limiter
	rateLimiter := managementapi.NewConnectionRateLimiter()
	restHandler.SetRateLimiter(rateLimiter)

	// Phase 2A (#75): OAuth 2.0 framework. The repo implements oauth.Store,
	// so the client can be built directly off it. The refresher gets a small
	// tenant-lookup function so ticker-scan jobs (which are global) can
	// resolve a grant's tenant before calling Client.Refresh.
	oauthClient := oauth.New(repo, oauth.DefaultRegistry())
	restHandler.SetOAuthClient(oauthClient)
	// WithRefresherDB makes refresh cluster-wide-single under N replicas (#138):
	// a grant is refreshed only by the replica that wins its advisory lock.
	oauthRefresher := managementapi.NewOAuthRefresher(oauthClient, repo, managementapi.WithRefresherDB(db))
	oauthRefresher.SetTenantLookup(func(ctx context.Context, grantID string) (string, error) {
		var tenantID string
		// lint:tenant-ok — resolving the row's own tenant by grant PK; outer
		// caller verifies tenant on every subsequent operation.
		const q = `SELECT tenant_id FROM oauth_grants WHERE id = $1`
		if err := db.QueryRowContext(ctx, q, grantID).Scan(&tenantID); err != nil {
			return "", err
		}
		return tenantID, nil
	})
	// NB: no `defer Stop()` here — setupServer returns long before the process
	// exits, so a deferred Stop would kill the refresher immediately (the
	// tenantProvisioner above has this latent bug). The refresher runs for the
	// process lifetime; its ticker + worker goroutines are reclaimed on exit.
	oauthRefresher.Start()
	restHandler.SetOAuthRefresher(oauthRefresher)

	// Phase 4A (#92): per-tenant usage metering. An hourly rollup snapshots
	// message + deploy counts (from Prometheus) and storage (from tenant_quotas)
	// into usage_daily, which the /usage API + CSV export read. PROMETHEUS_URL
	// unset → storage-only rollup (counts stay 0). Like the OAuth refresher, no
	// deferred Stop — it runs for the process lifetime.
	var promClient *promquery.Client
	if promURL := os.Getenv("PROMETHEUS_URL"); promURL != "" {
		promClient = promquery.New(promURL, nil)
	} else {
		slog.Warn("PROMETHEUS_URL unset; usage rollup will record storage only (no message/deploy counts)")
	}
	// WithRollupDB gates the hourly rollup to one replica per tick under N
	// replicas (#138); upserts are idempotent, so this is a contention guard.
	managementapi.NewUsageRollup(repo, promClient, managementapi.WithRollupDB(db)).Start()

	// Tenant NATS health monitor (#21): probe each instance's monitoring
	// endpoint and flip active/unhealthy so the discovery API stops handing out
	// dead instances. Advisory-lock-gated (db) so only one replica probes.
	managementapi.NewNATSHealthMonitor(repo, db, slog.Default()).Start()

	// Tenant NATS autoscaler (#19): scrape per-instance load, provision a new
	// instance past capacity, decommission empty extras. Advisory-lock-gated.
	// Inert without a K8s provisioner (compose); scale actions only run in a
	// real cluster. The 80%-capacity signal is exported as a Prometheus gauge
	// (vrsky_nats_instance_capacity_pct) for Alertmanager (#84).
	natsAutoscaler := managementapi.NewNATSAutoscaler(repo, k8sProvisioner, db, slog.Default()).
		WithSlugLookup(func(ctx context.Context, tenantID string) (string, error) {
			var slug string
			// lint:tenant-ok — resolving a tenant's own slug by PK for provisioning.
			const q = `SELECT slug FROM tenants WHERE id = $1`
			if err := db.QueryRowContext(ctx, q, tenantID).Scan(&slug); err != nil {
				return "", err
			}
			return slug, nil
		})
	natsAutoscaler.Start()
	// Phase 4D (#95): the public status page reads the same Prometheus client.
	restHandler.SetPrometheus(promClient)

	restHandler.RegisterRoutes(mux)

	// Gateway rate limiting (#90): when TRAEFIK_DYNAMIC_DIR is set, seed the
	// per-tenant rate-limit config now and refresh it on every plan change so
	// the edge limit tracks the subscription plan (no Traefik restart).
	if dynDir := os.Getenv("TRAEFIK_DYNAMIC_DIR"); dynDir != "" {
		gatewaySync := func(ctx context.Context) error {
			plans, err := repo.ListTenantPlans(ctx)
			if err != nil {
				return err
			}
			return writeTenantGatewayConfig(dynDir, plans)
		}
		seedCtx, seedCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := gatewaySync(seedCtx); err != nil {
			logger.Printf("WARNING: gateway rate-limit config seed failed: %v", err)
		} else {
			logger.Printf("gateway rate-limit config seeded to %s", dynDir)
		}
		seedCancel()
		restHandler.SetGatewaySync(gatewaySync)
	}

	// Initialize WebSocket infrastructure for real-time metrics streaming
	clientRegistry := managementapi.NewClientRegistry()
	metricsCache := managementapi.NewMetricsCache(5 * time.Minute)

	// Register ClientRegistry as a listener so metrics updates are broadcast to WebSocket clients
	metricsCache.AddListener(clientRegistry)

	// Wire WebSocket support into handler
	restHandler.InitializeWebSocketSupport(clientRegistry, metricsCache)

	// Initialize metrics subscriber (subscriptions are per-tenant, created when connections are registered)
	_ = managementapi.NewNATSSubscriber(nc, metricsCache, logger)

	// TODO: Add test data generator
	// - POST /api/v1/connections/{id}/test-message
	// - POST /api/v1/connections/{id}/auto-generator/start
	// - POST /api/v1/connections/{id}/auto-generator/stop
	// - GET /api/v1/connections/{id}/auto-generator/status

	// Wrap mux with middleware (applied in reverse order — innermost is rightmost).
	// Audit must sit INSIDE TenantID so it can read the tenant from context,
	// and OUTSIDE the route mux so it observes every handler.
	//   Logging → Metrics → CORS → TenantID → Audit → Routes
	var handler http.Handler = mux
	handler = managementapi.AuditMiddleware(repo, logger)(handler)
	handler = TenantIDMiddleware(config.TenantHeader)(handler)
	handler = CORSMiddleware(config.CORSOrigins, config.TenantHeader)(handler)
	handler = MetricsMiddleware(handler)
	handler = LoggingMiddleware(logging.New("management-api"))(handler)
	// Outermost: start/continue the trace for every inbound API request (no-op
	// when tracing is disabled).
	handler = otelhttp.NewHandler(handler, "management-api")

	return &http.Server{
		Addr:         config.ListenAddr,
		Handler:      handler,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		IdleTimeout:  60 * time.Second,
	}, tenantProvisioner
}

// initK8sClient builds a Kubernetes client, trying in-cluster config first
// (running inside K8s) then a kubeconfig (local dev). Returns nil (not fatal)
// when neither is available. The client is shared by the tenant NATS
// provisioner and the per-connection orchestrator (#157).
func initK8sClient(logger *log.Logger) kubernetes.Interface {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			logger.Printf("K8s client not available (no in-cluster config or kubeconfig)")
			return nil
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Printf("Failed to create K8s client: %v", err)
		return nil
	}

	logger.Printf("K8s client initialized")
	return clientset
}

// initK8sNATSProvisioner returns a tenant NATS provisioner backed by the shared
// K8s client, or nil (not fatal) when no client is available — provisioning
// then falls back to the inert compose path.
func initK8sNATSProvisioner(k8sClient kubernetes.Interface, logger *log.Logger) *managementapi.K8sNATSProvisioner {
	if k8sClient == nil {
		logger.Printf("tenant NATS provisioning disabled (no K8s client)")
		return nil
	}
	return managementapi.NewK8sNATSProvisioner(k8sClient, logger)
}

// runOrphanedWorkerGC periodically deletes orchestrator worker Deployments/HPAs
// whose connection no longer exists in the DB — a safety net for connection
// deletes whose teardown failed or predates #176. Blocks until ctx is cancelled.
func runOrphanedWorkerGC(ctx context.Context, k8sClient kubernetes.Interface, namespace string, repo managementapi.Repository, logger *log.Logger) {
	const tick = 5 * time.Minute
	logger.Printf("orphaned-worker GC started (tick=%s namespace=%s)", tick, namespace)

	sweep := func() {
		// Bound each sweep so a hung DB/K8s call can't stall the loop forever
		// and starve subsequent ticks.
		sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()

		// exists reports whether a connection is still in the DB. On any non
		// not-found error it returns true, so a transient DB blip (or the
		// per-sweep timeout) never deletes a live worker.
		exists := func(connectionID string) bool {
			if _, err := repo.GetConnection(sctx, connectionID); err != nil {
				if _, ok := err.(*managementapi.NotFoundError); ok {
					return false
				}
				return true
			}
			return true
		}

		n, err := orchestrator.GarbageCollectOrphanedWorkers(sctx, k8sClient, namespace, exists)
		if err != nil {
			logger.Printf("orphaned-worker GC: %v", err)
			return
		}
		if n > 0 {
			logger.Printf("orphaned-worker GC: removed resources for %d orphaned connection(s)", n)
		}
	}

	sweep() // sweep once at startup
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

// orchestratorConfigFromEnv builds the per-connection orchestrator config (#157)
// from the environment, falling back to DefaultConfig values. Worker pods
// connect to the same NATS the management-api uses (the in-cluster platform
// NATS) unless WORKER_NATS_URL overrides it.
func orchestratorConfigFromEnv(config *Config) *orchestrator.OrchestratorConfig {
	c := orchestrator.DefaultConfig()
	// Default worker NATS to the management-api's own NATS URL (the platform
	// service in-cluster); WORKER_NATS_URL can point workers elsewhere.
	if config != nil && config.NATSUrl != "" {
		c.NATSURLs = config.NATSUrl
	}
	if v := os.Getenv("WORKER_NATS_URL"); v != "" {
		c.NATSURLs = v
	}
	if v := os.Getenv("ORCHESTRATOR_NAMESPACE"); v != "" {
		c.Namespace = v
	}
	if v := os.Getenv("WORKER_IMAGE_REGISTRY"); v != "" {
		c.ImageRegistry = v
	}
	if v := os.Getenv("WORKER_IMAGE_VERSION"); v != "" {
		c.ImageVersion = v
	}
	if v := os.Getenv("NATS_ACCOUNT"); v != "" {
		c.NATSAccount = v
	}
	return c
}

// shutdownDrainSeconds is how long to keep serving (with /readyz reporting
// not-ready) after a shutdown signal, so Kubernetes de-registers the pod before
// the HTTP server stops. Tunable via SHUTDOWN_DRAIN_SECONDS (default 5s).
func shutdownDrainSeconds() time.Duration {
	if v := os.Getenv("SHUTDOWN_DRAIN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 5 * time.Second
}
