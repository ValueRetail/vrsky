package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"
)

func main() {
	config := LoadConfig()
	if err := config.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}

	logLevel := slog.LevelInfo
	switch config.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	logger.Info("Starting DB Consumer Service", "port", config.Port)

	// Connect to management database
	db, err := sql.Open("postgres", config.DatabaseURL)
	if err != nil {
		logger.Error("Failed to connect to management database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		logger.Error("Failed to ping management database", "error", err)
		os.Exit(1)
	}
	logger.Info("Management database connected")

	// Connect to NATS
	nc, err := nats.Connect(config.NATSUrl)
	if err != nil {
		logger.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()
	logger.Info("NATS connected", "url", config.NATSUrl)

	// Create and start service
	service := NewDBConsumerService(db, nc, logger, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.Start(ctx); err != nil {
		logger.Error("Failed to start service", "error", err)
		os.Exit(1)
	}

	// Start HTTP server for SSE events and test-connection endpoint
	server := NewServer(config.Port, service, logger)
	if err := server.Start(); err != nil {
		logger.Error("Failed to start HTTP server", "error", err)
		os.Exit(1)
	}
	defer server.Stop()

	logger.Info("DB Consumer Service running. Press Ctrl+C to stop.")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("Shutting down...")
	cancel()
	if err := service.Stop(ctx); err != nil {
		logger.Error("Error during shutdown", "error", err)
	}
}
