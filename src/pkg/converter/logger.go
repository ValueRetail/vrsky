package converter

import (
	"log/slog"
	"os"
	"strings"
)

// SetupLogger creates and configures a structured logger with slog.
// The log level is read from the LOG_LEVEL environment variable.
// Supported levels: debug, info, warn, error (default: info)
func SetupLogger(logLevel string) *slog.Logger {
	// Default to info if not specified
	if logLevel == "" {
		logLevel = "info"
	}

	// Parse log level
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		// Default to info for invalid values
		level = slog.LevelInfo
	}

	// Create JSON handler for structured logging
	opts := &slog.HandlerOptions{
		Level: level,
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)

	// Create logger with handler
	logger := slog.New(handler)

	return logger
}
