package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v4/stdlib"
	"github.com/nats-io/nats.go"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/managementapi"
	"github.com/ValueRetail/vrsky/pkg/oauth"
)

func main() {
	// Setup logging
	logger := log.New(os.Stdout, "[MGMT-API] ", log.LstdFlags|log.Lshortfile)

	// Load configuration
	config := LoadConfig()
	if err := config.Validate(); err != nil {
		logger.Fatalf("Invalid configuration: %v", err)
	}

	// Fail fast if the secrets master key is missing or malformed.
	// All credential encrypt/decrypt operations require ENCRYPTION_KEY.
	keyHex, err := crypto.Key()
	if err != nil {
		logger.Fatalf("ENCRYPTION_KEY is not configured: %v", err)
	}
	// Warn loudly when the dev key is in use — easy to miss in a real
	// deployment that copied docker-compose.yml as a starting point. The
	// canonical dev key is the literal repeated nibbles "0123456789abcdef".
	if keyHex == "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		logger.Printf("WARNING: ENCRYPTION_KEY is the documented dev value. " +
			"Generate a real key for any environment other than local development: openssl rand -hex 32")
	}

	logger.Printf("Starting %s v%s", config.ServiceName, config.Version)
	logger.Printf("Listening on %s", config.ListenAddr)

	// Initialize database connection
	db, err := initDatabase(config.DatabaseURL, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Initialize NATS connection
	nc, err := initNATS(config.NATSUrl, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize NATS: %v", err)
	}
	defer nc.Close()

	// Setup HTTP server with graceful shutdown
	server, tenantProvisioner := setupServer(config, db, nc, logger)

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
			logger.Fatalf("Server error: %v", err)
		}
	case sig := <-sigChan:
		logger.Printf("Received signal: %v, shutting down...", sig)

		// Graceful shutdown with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			logger.Printf("Error during shutdown: %v", err)
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
func setupServer(config *Config, db *sql.DB, nc *nats.Conn, logger *log.Logger) (*http.Server, *managementapi.TenantProvisioner) {
	mux := http.NewServeMux()

	// Health check endpoints
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "healthy",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		// Check dependencies
		deps := map[string]string{
			"database": "ok",
			"nats":     "ok",
		}

		// Determine HTTP status based on dependencies
		statusCode := http.StatusOK

		// Verify database connectivity
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			deps["database"] = "error: " + err.Error()
			statusCode = http.StatusServiceUnavailable
		}

		// Verify NATS connectivity
		if !nc.IsConnected() {
			deps["nats"] = "error: not connected"
			statusCode = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       "ready",
			"timestamp":    time.Now().UTC().Format(time.RFC3339),
			"dependencies": deps,
			"service_name": config.ServiceName,
			"version":      config.Version,
		})
	})

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

	// Initialize tenant NATS provisioning (Phase 2)
	k8sProvisioner := initK8sNATSProvisioner(logger)
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

	// Phase 3: Data sharing rate limiter
	rateLimiter := managementapi.NewConnectionRateLimiter()
	restHandler.SetRateLimiter(rateLimiter)

	// Phase 2A (#75): OAuth 2.0 framework. The repo implements oauth.Store,
	// so the client can be built directly off it. The refresher gets a small
	// tenant-lookup function so ticker-scan jobs (which are global) can
	// resolve a grant's tenant before calling Client.Refresh.
	oauthClient := oauth.New(repo, oauth.DefaultRegistry())
	restHandler.SetOAuthClient(oauthClient)
	oauthRefresher := managementapi.NewOAuthRefresher(oauthClient, repo)
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

	restHandler.RegisterRoutes(mux)

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
	//   Logging → CORS → TenantID → Audit → Routes
	var handler http.Handler = mux
	handler = managementapi.AuditMiddleware(repo, logger)(handler)
	handler = TenantIDMiddleware(config.TenantHeader)(handler)
	handler = CORSMiddleware(config.CORSOrigins, config.TenantHeader)(handler)
	handler = LoggingMiddleware(logger)(handler)

	return &http.Server{
		Addr:         config.ListenAddr,
		Handler:      handler,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		IdleTimeout:  60 * time.Second,
	}, tenantProvisioner
}

// initK8sNATSProvisioner tries to create a K8s client for tenant NATS provisioning.
// Returns nil (not fatal) if K8s is not available (e.g., local dev without a cluster).
func initK8sNATSProvisioner(logger *log.Logger) *managementapi.K8sNATSProvisioner {
	// Try in-cluster config first (running inside K8s)
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig (local dev)
		kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			logger.Printf("K8s client not available (no in-cluster config or kubeconfig): tenant NATS provisioning disabled")
			return nil
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Printf("Failed to create K8s client: %v — tenant NATS provisioning disabled", err)
		return nil
	}

	logger.Printf("K8s client initialized for tenant NATS provisioning")
	return managementapi.NewK8sNATSProvisioner(clientset, logger)
}
