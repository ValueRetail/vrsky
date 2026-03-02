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
	"github.com/nats-io/nats.go"
)

// slogLoggerAdapter adapts slog.Logger to converter.Logger interface
type slogLoggerAdapter struct {
	logger *slog.Logger
}

func (a *slogLoggerAdapter) InfoContext(ctx context.Context, msg string, args ...interface{}) {
	a.logger.InfoContext(ctx, msg, args...)
}

func (a *slogLoggerAdapter) WarnContext(ctx context.Context, msg string, args ...interface{}) {
	a.logger.WarnContext(ctx, msg, args...)
}

func (a *slogLoggerAdapter) ErrorContext(ctx context.Context, msg string, args ...interface{}) {
	a.logger.ErrorContext(ctx, msg, args...)
}

func (a *slogLoggerAdapter) Warn(msg string) {
	a.logger.Warn(msg)
}

func (a *slogLoggerAdapter) Error(msg string) {
	a.logger.Error(msg)
}

func main() {
	// Setup logging
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	slogLogger := converter.SetupLogger(logLevel)
	logger := &slogLoggerAdapter{logger: slogLogger}

	logger.InfoContext(context.Background(), "Converter service starting")

	// Load environment variables
	converterID := os.Getenv("CONVERTER_ID")
	if converterID == "" {
		logger.ErrorContext(context.Background(), "CONVERTER_ID not set")
		os.Exit(1)
	}

	tenantID := os.Getenv("TENANT_ID")
	if tenantID == "" {
		logger.ErrorContext(context.Background(), "TENANT_ID not set")
		os.Exit(1)
	}

	natsURLs := os.Getenv("NATS_URLS")
	if natsURLs == "" {
		natsURLs = "nats://nats:4222"
	}

	logger.InfoContext(context.Background(), "Environment loaded",
		"converter_id", converterID,
		"tenant_id", tenantID,
		"nats_urls", natsURLs,
		"log_level", logLevel,
	)

	// Connect to NATS
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	natsAddrs := strings.Split(natsURLs, ",")
	logger.InfoContext(ctx, "Connecting to NATS", "addresses", natsAddrs)

	natsConn, err := nats.Connect(strings.Join(natsAddrs, ","),
		nats.Name("converter-"+converterID),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1), // Infinite reconnects
	)
	if err != nil {
		logger.ErrorContext(ctx, "Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer natsConn.Close()

	logger.InfoContext(ctx, "Connected to NATS", "server_id", natsConn.ConnectedServerId())

	// Start health server for Kubernetes probes (port 8080)
	healthServer := converter.NewHealthServer(8080, logger)
	if healthServer != nil {
		if err := healthServer.Start(ctx); err != nil {
			logger.ErrorContext(ctx, "Failed to start health server", "error", err)
			os.Exit(1)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := healthServer.Stop(shutdownCtx); err != nil {
				logger.ErrorContext(shutdownCtx, "Error stopping health server", "error", err)
			}
		}()
	}

	// Create converter instance
	conv, err := converter.NewConverter(ctx, converterID, tenantID, natsConn, slogLogger)
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

	logger.InfoContext(ctx, "Converter started successfully")

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigChan
	logger.InfoContext(ctx, "Received signal, shutting down", "signal", sig.String())

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
