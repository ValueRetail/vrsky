package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ValueRetail/vrsky/pkg/io"
)

func main() {
	// Setup logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Create PostgreSQL producer
	producer, err := io.NewPostgresOutput(logger, prometheus.DefaultRegisterer)
	if err != nil {
		logger.Error("Failed to create PostgreSQL producer", "error", err)
		os.Exit(1)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start producer
	if err := producer.Start(ctx); err != nil {
		logger.Error("Failed to start producer", "error", err)
		os.Exit(1)
	}

	logger.Info("PostgreSQL producer started successfully")

	// Get metrics port from environment (default: 9091)
	// Producer uses 9091 to avoid conflicts with consumer (9090) when running locally
	metricsPort := "9091"
	if port := os.Getenv("POSTGRES_PRODUCER_METRICS_PORT"); port != "" {
		// Validate port is a valid number
		if _, err := strconv.Atoi(port); err == nil {
			metricsPort = port
		} else {
			logger.Warn("Invalid POSTGRES_PRODUCER_METRICS_PORT, using default", "port", port)
		}
	}

	// Create metrics HTTP server with explicit ServeMux and handler
	metricsMux := http.NewServeMux()
	metricsHandler, err := io.GetMetricsHandler(prometheus.DefaultGatherer)
	if err != nil {
		logger.Error("Failed to create metrics handler", "error", err)
		os.Exit(1)
	}
	metricsMux.Handle("/metrics", metricsHandler)

	metricsAddr := fmt.Sprintf(":%s", metricsPort)
	metricsServer := &http.Server{
		Addr:         metricsAddr,
		Handler:      metricsMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start metrics server in background
	metricsServerErr := make(chan error, 1)
	go func() {
		logger.Info("Starting metrics server", "addr", metricsAddr)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			metricsServerErr <- fmt.Errorf("metrics server error: %w", err)
		}
	}()

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal or metrics server error
	select {
	case sig := <-sigChan:
		logger.Info("Received shutdown signal", "signal", sig)
	case err := <-metricsServerErr:
		logger.Error("Metrics server failed", "error", err)
		cancel()
	}

	// Give pending operations a moment to complete before closing
	time.Sleep(500 * time.Millisecond)

	// Gracefully shutdown metrics server (5 second timeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("Failed to shutdown metrics server", "error", err)
	} else {
		logger.Info("Metrics server stopped")
	}

	// Cancel context to stop the producer
	cancel()

	// Close producer gracefully
	if err := producer.Close(); err != nil {
		logger.Error("Failed to close producer", "error", err)
		os.Exit(1)
	}

	logger.Info("PostgreSQL producer stopped",
		"total_written", producer.GetWritten())
}
