package io

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ValueRetail/vrsky/pkg/component"
)

// NewInput creates an Input handler based on type
func NewInput(inputType string, configJSON json.RawMessage) (component.Input, error) {
	switch inputType {
	case "http":
		return NewHTTPInput(configJSON)
	case "nats":
		return NewNATSInput(configJSON)
	case "file":
		logger := slog.Default()
		return NewFileConsumer(logger)
	case "api":
		logger := slog.Default()
		// Note: Pass nil for stateStore to use in-memory state (non-persistent)
		// For production, inject a PostgresStateStore implementation
		return NewAPIConsumer(configJSON, nil, logger)
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
