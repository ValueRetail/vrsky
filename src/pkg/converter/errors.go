package converter

import (
	"errors"
	"fmt"
)

// Error types for converter operations

// ErrConfigNotFound is returned when config service returns 404
var ErrConfigNotFound = errors.New("converter config not found")

// ErrConfigInvalid is returned when configuration fails validation
var ErrConfigInvalid = errors.New("converter config invalid")

// ErrConfigServiceUnavailable is returned when config service is unreachable
var ErrConfigServiceUnavailable = errors.New("config service unavailable")

// ErrTransformationFailed is returned when transformation rule execution fails
var ErrTransformationFailed = errors.New("transformation failed")

// ErrFieldNotFound is returned when a required field is not found in input
var ErrFieldNotFound = errors.New("field not found")

// ErrTypeConversionFailed is returned when type conversion fails
var ErrTypeConversionFailed = errors.New("type conversion failed")

// ErrValidationFailed is returned when schema validation fails
var ErrValidationFailed = errors.New("validation failed")

// ErrExpressionEvaluation is returned when expression parsing/evaluation fails
var ErrExpressionEvaluation = errors.New("expression evaluation failed")

// ErrLookupFailed is returned when a lookup function fails
var ErrLookupFailed = errors.New("lookup failed")

// ConfigError represents an error with configuration context
type ConfigError struct {
	TenantID    string
	ConverterID string
	Field       string
	Message     string
	Cause       error
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("config error (tenant=%s, converter=%s, field=%s): %s: %v",
		e.TenantID, e.ConverterID, e.Field, e.Message, e.Cause)
}

func (e *ConfigError) Unwrap() error {
	return e.Cause
}

// TransformError represents an error during transformation with context
type TransformError struct {
	TenantID    string
	ConverterID string
	MessageID   string
	RuleIndex   int
	Field       string
	Message     string
	Cause       error
}

func (e *TransformError) Error() string {
	return fmt.Sprintf("transform error (tenant=%s, converter=%s, message=%s, rule=%d, field=%s): %s: %v",
		e.TenantID, e.ConverterID, e.MessageID, e.RuleIndex, e.Field, e.Message, e.Cause)
}

func (e *TransformError) Unwrap() error {
	return e.Cause
}

// LookupError represents an error during lookup function execution
type LookupError struct {
	TenantID    string
	ConverterID string
	Function    string
	Message     string
	Cause       error
}

func (e *LookupError) Error() string {
	return fmt.Sprintf("lookup error (tenant=%s, converter=%s, function=%s): %s: %v",
		e.TenantID, e.ConverterID, e.Function, e.Message, e.Cause)
}

func (e *LookupError) Unwrap() error {
	return e.Cause
}
