package io

import (
	"encoding/json"
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/component"
)

// Factory creates Input and Output instances based on type and configuration
type Factory struct{}

// NewFactory creates a new I/O factory
func NewFactory() *Factory {
	return &Factory{}
}

// CreateInput creates an Input instance from type and JSON configuration
func (f *Factory) CreateInput(inputType string, config json.RawMessage) (component.Input, error) {
	switch inputType {
	case "nats":
		return NewNATSInput(config)
	case "http":
		return NewHTTPInput(config)
	case "file":
		return NewFileInput(config)
	default:
		return nil, fmt.Errorf("unknown input type: %s", inputType)
	}
}

// CreateOutput creates an Output instance from type and JSON configuration
func (f *Factory) CreateOutput(outputType string, config json.RawMessage) (component.Output, error) {
	switch outputType {
	case "nats":
		return NewNATSOutput(config)
	case "http":
		return NewHTTPOutput(config)
	case "file":
		return NewFileOutput(config)
	default:
		return nil, fmt.Errorf("unknown output type: %s", outputType)
	}
}
