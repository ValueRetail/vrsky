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
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	logger.Info("Starting Tenant Consumer Service", "version", "1.0.0")

	natsURL := getEnv("NATS_URL", "nats://localhost:4222")
	dbURL := getEnv("DATABASE_URL", "")
	if dbURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	db, err := initDatabase(dbURL, logger)
	if err != nil {
		logger.Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	nc, err := initNATS(natsURL, logger)
	if err != nil {
		logger.Error("Failed to initialize NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	service := NewTenantConsumerService(db, nc, logger)

	ctx := context.Background()
	if err := service.Start(ctx); err != nil {
		logger.Error("Failed to start service", "error", err)
		os.Exit(1)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("Tenant Consumer Service running. Press Ctrl+C to stop.")
	<-sigChan

	logger.Info("Shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	service.Stop(shutdownCtx)
	logger.Info("Tenant Consumer Service stopped")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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
