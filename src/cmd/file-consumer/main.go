package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v4/stdlib"
	"github.com/nats-io/nats.go"
)

func main() {
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))

	config := LoadConfig()
	if err := config.Validate(); err != nil {
		logger.Error("Invalid configuration", "error", err)
		os.Exit(1)
	}

	logger.Info("Starting File Consumer Service", "version", "1.0.0", "port", config.Port, "base_dir", config.BaseDir)

	db, err := initDatabase(config.DatabaseURL, logger)
	if err != nil {
		logger.Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	nc, err := initNATS(config.NATSUrl, logger)
	if err != nil {
		logger.Error("Failed to initialize NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	service := NewFileConsumerService(db, nc, logger, config)

	ctx := context.Background()
	if err := service.Start(ctx); err != nil {
		logger.Error("Failed to start File Consumer Service", "error", err)
		os.Exit(1)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("File Consumer Service running. Press Ctrl+C to stop.")
	<-sigChan

	logger.Info("Shutting down File Consumer Service...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := service.Stop(shutdownCtx); err != nil {
		logger.Error("Error during shutdown", "error", err)
	}

	logger.Info("File Consumer Service stopped")
}

func initDatabase(dbURL string, logger *slog.Logger) (*sql.DB, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	logger.Info("Database connected successfully")
	return db, nil
}

func initNATS(natsURL string, logger *slog.Logger) (*nats.Conn, error) {
	nc, err := nats.Connect(
		natsURL,
		nats.ReconnectWait(100*time.Millisecond),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			logger.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("NATS reconnected", "url", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	logger.Info("NATS connected", "url", nc.ConnectedUrl())
	return nc, nil
}
