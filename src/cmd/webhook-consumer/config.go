package main

import (
	"fmt"
	"os"
)

// Config holds the Webhook Consumer service configuration
type Config struct {
	NATSUrl     string
	DatabaseURL string
	WebhookPort string
	LogLevel    string
}

// LoadConfig loads configuration from environment variables
func LoadConfig() *Config {
	return &Config{
		NATSUrl:     getEnv("NATS_URL", "nats://localhost:4222"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/vrsky?sslmode=disable"),
		WebhookPort: getEnv("WEBHOOK_PORT", "9100"),
		LogLevel:    getEnv("LOG_LEVEL", "info"),
	}
}

// Validate checks that required configuration values are present
func (c *Config) Validate() error {
	if c.NATSUrl == "" {
		return fmt.Errorf("NATS_URL is required")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	return nil
}

// getEnv returns the environment variable value or a default
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
