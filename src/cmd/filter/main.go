package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/ValueRetail/vrsky/pkg/filter"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func main() {
	// Setup logging
	setupLogging()

	// Load configuration from environment
	config := loadFilterConfig()

	slog.Info("Filter configuration loaded",
		"filter_id", config.FilterID,
		"input_topic", config.InputTopic,
		"output_topic", config.OutputTopic)

	// Connect to NATS
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	slog.Info("Connecting to NATS", "url", natsURL)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		slog.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	slog.Info("Connected to NATS")

	// Create logger
	logger := slog.Default()

	// Create metrics registry
	registry := prometheus.NewRegistry()

	// Create the filter
	f, err := filter.NewFilter(config.FilterID, config, nc, logger, registry)
	if err != nil {
		slog.Error("Failed to create filter", "error", err)
		os.Exit(1)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the filter
	if err := f.Start(ctx); err != nil {
		slog.Error("Failed to start filter", "error", err)
		os.Exit(1)
	}

	// Create subscription to input topic
	msgChan := make(chan *nats.Msg, 100)
	sub, err := nc.ChanSubscribe(config.InputTopic, msgChan)
	if err != nil {
		slog.Error("Failed to subscribe to input topic", "error", err)
		os.Exit(1)
	}
	defer sub.Unsubscribe()

	slog.Info("Subscribed to input topic", "topic", config.InputTopic)

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
					slog.Error("Failed to process message",
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
						slog.Error("Failed to publish to output topic",
							"topic", outputTopic,
							"error", err)
					} else {
						slog.Debug("Message published to output topic",
							"topic", outputTopic,
							"envelope_id", env.ID)
					}
				} else {
					// Publish to rejection topic for non-accepted messages
					if err := nc.Publish(config.RejectionTopic, env.Payload); err != nil {
						slog.Error("Failed to publish to rejection topic",
							"topic", config.RejectionTopic,
							"error", err)
					} else {
						slog.Debug("Message published to rejection topic",
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
			slog.Error("Filter error", "error", err)
			os.Exit(1)
		}
	case sig := <-sigChan:
		slog.Info("Received signal, shutting down", "signal", sig.String())
		cancel()

		// Stop filter with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		if err := f.Stop(shutdownCtx); err != nil {
			slog.Error("Error stopping filter", "error", err)
		}

		slog.Info("Filter shutdown complete")
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
func setupLogging() {
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: logLevel,
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	slog.SetDefault(slog.New(handler))
}
