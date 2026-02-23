package converter

import (
	"time"
)

// ConverterConfig holds the configuration for a converter instance.
// It defines input/output topics, transformation rules, and error handling strategies.
type ConverterConfig struct {
	// ConverterID is the unique identifier for this converter (required)
	ConverterID string `yaml:"converter_id" json:"converter_id"`

	// TenantID is the tenant that owns this converter (required)
	TenantID string `yaml:"tenant_id" json:"tenant_id"`

	// InputTopic is the NATS topic to subscribe to (required)
	// Example: "hp.webhook.received"
	InputTopic string `yaml:"input_topic" json:"input_topic"`

	// OutputTopic is the NATS topic to publish transformed messages to.
	// Auto-generated as {InputTopic}.converted but stored for consistency
	OutputTopic string `yaml:"output_topic" json:"output_topic"`

	// ErrorTopic is the NATS topic to publish failed messages to (required)
	// Example: "hp.webhook.errors"
	ErrorTopic string `yaml:"error_topic" json:"error_topic"`

	// Transformations is a list of transformation rules (required, can be empty in Phase 1)
	Transformations []Transformation `yaml:"transformations" json:"transformations"`

	// InputSchema defines required fields for input validation (optional, nil in Phase 1)
	InputSchema *ValidationSchema `yaml:"input_schema" json:"input_schema"`

	// OutputSchema defines required fields for output validation (optional, nil in Phase 1)
	OutputSchema *ValidationSchema `yaml:"output_schema" json:"output_schema"`

	// ErrorHandling defines error handling strategies
	ErrorHandling ErrorHandlingConfig `yaml:"error_handling" json:"error_handling"`

	// MaxRetries is the maximum number of retry attempts (default: 3, range: 1-10)
	MaxRetries int `yaml:"max_retries" json:"max_retries"`

	// RetryBackoff is the backoff strategy for retries (default: "exponential")
	// Only "exponential" is supported in Phase 1
	RetryBackoff string `yaml:"retry_backoff" json:"retry_backoff"`
}

// Transformation defines a single transformation rule (placeholder for Phase 1)
type Transformation struct {
	// Source is the field to extract from input (optional)
	Source string `yaml:"source" json:"source"`

	// Target is the field name in output (required)
	Target string `yaml:"target" json:"target"`

	// Type is the target field type (string, int, float, bool, etc.)
	Type string `yaml:"type" json:"type"`

	// Expression is a calculated field expression (optional)
	// Example: "sum(order.line_items[].price)"
	Expression string `yaml:"expression" json:"expression"`

	// Function is a function call (optional)
	// Example: "lookup_customer_account(order.customer.email)"
	Function string `yaml:"function" json:"function"`

	// Condition is evaluated to determine if transformation applies (optional)
	// Example: "order.total > 5000"
	Condition string `yaml:"condition" json:"condition"`

	// Value is a static value to set (optional)
	// Used with Condition
	Value interface{} `yaml:"value" json:"value"`
}

// ValidationSchema defines input/output validation rules
type ValidationSchema struct {
	// RequiredFields is a list of required field paths (using JSONPath notation)
	// Example: ["order.id", "order.customer.email"]
	RequiredFields []string `yaml:"required_fields" json:"required_fields"`
}

// ErrorHandlingConfig defines how to handle errors at different stages
type ErrorHandlingConfig struct {
	// MissingFields defines behavior when required input fields are missing
	// Values: "skip" (skip field), "coerce" (attempt conversion), "fail" (reject message)
	// Default: "fail"
	MissingFields string `yaml:"missing_fields" json:"missing_fields"`

	// TypeMismatch defines behavior when type conversion fails
	// Values: "skip", "coerce", "fail"
	// Default: "fail"
	TypeMismatch string `yaml:"type_mismatch" json:"type_mismatch"`

	// ValidationError defines behavior when schema validation fails
	// Values: "skip", "coerce", "fail"
	// Default: "fail"
	ValidationError string `yaml:"validation_error" json:"validation_error"`
}

// TransformResult holds the result of a transformation
type TransformResult struct {
	// Success indicates if transformation succeeded
	Success bool

	// Data is the transformed message (or original if Phase 1 pass-through)
	Data interface{}

	// Errors contains any transformation errors that occurred
	Errors []TransformationError

	// RetryCount tracks how many times this message was retried
	RetryCount int
}

// TransformationError represents a single transformation error
type TransformationError struct {
	// Field is the field that failed transformation
	Field string

	// Message is the error message
	Message string

	// Type is the error category (validation, extraction, conversion, etc.)
	Type string

	// Timestamp when the error occurred
	Timestamp time.Time
}
