package io

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ValueRetail/vrsky/pkg/component"
)

// InputOptions provides optional dependencies for input handlers
type InputOptions struct {
	// DB is an optional database connection for persistent state stores
	DB *sql.DB
	// Logger is an optional structured logger (defaults to slog.Default())
	Logger *slog.Logger
}

// NewInput creates an Input handler based on type
func NewInput(inputType string, configJSON json.RawMessage) (component.Input, error) {
	return NewInputWithOptions(inputType, configJSON, nil)
}

// NewInputWithOptions creates an Input handler with optional dependencies
func NewInputWithOptions(inputType string, configJSON json.RawMessage, opts *InputOptions) (component.Input, error) {
	logger := slog.Default()
	if opts != nil && opts.Logger != nil {
		logger = opts.Logger
	}

	switch inputType {
	case "http":
		return NewHTTPInput(configJSON)
	case "nats":
		return NewNATSInput(configJSON)
	case "file":
		return NewFileConsumer(logger)
	case "api":
		// Use PostgresStateStore if DB is provided, otherwise use in-memory
		var stateStore StateStore
		if opts != nil && opts.DB != nil {
			var err error
			stateStore, err = NewPostgresStateStore(opts.DB, logger)
			if err != nil {
				return nil, fmt.Errorf("create postgres state store: %w", err)
			}
			logger.Info("API consumer using PostgreSQL state store")
		} else {
			logger.Info("API consumer using in-memory state store (non-persistent)")
		}
		return NewAPIConsumer(configJSON, stateStore, logger)
	default:
		return nil, fmt.Errorf("unknown input type: %s", inputType)
	}
}

// NewOutput creates an Output handler based on type
func NewOutput(outputType string, configJSON json.RawMessage) (component.Output, error) {
	switch outputType {
	case "http":
		return NewHTTPOutput(configJSON)
	case "nats":
		return NewNATSOutput(configJSON)
	default:
		return nil, fmt.Errorf("unknown output type: %s", outputType)
	}
}
