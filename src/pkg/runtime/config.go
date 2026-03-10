// Package runtime provides shared runtime configuration and initialization
// for VRSky components running in Kubernetes.
package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// Config holds the standard environment variables injected by the orchestrator
type Config struct {
	// TenantID identifies the tenant owning this pipeline
	TenantID string
	// ConnectionID identifies the pipeline/connection
	ConnectionID string
	// NodeID uniquely identifies this node in the pipeline
	NodeID string
	// NodeType is the component type (consumer, filter, converter, producer)
	NodeType string
	// InputNATSSubject is the NATS subject to read messages from (empty for consumers)
	InputNATSSubject string
	// OutputNATSSubject is the NATS subject to write messages to (empty for producers)
	OutputNATSSubject string
	// Config is the JSON-encoded component configuration
	Config json.RawMessage
	// NATSURLs is a comma-separated list of NATS server URLs
	NATSURLs string
	// NATSAccount is the NATS account for authentication
	NATSAccount string
	// HealthPort is the port for health/metrics endpoints (default: 8080)
	HealthPort int
	// MetricsPort is the port for Prometheus metrics (default: 9090, or same as HealthPort)
	MetricsPort int
	// LogLevel is the logging level (debug, info, warn, error)
	LogLevel string
}

// LoadFromEnv loads configuration from standard environment variables
// as injected by the VRSky orchestrator.
func LoadFromEnv() (*Config, error) {
	cfg := &Config{
		TenantID:          os.Getenv("TENANT_ID"),
		ConnectionID:      os.Getenv("CONNECTION_ID"),
		NodeID:            os.Getenv("NODE_ID"),
		NodeType:          os.Getenv("NODE_TYPE"),
		InputNATSSubject:  os.Getenv("INPUT_NATS_SUBJECT"),
		OutputNATSSubject: os.Getenv("OUTPUT_NATS_SUBJECT"),
		NATSURLs:          os.Getenv("NATS_URLS"),
		NATSAccount:       os.Getenv("NATS_ACCOUNT"),
		LogLevel:          os.Getenv("LOG_LEVEL"),
	}

	// Parse CONFIG JSON if present
	configStr := os.Getenv("CONFIG")
	if configStr != "" {
		// Validate it's valid JSON
		var obj interface{}
		if err := json.Unmarshal([]byte(configStr), &obj); err != nil {
			return nil, fmt.Errorf("CONFIG is not valid JSON: %w", err)
		}
		cfg.Config = json.RawMessage(configStr)
	}

	// Parse health port with default
	healthPortStr := os.Getenv("HEALTH_PORT")
	if healthPortStr != "" {
		port, err := strconv.Atoi(healthPortStr)
		if err != nil {
			return nil, fmt.Errorf("HEALTH_PORT is not a valid integer: %w", err)
		}
		cfg.HealthPort = port
	} else {
		cfg.HealthPort = 8080
	}

	// Parse metrics port with default (same as health port by default)
	metricsPortStr := os.Getenv("METRICS_PORT")
	if metricsPortStr != "" {
		port, err := strconv.Atoi(metricsPortStr)
		if err != nil {
			return nil, fmt.Errorf("METRICS_PORT is not a valid integer: %w", err)
		}
		cfg.MetricsPort = port
	} else {
		cfg.MetricsPort = cfg.HealthPort
	}

	// Set defaults
	if cfg.NATSURLs == "" {
		cfg.NATSURLs = "nats://nats:4222"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	return cfg, nil
}

// Validate checks that required fields are present for the given node type
func (c *Config) Validate() error {
	if c.TenantID == "" {
		return fmt.Errorf("TENANT_ID is required")
	}
	if c.ConnectionID == "" {
		return fmt.Errorf("CONNECTION_ID is required")
	}
	if c.NodeID == "" {
		return fmt.Errorf("NODE_ID is required")
	}
	if c.NodeType == "" {
		return fmt.Errorf("NODE_TYPE is required")
	}

	// Validate based on node type
	switch c.NodeType {
	case "consumer":
		if c.OutputNATSSubject == "" {
			return fmt.Errorf("OUTPUT_NATS_SUBJECT is required for consumer")
		}
	case "producer":
		if c.InputNATSSubject == "" {
			return fmt.Errorf("INPUT_NATS_SUBJECT is required for producer")
		}
	case "filter", "converter":
		if c.InputNATSSubject == "" {
			return fmt.Errorf("INPUT_NATS_SUBJECT is required for %s", c.NodeType)
		}
		if c.OutputNATSSubject == "" {
			return fmt.Errorf("OUTPUT_NATS_SUBJECT is required for %s", c.NodeType)
		}
	default:
		return fmt.Errorf("invalid NODE_TYPE: %s", c.NodeType)
	}

	return nil
}

// ParseConfig unmarshals the Config JSON into the provided struct
func (c *Config) ParseConfig(v interface{}) error {
	if len(c.Config) == 0 {
		return nil // No config provided, leave defaults
	}
	return json.Unmarshal(c.Config, v)
}

// IsConsumer returns true if this is a consumer node
func (c *Config) IsConsumer() bool {
	return c.NodeType == "consumer"
}

// IsProducer returns true if this is a producer node
func (c *Config) IsProducer() bool {
	return c.NodeType == "producer"
}

// IsFilter returns true if this is a filter node
func (c *Config) IsFilter() bool {
	return c.NodeType == "filter"
}

// IsConverter returns true if this is a converter node
func (c *Config) IsConverter() bool {
	return c.NodeType == "converter"
}
