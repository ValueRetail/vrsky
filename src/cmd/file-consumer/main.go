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
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Create consumer
	consumer, err := io.NewFileConsumer(logger)
	if err != nil {
		logger.Error("Failed to create consumer", "err", err)
		os.Exit(1)
	}

	// Start consumer
	err = consumer.Start(context.Background())
	if err != nil {
		logger.Error("Failed to start consumer", "err", err)
		os.Exit(1)
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logger.Info("File Consumer running. Press Ctrl+C to stop.")
	<-sigChan

	// Close consumer
	consumer.Close()
	logger.Info("File Consumer closed")
}
