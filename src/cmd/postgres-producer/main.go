package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ValueRetail/vrsky/pkg/io"
)

func main() {
	// Setup logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Create PostgreSQL producer
	producer, err := io.NewPostgresOutput(logger)
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

	// Start metrics HTTP server on port 9090
	metricsHandler := io.GetMetricsHandler()
	go func() {
		http.Handle("/metrics", metricsHandler)
		logger.Info("Starting metrics server on :9090")
		if err := http.ListenAndServe(":9090", nil); err != nil && err != http.ErrServerClosed {
			logger.Error("Metrics server error", "error", err)
		}
	}()

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal
	sig := <-sigChan
	logger.Info("Received shutdown signal", "signal", sig)

	// Cancel context to stop the producer
	cancel()

	// Give pending operations a moment to complete before closing
	time.Sleep(500 * time.Millisecond)

	// Close producer gracefully
	if err := producer.Close(); err != nil {
		logger.Error("Failed to close producer", "error", err)
		os.Exit(1)
	}

	logger.Info("PostgreSQL producer stopped",
		"total_written", producer.GetWritten())
}
