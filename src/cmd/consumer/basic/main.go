package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/health"
	"github.com/ValueRetail/vrsky/pkg/io"
	"github.com/ValueRetail/vrsky/pkg/metrics"
	"github.com/ValueRetail/vrsky/pkg/runtime"
)

func main() {
	// Setup logging first
	logger := setupLogging()

	logger.Info("Starting VRSky consumer component")

	// Try to load new K8s runtime config first
	rtCfg, err := runtime.LoadFromEnv()
	if err != nil {
		logger.Error("Failed to load runtime config", "error", err)
		os.Exit(1)
	}

	// Check if we're running in K8s mode (new env vars) or legacy mode
	if rtCfg.NodeID != "" && rtCfg.TenantID != "" {
		// New K8s mode - use runtime config
		runK8sMode(logger, rtCfg)
	} else {
		// Legacy mode - use old config package
		runLegacyMode(logger)
	}
}

// runK8sMode runs the consumer with K8s orchestrator-injected configuration
func runK8sMode(logger *slog.Logger, rtCfg *runtime.Config) {
	logger.Info("Running in K8s mode",
		"tenant_id", rtCfg.TenantID,
		"connection_id", rtCfg.ConnectionID,
		"node_id", rtCfg.NodeID,
		"output_subject", rtCfg.OutputNATSSubject)

	// Validate configuration
	if err := rtCfg.Validate(); err != nil {
		logger.Error("Invalid configuration", "error", err)
		os.Exit(1)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize metrics
	baseMetrics, err := metrics.NewBase(metrics.Config{
		TenantID:     rtCfg.TenantID,
		ConnectionID: rtCfg.ConnectionID,
		NodeID:       rtCfg.NodeID,
		NodeType:     metrics.TypeConsumer,
	})
	if err != nil {
		logger.Error("Failed to initialize metrics", "error", err)
		os.Exit(1)
	}

	// Start health server
	healthServer := health.NewServer(health.Config{
		Port:        rtCfg.HealthPort,
		ComponentID: "consumer",
		NodeID:      rtCfg.NodeID,
		Logger:      logger,
	})

	// Mark as not ready until input/output are started
	healthServer.SetReady(false)

	if err := healthServer.Start(ctx); err != nil {
		logger.Error("Failed to start health server", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = healthServer.Stop(shutdownCtx)
	}()

	// Parse component-specific config
	var consumerCfg ConsumerConfig
	if err := rtCfg.ParseConfig(&consumerCfg); err != nil {
		logger.Error("Failed to parse consumer config", "error", err)
		os.Exit(1)
	}

	// Create input based on config
	input, err := createInput(logger, consumerCfg)
	if err != nil {
		logger.Error("Failed to create input", "error", err)
		os.Exit(1)
	}

	// Create NATS output to publish to OUTPUT_NATS_SUBJECT
	outputConfig, _ := json.Marshal(map[string]interface{}{
		"url":     rtCfg.NATSURLs,
		"subject": rtCfg.OutputNATSSubject,
	})
	output, err := io.NewNATSOutput(outputConfig)
	if err != nil {
		logger.Error("Failed to create NATS output", "error", err)
		os.Exit(1)
	}

	// Start input and output
	if err := input.Start(ctx); err != nil {
		logger.Error("Failed to start input", "error", err)
		os.Exit(1)
	}

	if err := output.Start(ctx); err != nil {
		logger.Error("Failed to start output", "error", err)
		os.Exit(1)
	}

	// Mark as ready now that everything is initialized
	healthServer.SetReady(true)
	logger.Info("Consumer ready for traffic")

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the main processing loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Record message received
				start := time.Now()
				baseMetrics.RecordReceived()

				env, err := input.Read(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return // Context cancelled, shutdown
					}
					logger.Error("Failed to read from input", "error", err)
					baseMetrics.RecordFailed("read_error")
					continue
				}

				// Write to NATS output
				if err := output.Write(ctx, env); err != nil {
					logger.Error("Failed to write to output",
						"error", err,
						"message_id", env.ID)
					baseMetrics.RecordFailed("write_error")
					continue
				}

				baseMetrics.ObserveProcessing(start, nil)
				logger.Debug("Message processed",
					"message_id", env.ID,
					"duration", time.Since(start))
			}
		}
	}()

	// Wait for shutdown signal
	sig := <-sigChan
	logger.Info("Received signal, shutting down", "signal", sig.String())

	// Mark as not ready (stop receiving new traffic)
	healthServer.SetReady(false)

	// Cancel context to stop processing loop
	cancel()

	// Graceful shutdown
	if err := input.Close(); err != nil {
		logger.Error("Error closing input", "error", err)
	}
	if err := output.Close(); err != nil {
		logger.Error("Error closing output", "error", err)
	}

	logger.Info("Consumer shutdown complete")
}

// runLegacyMode runs the consumer with the old config package (backward compatibility)
func runLegacyMode(logger *slog.Logger) {
	logger.Info("Running in legacy mode")

	// Import config package dynamically to avoid circular dependencies
	// This preserves backward compatibility with existing deployments
	inputType := os.Getenv("INPUT_TYPE")
	inputConfigStr := os.Getenv("INPUT_CONFIG")
	outputType := os.Getenv("OUTPUT_TYPE")
	outputConfigStr := os.Getenv("OUTPUT_CONFIG")

	if inputType == "" || outputType == "" {
		logger.Error("Legacy mode requires INPUT_TYPE and OUTPUT_TYPE")
		os.Exit(1)
	}

	logger.Info("Configuration loaded",
		"input_type", inputType,
		"output_type", outputType)

	// Create input from configuration
	input, err := io.NewInput(inputType, json.RawMessage(inputConfigStr))
	if err != nil {
		logger.Error("Failed to create input", "error", err)
		os.Exit(1)
	}

	// Create output from configuration
	output, err := io.NewOutput(outputType, json.RawMessage(outputConfigStr))
	if err != nil {
		logger.Error("Failed to create output", "error", err)
		os.Exit(1)
	}

	// Create consumer component
	cons := component.New(input, output)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the consumer input and output
	if err := input.Start(ctx); err != nil {
		logger.Error("Failed to start input", "error", err)
		os.Exit(1)
	}

	if err := output.Start(ctx); err != nil {
		logger.Error("Failed to start output", "error", err)
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
			logger.Error("Consumer error", "error", err)
			os.Exit(1)
		}
	case sig := <-sigChan:
		logger.Info("Received signal, shutting down", "signal", sig.String())
		cancel()
		_ = cons.Stop(ctx)
	}
}

// ConsumerConfig holds component-specific configuration for consumers
type ConsumerConfig struct {
	// InputType is the type of input (http, file, etc.)
	InputType string `json:"input_type"`
	// InputConfig is the input-specific configuration
	InputConfig json.RawMessage `json:"input_config"`
}

// createInput creates an input handler based on consumer config
func createInput(logger *slog.Logger, cfg ConsumerConfig) (component.Input, error) {
	if cfg.InputType == "" {
		cfg.InputType = "http" // Default to HTTP webhook
	}
	return io.NewInput(cfg.InputType, cfg.InputConfig)
}

// setupLogging configures structured logging
func setupLogging() *slog.Logger {
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: logLevel,
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	return logger
}
