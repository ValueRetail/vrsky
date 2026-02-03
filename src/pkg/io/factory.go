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
