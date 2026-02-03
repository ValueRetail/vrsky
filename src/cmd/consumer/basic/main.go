package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ValueRetail/vrsky/internal/config"
	"github.com/ValueRetail/vrsky/pkg/component"
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

	// Create input from configuration
	inputConfig, err := json.Marshal(cfg.InputConfig)
	if err != nil {
		slog.Error("Failed to marshal input config", "error", err)
		os.Exit(1)
	}

	input, err := io.NewInput(cfg.InputType, inputConfig)
	if err != nil {
		slog.Error("Failed to create input", "error", err)
		os.Exit(1)
	}

	// Create output from configuration
	outputConfig, err := json.Marshal(cfg.OutputConfig)
	if err != nil {
		slog.Error("Failed to marshal output config", "error", err)
		os.Exit(1)
	}

	output, err := io.NewOutput(cfg.OutputType, outputConfig)
	if err != nil {
		slog.Error("Failed to create output", "error", err)
		os.Exit(1)
	}

	// Create consumer
	cons := component.New(input, output)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the consumer input and output
	if err := input.Start(ctx); err != nil {
		slog.Error("Failed to start input", "error", err)
		os.Exit(1)
	}

	if err := output.Start(ctx); err != nil {
		slog.Error("Failed to start output", "error", err)
		os.Exit(1)
	}

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the main processing loop in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- cons.Process(ctx, input, output)
	}()

	// Wait for either an error or a signal
	select {
	case err := <-errChan:
		if err != nil {
			slog.Error("Consumer error", "error", err)
			os.Exit(1)
		}
	case sig := <-sigChan:
		slog.Info("Received signal, shutting down",
			"signal", sig.String())
		cancel()
		cons.Stop(ctx)
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
