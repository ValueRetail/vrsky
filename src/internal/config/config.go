package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config holds the application configuration loaded from environment variables
type Config struct {
	InputType    string          `json:"input_type"`
	InputConfig  json.RawMessage `json:"input_config"`
	OutputType   string          `json:"output_type"`
	OutputConfig json.RawMessage `json:"output_config"`
}

// Load reads configuration from environment variables
func Load() (*Config, error) {
	config := &Config{}

	// Read input configuration
	inputType := os.Getenv("INPUT_TYPE")
	if inputType == "" {
		return nil, fmt.Errorf("INPUT_TYPE environment variable is required")
	}
	config.InputType = inputType

	inputConfigStr := os.Getenv("INPUT_CONFIG")
	if inputConfigStr == "" {
		return nil, fmt.Errorf("INPUT_CONFIG environment variable is required")
	}

	// Validate JSON
	var inputConfigObj interface{}
	if err := json.Unmarshal([]byte(inputConfigStr), &inputConfigObj); err != nil {
		return nil, fmt.Errorf("INPUT_CONFIG is not valid JSON: %w", err)
	}
	config.InputConfig = json.RawMessage(inputConfigStr)

	// Read output configuration
	outputType := os.Getenv("OUTPUT_TYPE")
	if outputType == "" {
		return nil, fmt.Errorf("OUTPUT_TYPE environment variable is required")
	}
	config.OutputType = outputType

	outputConfigStr := os.Getenv("OUTPUT_CONFIG")
	if outputConfigStr == "" {
		return nil, fmt.Errorf("OUTPUT_CONFIG environment variable is required")
	}

	// Validate JSON
	var outputConfigObj interface{}
	if err := json.Unmarshal([]byte(outputConfigStr), &outputConfigObj); err != nil {
		return nil, fmt.Errorf("OUTPUT_CONFIG is not valid JSON: %w", err)
	}
	config.OutputConfig = json.RawMessage(outputConfigStr)

	return config, nil
}
