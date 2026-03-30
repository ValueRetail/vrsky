package main

import (
	"fmt"
	"os"
)

type Config struct {
	NATSUrl     string
	DatabaseURL string
	Port        string
	BaseDir     string
	LogLevel    string
	HostHome    string
}

func LoadConfig() *Config {
	return &Config{
		NATSUrl:     getEnv("NATS_URL", "nats://localhost:4222"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/vrsky?sslmode=disable"),
		Port:        getEnv("FILE_CONSUMER_PORT", "9200"),
		BaseDir:     getEnv("FILE_CONSUMER_BASE_DIR", "/data/input"),
		LogLevel:    getEnv("LOG_LEVEL", "info"),
		HostHome:    getEnv("HOST_HOME", ""),
	}
}

func (c *Config) Validate() error {
	if c.NATSUrl == "" {
		return fmt.Errorf("NATS_URL is required")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
