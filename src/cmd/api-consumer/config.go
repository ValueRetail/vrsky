package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds the API Consumer service configuration
type Config struct {
	// Required
	NATSUrl     string
	DatabaseURL string

	// Optional with defaults
	Port                string
	EncryptionKey       string        // Hex-encoded 32-byte key for AES-256
	LogLevel            string        // debug, info, warn, error
	PollTimeout         time.Duration // HTTP request timeout
	MaxConcurrentPolls  int           // Max concurrent polling goroutines
	DefaultPollInterval time.Duration // Default poll interval if not specified
}

// LoadConfig loads configuration from environment variables
func LoadConfig() *Config {
	pollTimeout := 30 * time.Second
	if val := os.Getenv("API_CONSUMER_POLL_TIMEOUT"); val != "" {
		if parsed, err := time.ParseDuration(val); err == nil {
			pollTimeout = parsed
		}
	}

	maxConcurrent := 100
	if val := os.Getenv("API_CONSUMER_MAX_WORKERS"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			maxConcurrent = parsed
		}
	}

	defaultPollInterval := 60 * time.Second
	if val := os.Getenv("API_CONSUMER_DEFAULT_POLL_INTERVAL"); val != "" {
		if parsed, err := time.ParseDuration(val); err == nil {
			defaultPollInterval = parsed
		}
	}

	return &Config{
		NATSUrl:             getEnv("NATS_URL", "nats://localhost:4222"),
		DatabaseURL:         getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/vrsky?sslmode=disable"),
		Port:                getEnv("API_CONSUMER_PORT", "9800"),
		EncryptionKey:       os.Getenv("ENCRYPTION_KEY"), // Optional, empty means no encryption
		LogLevel:            getEnv("LOG_LEVEL", "info"),
		PollTimeout:         pollTimeout,
		MaxConcurrentPolls:  maxConcurrent,
		DefaultPollInterval: defaultPollInterval,
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
	if c.EncryptionKey != "" && len(c.EncryptionKey) != 64 {
		return fmt.Errorf("ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
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
