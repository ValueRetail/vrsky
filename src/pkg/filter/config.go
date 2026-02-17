package filter

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config represents the filter configuration
type Config struct {
	FilterID       string        `yaml:"filter_id"`
	InputTopic     string        `yaml:"input_topic"`
	OutputTopic    string        `yaml:"output_topic"`
	RejectionTopic string        `yaml:"rejection_topic"`
	Rules          []interface{} `yaml:"rules"`              // Raw gating/validation rules
	RoutingRules   []interface{} `yaml:"routing_rules"`      // Optional routing rules for Priority 2
	RateLimitRules []interface{} `yaml:"rate_limit_rules"`   // Optional rate limit rules for Priority 3
}

// Rule represents a single filter rule
type Rule struct {
	ID        string
	Name      string
	SchemaID  string
	Condition *Condition
}

// Condition represents a single condition to evaluate
type Condition struct {
	Operator string      // ==, !=, >, <, >=, <=, contains, startswith, endswith, regex_match, in_list, always
	Field    string      // Path to field (supports dot notation)
	Value    interface{} // Value to compare against
}

// LoadConfig loads configuration from a YAML file
func LoadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	// Validate config
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &config, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.FilterID == "" {
		return fmt.Errorf("filter_id is required")
	}
	if c.InputTopic == "" {
		return fmt.Errorf("input_topic is required")
	}
	if c.OutputTopic == "" {
		return fmt.Errorf("output_topic is required")
	}
	if c.RejectionTopic == "" {
		return fmt.Errorf("rejection_topic is required")
	}
	if len(c.Rules) == 0 {
		return fmt.Errorf("at least one rule is required")
	}
	// Routing rules are optional (Priority 2 feature)
	return nil
}
