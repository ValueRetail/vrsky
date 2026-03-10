package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ValueRetail/vrsky/pkg/converter"
	"github.com/ValueRetail/vrsky/pkg/health"
	"github.com/ValueRetail/vrsky/pkg/runtime"
	"github.com/nats-io/nats.go"
)

func main() {
	// Setup logging
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	logger := converter.SetupLogger(logLevel)

	logger.InfoContext(context.Background(), "Starting VRSky converter component")

	// Try to load new K8s runtime config first
	rtCfg, err := runtime.LoadFromEnv()
	if err != nil {
		logger.ErrorContext(context.Background(), "Failed to load runtime config", "error", err)
		os.Exit(1)
	}

	// Check if we're running in K8s mode (new env vars) or legacy mode
	if rtCfg.NodeID != "" && rtCfg.TenantID != "" && rtCfg.ConnectionID != "" {
		// New K8s mode - use runtime config
		runK8sMode(logger, rtCfg)
	} else {
		// Legacy mode - use existing CONVERTER_ID/TENANT_ID approach
		runLegacyMode(logger)
	}
}

// runK8sMode runs the converter with K8s orchestrator-injected configuration
func runK8sMode(logger *slog.Logger, rtCfg *runtime.Config) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger.InfoContext(ctx, "Running in K8s mode",
		"tenant_id", rtCfg.TenantID,
		"connection_id", rtCfg.ConnectionID,
		"node_id", rtCfg.NodeID,
		"input_subject", rtCfg.InputNATSSubject,
		"output_subject", rtCfg.OutputNATSSubject)

	// Validate configuration
	if err := rtCfg.Validate(); err != nil {
		logger.ErrorContext(ctx, "Invalid configuration", "error", err)
		os.Exit(1)
	}

	// Start health server using the shared package
	healthServer := health.NewServer(health.Config{
		Port:        rtCfg.HealthPort,
		ComponentID: "converter",
		NodeID:      rtCfg.NodeID,
		Logger:      logger,
	})

	// Mark as not ready until converter is started
	healthServer.SetReady(false)

	if err := healthServer.Start(ctx); err != nil {
		logger.ErrorContext(ctx, "Failed to start health server", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = healthServer.Stop(shutdownCtx)
	}()

	// Connect to NATS
	natsAddrs := strings.Split(rtCfg.NATSURLs, ",")
	logger.InfoContext(ctx, "Connecting to NATS", "addresses", natsAddrs)

	natsConn, err := nats.Connect(strings.Join(natsAddrs, ","),
		nats.Name("converter-"+rtCfg.NodeID),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		logger.ErrorContext(ctx, "Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer natsConn.Close()

	logger.InfoContext(ctx, "Connected to NATS", "server_id", natsConn.ConnectedServerId())

	// Use NODE_ID as converter_id for the converter package
	// The converter package will load config from config service using tenant_id and converter_id
	conv, err := converter.NewConverter(ctx, rtCfg.NodeID, rtCfg.TenantID, natsConn, logger)
	if err != nil {
		logger.ErrorContext(ctx, "Failed to create converter", "error", err)
		os.Exit(1)
	}

	// Start converter
	logger.InfoContext(ctx, "Starting converter component")
	if err := conv.Start(ctx); err != nil {
		logger.ErrorContext(ctx, "Failed to start converter", "error", err)
		os.Exit(1)
	}

	// Mark as ready
	healthServer.SetReady(true)
	logger.InfoContext(ctx, "Converter started successfully and ready for traffic")

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigChan
	logger.InfoContext(ctx, "Received signal, shutting down", "signal", sig.String())

	// Mark as not ready
	healthServer.SetReady(false)

	// Graceful shutdown
	logger.InfoContext(ctx, "Stopping converter (30s timeout)")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := conv.Stop(shutdownCtx); err != nil {
		logger.ErrorContext(shutdownCtx, "Error stopping converter", "error", err)
		os.Exit(1)
	}

	logger.InfoContext(context.Background(), "Converter stopped successfully")
}

// runLegacyMode runs the converter with existing CONVERTER_ID/TENANT_ID environment variables
func runLegacyMode(logger *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load environment variables
	converterID := os.Getenv("CONVERTER_ID")
	if converterID == "" {
		logger.ErrorContext(ctx, "CONVERTER_ID not set")
		os.Exit(1)
	}

	tenantID := os.Getenv("TENANT_ID")
	if tenantID == "" {
		logger.ErrorContext(ctx, "TENANT_ID not set")
		os.Exit(1)
	}

	natsURLs := os.Getenv("NATS_URLS")
	if natsURLs == "" {
		natsURLs = "nats://nats:4222"
	}

	logger.InfoContext(ctx, "Running in legacy mode",
		"converter_id", converterID,
		"tenant_id", tenantID,
		"nats_urls", natsURLs,
	)

	// Connect to NATS
	natsAddrs := strings.Split(natsURLs, ",")
	logger.InfoContext(ctx, "Connecting to NATS", "addresses", natsAddrs)

	natsConn, err := nats.Connect(strings.Join(natsAddrs, ","),
		nats.Name("converter-"+converterID),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		logger.ErrorContext(ctx, "Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer natsConn.Close()

	logger.InfoContext(ctx, "Connected to NATS", "server_id", natsConn.ConnectedServerId())

	// Start health server for Kubernetes probes (port 8080)
	healthServer := health.NewServer(health.Config{
		Port:        8080,
		ComponentID: "converter",
		NodeID:      converterID,
		Logger:      logger,
	})

	healthServer.SetReady(false)
	if err := healthServer.Start(ctx); err != nil {
		logger.ErrorContext(ctx, "Failed to start health server", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = healthServer.Stop(shutdownCtx)
	}()

	// Create converter instance
	conv, err := converter.NewConverter(ctx, converterID, tenantID, natsConn, logger)
	if err != nil {
		logger.ErrorContext(ctx, "Failed to create converter", "error", err)
		os.Exit(1)
	}

	// Start converter
	logger.InfoContext(ctx, "Starting converter component")
	if err := conv.Start(ctx); err != nil {
		logger.ErrorContext(ctx, "Failed to start converter", "error", err)
		os.Exit(1)
	}

	// Mark as ready
	healthServer.SetReady(true)
	logger.InfoContext(ctx, "Converter started successfully")

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigChan
	logger.InfoContext(ctx, "Received signal, shutting down", "signal", sig.String())

	// Mark as not ready
	healthServer.SetReady(false)

	// Graceful shutdown
	logger.InfoContext(ctx, "Stopping converter (30s timeout)")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := conv.Stop(shutdownCtx); err != nil {
		logger.ErrorContext(shutdownCtx, "Error stopping converter", "error", err)
		os.Exit(1)
	}

	logger.InfoContext(context.Background(), "Converter stopped successfully")
}
