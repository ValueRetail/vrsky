package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the Management API configuration
type Config struct {
	// Database
	DatabaseURL string

	// NATS
	NATSUrl       string
	NATSSubjPref  string // Subject prefix (e.g., "vrsky")
	NATSReconnect time.Duration

	// HTTP Server
	ListenAddr   string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	// CORS
	CORSOrigins []string

	// Multi-tenancy
	TenantHeader string

	// Logging
	LogLevel string

	// Service
	ServiceName string
	Version     string
}

// LoadConfig loads configuration from environment variables
func LoadConfig() *Config {
	return &Config{
		// Database
		DatabaseURL: getEnv("MGMT_API_DB_URL", "postgres://vrsky_user:vrsky_pass@postgres:5432/vrsky"),

		// NATS
		NATSUrl:       getEnv("MGMT_API_NATS_URL", "nats://nats:4222"),
		NATSSubjPref:  getEnv("MGMT_API_NATS_PREFIX", "vrsky"),
		NATSReconnect: getDurationEnv("MGMT_API_NATS_RECONNECT_MS", 100*time.Millisecond),

		// HTTP Server
		ListenAddr:   getEnv("MGMT_API_LISTEN", ":3000"),
		ReadTimeout:  getDurationEnv("MGMT_API_READ_TIMEOUT_SEC", 15*time.Second),
		WriteTimeout: getDurationEnv("MGMT_API_WRITE_TIMEOUT_SEC", 30*time.Second),

		// CORS
		CORSOrigins: parseEnvList("MGMT_API_CORS_ORIGINS", "http://localhost:5173"),

		// Multi-tenancy
		TenantHeader: getEnv("MGMT_API_TENANT_HEADER", "X-Tenant-ID"),

		// Logging
		LogLevel: getEnv("LOG_LEVEL", "info"),

		// Service
		ServiceName: "vrsky-management-api",
		Version:     getEnv("API_VERSION", "1.0.0"),
	}
}

// getEnv retrieves an environment variable or returns a default value
func getEnv(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

// getDurationEnv retrieves a duration environment variable
// If the key ends with "_SEC", it parses the value as seconds
// Otherwise, it parses as milliseconds for backward compatibility
func getDurationEnv(key string, defaultValue time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}

	num, err := strconv.Atoi(val)
	if err != nil {
		return defaultValue
	}

	// Check if the key ends with "_SEC" to determine unit
	if strings.HasSuffix(key, "_SEC") {
		return time.Duration(num) * time.Second
	}

	// Default to milliseconds for backward compatibility
	return time.Duration(num) * time.Millisecond
}

// parseEnvList parses a comma-separated list from environment variable
func parseEnvList(key, defaultValue string) []string {
	val := os.Getenv(key)
	if val == "" {
		val = defaultValue
	}

	// Parse comma-separated values
	var result []string
	for _, item := range strings.Split(val, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}

	if len(result) == 0 {
		// Return default as single-element slice
		return []string{defaultValue}
	}

	return result
}

// Validate checks if configuration is valid
func (c *Config) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("MGMT_API_DB_URL must be set")
	}
	if c.NATSUrl == "" {
		return fmt.Errorf("MGMT_API_NATS_URL must be set")
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("MGMT_API_LISTEN must be set")
	}
	return nil
}
