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

	"github.com/ValueRetail/vrsky/pkg/managementapi"
)

func main() {
	// Setup logging
	logger := log.New(os.Stdout, "[MGMT-API] ", log.LstdFlags|log.Lshortfile)

	// Load configuration
	config := LoadConfig()
	if err := config.Validate(); err != nil {
		logger.Fatalf("Invalid configuration: %v", err)
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
	server := setupServer(config, db, nc, logger)

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

// setupServer creates and configures the HTTP server
func setupServer(config *Config, db *sql.DB, nc *nats.Conn, logger *log.Logger) *http.Server {
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
	restHandler.RegisterRoutes(mux)

	// TODO: Add WebSocket handler
	// - WS /ws/metrics
	// TODO: Add test data generator
	// - POST /api/v1/connections/{id}/test-message
	// - POST /api/v1/connections/{id}/auto-generator/start
	// - POST /api/v1/connections/{id}/auto-generator/stop
	// - GET /api/v1/connections/{id}/auto-generator/status

	// Wrap mux with middleware (applied in reverse order)
	var handler http.Handler = mux
	handler = LoggingMiddleware(logger)(handler)
	handler = CORSMiddleware(config.CORSOrigins)(handler)
	handler = TenantIDMiddleware(config.TenantHeader)(handler)

	return &http.Server{
		Addr:         config.ListenAddr,
		Handler:      handler,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		IdleTimeout:  60 * time.Second,
	}
}
