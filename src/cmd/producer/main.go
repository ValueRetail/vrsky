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
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/health"
	"github.com/ValueRetail/vrsky/pkg/io"
	"github.com/ValueRetail/vrsky/pkg/metrics"
	"github.com/ValueRetail/vrsky/pkg/runtime"
	"github.com/nats-io/nats.go"
)

func main() {
	// Setup logging first
	logger := setupLogging()

	logger.Info("Starting VRSky producer component")

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

// runK8sMode runs the producer with K8s orchestrator-injected configuration
func runK8sMode(logger *slog.Logger, rtCfg *runtime.Config) {
	logger.Info("Running in K8s mode",
		"tenant_id", rtCfg.TenantID,
		"connection_id", rtCfg.ConnectionID,
		"node_id", rtCfg.NodeID,
		"input_subject", rtCfg.InputNATSSubject)

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
		NodeType:     metrics.TypeProducer,
	})
	if err != nil {
		logger.Error("Failed to initialize metrics", "error", err)
		os.Exit(1)
	}

	// Start health server
	healthServer := health.NewServer(health.Config{
		Port:        rtCfg.HealthPort,
		ComponentID: "producer",
		NodeID:      rtCfg.NodeID,
		Logger:      logger,
	})

	// Mark as not ready until NATS is connected
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

	// Connect to NATS
	logger.Info("Connecting to NATS", "url", rtCfg.NATSURLs)
	nc, err := nats.Connect(rtCfg.NATSURLs,
		nats.Name("vrsky-producer-"+rtCfg.NodeID),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		logger.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	logger.Info("Connected to NATS")

	// Parse producer-specific config
	var producerCfg ProducerConfig
	if err := rtCfg.ParseConfig(&producerCfg); err != nil {
		logger.Error("Failed to parse producer config", "error", err)
		os.Exit(1)
	}

	// Create output based on config (file, HTTP, database, etc.)
	output, err := createOutput(logger, producerCfg)
	if err != nil {
		logger.Error("Failed to create output", "error", err)
		os.Exit(1)
	}

	// Start the output
	if err := output.Start(ctx); err != nil {
		logger.Error("Failed to start output", "error", err)
		os.Exit(1)
	}

	// Subscribe to input NATS subject
	msgChan := make(chan *nats.Msg, 100)
	sub, err := nc.ChanSubscribe(rtCfg.InputNATSSubject, msgChan)
	if err != nil {
		logger.Error("Failed to subscribe to input subject", "error", err)
		os.Exit(1)
	}
	defer func() { _ = sub.Unsubscribe() }()

	logger.Info("Subscribed to input subject", "subject", rtCfg.InputNATSSubject)

	// Mark as ready now that everything is initialized
	healthServer.SetReady(true)
	logger.Info("Producer ready for traffic")

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the main processing loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-msgChan:
				if msg == nil {
					continue
				}

				start := time.Now()
				baseMetrics.RecordReceived()

				// Parse envelope from NATS message
				env, err := envelope.Unmarshal(msg.Data)
				if err != nil {
					logger.Error("Failed to unmarshal envelope", "error", err)
					baseMetrics.RecordFailed("unmarshal_error")
					continue
				}

				// Write to output destination
				if err := output.Write(ctx, env); err != nil {
					logger.Error("Failed to write to output",
						"error", err,
						"envelope_id", env.ID)
					baseMetrics.RecordFailed("write_error")
					continue
				}

				baseMetrics.ObserveProcessing(start, nil)
				logger.Debug("Message produced",
					"envelope_id", env.ID,
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
	if err := output.Close(); err != nil {
		logger.Error("Error closing output", "error", err)
	}

	logger.Info("Producer shutdown complete")
}

// runLegacyMode runs the producer with the old config package (backward compatibility)
func runLegacyMode(logger *slog.Logger) {
	logger.Info("Running in legacy mode")

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

	// Create producer component
	prod := component.New(input, output)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the producer input and output
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
		errChan <- prod.Process(ctx, input, output)
	}()

	// Wait for either an error or a signal
	select {
	case err := <-errChan:
		if err != nil {
			logger.Error("Producer error", "error", err)
			os.Exit(1)
		}
	case sig := <-sigChan:
		logger.Info("Received signal, shutting down", "signal", sig.String())
		cancel()
		_ = prod.Stop(ctx)
	}
}

// ProducerConfig holds component-specific configuration for producers
type ProducerConfig struct {
	// OutputType is the type of output (http, file, database, etc.)
	OutputType string `json:"output_type"`
	// OutputConfig is the output-specific configuration
	OutputConfig json.RawMessage `json:"output_config"`
}

// createOutput creates an output handler based on producer config
func createOutput(logger *slog.Logger, cfg ProducerConfig) (component.Output, error) {
	if cfg.OutputType == "" {
		cfg.OutputType = "file" // Default to file output
	}
	return io.NewOutput(cfg.OutputType, cfg.OutputConfig)
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
