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

	// Create producer
	producer, err := io.NewFileProducer(logger)
	if err != nil {
		logger.Error("Failed to create producer", "err", err)
		os.Exit(1)
	}

	// Start producer
	err = producer.Start(context.Background())
	if err != nil {
		logger.Error("Failed to start producer", "err", err)
		os.Exit(1)
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logger.Info("File Producer running. Press Ctrl+C to stop.")
	<-sigChan

	// Close producer
	producer.Close()
	logger.Info("File Producer closed")
}
