package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/internal/config"
	"github.com/ValueRetail/vrsky/pkg/io"
)

func main() {
	// Setup logging
	setupLogging()

	// Load configuration from environment
	cfg, err := config.Load()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	slog.Info("Configuration loaded",
		"input_type", cfg.InputType,
		"output_type", cfg.OutputType)

	// Create I/O factory
	factory := io.NewFactory()

	// Create input from configuration
	input, err := factory.CreateInput(cfg.InputType, cfg.InputConfig)
	if err != nil {
		slog.Error("Failed to create input", "error", err)
		os.Exit(1)
	}

	// Create output from configuration
	output, err := factory.CreateOutput(cfg.OutputType, cfg.OutputConfig)
	if err != nil {
		slog.Error("Failed to create output", "error", err)
		os.Exit(1)
	}

	// Create producer
	prod := component.New(input, output)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the producer
	if err := prod.Start(ctx); err != nil {
		slog.Error("Failed to start producer", "error", err)
		os.Exit(1)
	}

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the main processing loop in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- prod.Process(ctx, input, output)
	}()

	// Wait for either an error or a signal
	select {
	case err := <-errChan:
		if err != nil {
			slog.Error("Producer error", "error", err)
			os.Exit(1)
		}
	case sig := <-sigChan:
		slog.Info("Received signal, shutting down",
			"signal", sig.String())
		cancel()
		prod.Stop(ctx)
	}
}

func setupLogging() {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}

	// Check for debug mode
	if flag.Lookup("debug") != nil && flag.Lookup("debug").Value.String() == "true" {
		opts.Level = slog.LevelDebug
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	slog.SetDefault(slog.New(handler))
}
