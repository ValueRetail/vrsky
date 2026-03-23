package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/filter"
	"github.com/ValueRetail/vrsky/pkg/health"
	"github.com/ValueRetail/vrsky/pkg/runtime"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	// Setup logging
	logger := setupLogging()

	logger.Info("Starting VRSky filter component")

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
		// Legacy mode - use old config approach
		runLegacyMode(logger)
	}
}

// runK8sMode runs the filter with K8s orchestrator-injected configuration
func runK8sMode(logger *slog.Logger, rtCfg *runtime.Config) {
	logger.Info("Running in K8s mode",
		"tenant_id", rtCfg.TenantID,
		"connection_id", rtCfg.ConnectionID,
		"node_id", rtCfg.NodeID,
		"input_subject", rtCfg.InputNATSSubject,
		"output_subject", rtCfg.OutputNATSSubject)

	// Validate configuration
	if err := rtCfg.Validate(); err != nil {
		logger.Error("Invalid configuration", "error", err)
		os.Exit(1)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start health server
	healthServer := health.NewServer(health.Config{
		Port:        rtCfg.HealthPort,
		ComponentID: "filter",
		NodeID:      rtCfg.NodeID,
		Logger:      logger,
	})

	// Mark as not ready until filter is started
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
		nats.Name("vrsky-filter-"+rtCfg.NodeID),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		logger.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	logger.Info("Connected to NATS")

	// Parse filter-specific configuration
	var filterCfg filter.Config
	if err := rtCfg.ParseConfig(&filterCfg); err != nil {
		logger.Error("Failed to parse filter config", "error", err)
		os.Exit(1)
	}

	// Override topics with runtime config
	filterCfg.FilterID = rtCfg.NodeID
	filterCfg.InputTopic = rtCfg.InputNATSSubject
	filterCfg.OutputTopic = rtCfg.OutputNATSSubject
	if filterCfg.RejectionTopic == "" {
		filterCfg.RejectionTopic = rtCfg.OutputNATSSubject + ".rejected"
	}

	// Create metrics registry
	registry := prometheus.NewRegistry()

	// Create the filter
	f, err := filter.NewFilter(rtCfg.NodeID, &filterCfg, nc, logger, registry)
	if err != nil {
		logger.Error("Failed to create filter", "error", err)
		os.Exit(1)
	}

	// Start the filter
	if err := f.Start(ctx); err != nil {
		logger.Error("Failed to start filter", "error", err)
		os.Exit(1)
	}

	// Mark as ready
	healthServer.SetReady(true)
	logger.Info("Filter ready for traffic")

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigChan
	logger.Info("Received signal, shutting down", "signal", sig.String())

	// Mark as not ready
	healthServer.SetReady(false)

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := f.Stop(shutdownCtx); err != nil {
		logger.Error("Error stopping filter", "error", err)
	}

	logger.Info("Filter shutdown complete")
}

// runLegacyMode runs the filter with the old configuration style
func runLegacyMode(logger *slog.Logger) {
	// Load configuration from environment
	config := loadFilterConfig()

	logger.Info("Filter configuration loaded (legacy mode)",
		"filter_id", config.FilterID,
		"input_topic", config.InputTopic,
		"output_topic", config.OutputTopic)

	// Connect to NATS
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	logger.Info("Connecting to NATS", "url", natsURL)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		logger.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	logger.Info("Connected to NATS")

	// Create metrics registry
	registry := prometheus.NewRegistry()

	// Create the filter
	f, err := filter.NewFilter(config.FilterID, config, nc, logger, registry)
	if err != nil {
		logger.Error("Failed to create filter", "error", err)
		os.Exit(1)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start health server (even in legacy mode)
	healthPort := 8080
	healthServer := health.NewServer(health.Config{
		Port:        healthPort,
		ComponentID: "filter",
		NodeID:      config.FilterID,
		Logger:      logger,
	})

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

	// Start the filter
	if err := f.Start(ctx); err != nil {
		logger.Error("Failed to start filter", "error", err)
		os.Exit(1)
	}

	// Create subscription to input topic
	msgChan := make(chan *nats.Msg, 100)
	sub, err := nc.ChanSubscribe(config.InputTopic, msgChan)
	if err != nil {
		logger.Error("Failed to subscribe to input topic", "error", err)
		os.Exit(1)
	}
	defer func() { _ = sub.Unsubscribe() }()

	logger.Info("Subscribed to input topic", "topic", config.InputTopic)

	// Mark as ready
	healthServer.SetReady(true)

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the main processing loop in a goroutine
	errChan := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-msgChan:
				if msg == nil {
					continue
				}

				// Create envelope from NATS message
				env := &envelope.Envelope{
					ID:      uuid.New().String(),
					Payload: msg.Data,
					Source:  config.InputTopic,
				}

				// Process the message through filter
				decision, err := f.ProcessMessage(ctx, env)
				if err != nil {
					logger.Error("Failed to process message",
						"error", err,
						"envelope_id", env.ID)
					continue
				}

				// Publish to appropriate output topic based on decision
				outputTopic := config.OutputTopic

				// Check decision and publish accordingly
				if decision != nil && decision.Action == filter.ActionAccept {
					// Publish to output topic
					if err := nc.Publish(outputTopic, env.Payload); err != nil {
						logger.Error("Failed to publish to output topic",
							"topic", outputTopic,
							"error", err)
					} else {
						logger.Debug("Message published to output topic",
							"topic", outputTopic,
							"envelope_id", env.ID)
					}
				} else {
					// Publish to rejection topic for non-accepted messages
					if err := nc.Publish(config.RejectionTopic, env.Payload); err != nil {
						logger.Error("Failed to publish to rejection topic",
							"topic", config.RejectionTopic,
							"error", err)
					} else {
						logger.Debug("Message published to rejection topic",
							"topic", config.RejectionTopic,
							"envelope_id", env.ID)
					}
				}
			}
		}
	}()

	// Wait for either an error or a signal
	select {
	case err := <-errChan:
		if err != nil {
			logger.Error("Filter error", "error", err)
			os.Exit(1)
		}
	case sig := <-sigChan:
		logger.Info("Received signal, shutting down", "signal", sig.String())
		cancel()

		// Stop filter with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		if err := f.Stop(shutdownCtx); err != nil {
			logger.Error("Error stopping filter", "error", err)
		}

		logger.Info("Filter shutdown complete")
	}
}

// loadFilterConfig loads the filter configuration from environment variables and files
func loadFilterConfig() *filter.Config {
	// Try to load from config file first
	configFile := os.Getenv("FILTER_CONFIG_FILE")
	if configFile != "" {
		cfg, err := filter.LoadConfig(configFile)
		if err != nil {
			slog.Error("Failed to load config file, using defaults", "error", err)
		} else {
			return cfg
		}
	}

	// Load from environment variables with defaults
	config := &filter.Config{
		FilterID:       getEnv("FILTER_ID", "default-filter"),
		InputTopic:     getEnv("INPUT_TOPIC", "filter.input"),
		OutputTopic:    getEnv("OUTPUT_TOPIC", "filter.output"),
		RejectionTopic: getEnv("REJECTION_TOPIC", "filter.rejection"),
		Rules:          []interface{}{},
	}

	return config
}

// getEnv gets an environment variable with a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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
