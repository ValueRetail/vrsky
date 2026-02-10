package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ValueRetail/vrsky/pkg/io"
)

func main() {
	// Setup logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Create PostgreSQL CDC consumer
	consumer, err := io.NewPostgresInput(logger)
	if err != nil {
		logger.Error("Failed to create PostgreSQL consumer", "error", err)
		os.Exit(1)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start consumer
	if err := consumer.Start(ctx); err != nil {
		logger.Error("Failed to start consumer", "error", err)
		os.Exit(1)
	}

	logger.Info("PostgreSQL consumer started successfully")

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal
	sig := <-sigChan
	logger.Info("Received shutdown signal", "signal", sig)

	// Cancel context to stop the consumer
	cancel()

	// Close consumer gracefully
	if err := consumer.Close(); err != nil {
		logger.Error("Failed to close consumer", "error", err)
		os.Exit(1)
	}

	logger.Info("PostgreSQL consumer stopped")
}
