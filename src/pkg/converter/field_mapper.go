package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tidwall/gjson"
)

// FieldMapper handles extraction and type conversion of fields from JSON payloads.
type FieldMapper struct {
	logger Logger
	ctx    context.Context
}

// NewFieldMapper creates a new field mapper instance.
func NewFieldMapper(ctx context.Context, logger Logger) *FieldMapper {
	if ctx == nil {
		ctx = context.Background()
	}
	return &FieldMapper{
		logger: logger,
		ctx:    ctx,
	}
}

// ExtractField retrieves a field from JSON payload using dot notation or JSONPath.
// Returns the value (typed), or nil if field doesn't exist.
// Errors are logged but not returned (lenient approach).
func (fm *FieldMapper) ExtractField(payload []byte, path string) interface{} {
	if len(payload) == 0 || path == "" {
		return nil
	}

	// Use gjson to extract the value
	result := gjson.GetBytes(payload, path)

	// If value doesn't exist, return nil
	if !result.Exists() {
		return nil
	}

	// Return the raw value - gjson handles type conversion
	return result.Value()
}

// ExtractFieldWithType retrieves a field and converts it to the specified type.
// Supports: "string", "int", "int64", "float", "float64", "bool"
// Returns the zero value for the type if field doesn't exist (logs warning).
func (fm *FieldMapper) ExtractFieldWithType(payload []byte, path string, targetType string) interface{} {
	value := fm.ExtractField(payload, path)

	// Attempt type conversion regardless of whether value is nil
	return fm.coerceType(value, targetType)
}

// coerceType attempts to convert a value to the target type.
// Uses lenient conversion (zero values on failure) and logs warnings.
func (fm *FieldMapper) coerceType(value interface{}, targetType string) interface{} {
	switch targetType {
	case "string":
		return fm.toString(value)
	case "int":
		return fm.toInt(value)
	case "int64":
		return fm.toInt64(value)
	case "float":
		return fm.toFloat(value)
	case "float64":
		return fm.toFloat64(value)
	case "bool":
		return fm.toBool(value)
	default:
		// Unknown type - return as-is
		return value
	}
}

func (fm *FieldMapper) toString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		// Check if it's a whole number
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		// Try JSON marshaling for complex types
		if data, err := json.Marshal(v); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", v)
	}
}

func (fm *FieldMapper) toInt(value interface{}) int {
	return int(fm.toInt64(value))
}

func (fm *FieldMapper) toInt64(value interface{}) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case string:
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i
		}
		if fm.logger != nil {
			fm.logger.WarnContext(fm.ctx, "failed to convert string to int64, using 0", "value", v)
		}
		return 0
	case bool:
		if v {
			return 1
		}
		return 0
	case nil:
		return 0
	default:
		if fm.logger != nil {
			fm.logger.WarnContext(fm.ctx, "cannot convert type to int64, using 0", "type", fmt.Sprintf("%T", v))
		}
		return 0
	}
}

func (fm *FieldMapper) toFloat(value interface{}) float32 {
	return float32(fm.toFloat64(value))
}

func (fm *FieldMapper) toFloat64(value interface{}) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
		if fm.logger != nil {
			fm.logger.WarnContext(fm.ctx, "failed to convert string to float64, using 0.0", "value", v)
		}
		return 0.0
	case bool:
		if v {
			return 1.0
		}
		return 0.0
	case nil:
		return 0.0
	default:
		if fm.logger != nil {
			fm.logger.WarnContext(fm.ctx, "cannot convert type to float64, using 0.0", "type", fmt.Sprintf("%T", v))
		}
		return 0.0
	}
}

func (fm *FieldMapper) toBool(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		// Parse various string representations of bool
		switch v {
		case "true", "True", "TRUE", "1", "yes", "Yes", "YES":
			return true
		case "false", "False", "FALSE", "0", "no", "No", "NO", "":
			return false
		default:
			if fm.logger != nil {
				fm.logger.WarnContext(fm.ctx, "unknown bool value, using false", "value", v)
			}
			return false
		}
	case nil:
		return false
	default:
		// Non-empty values are truthy
		return true
	}
}

// ExtractAll returns all fields at the path as a slice.
// Useful for array processing in expressions.
func (fm *FieldMapper) ExtractAll(payload []byte, path string) []interface{} {
	if len(payload) == 0 || path == "" {
		return nil
	}

	result := gjson.GetBytes(payload, path)
	if !result.Exists() {
		return nil
	}

	// If result is an array, return all elements as interface{}
	if result.IsArray() {
		var values []interface{}
		for _, item := range result.Array() {
			values = append(values, item.Value())
		}
		return values
	}

	// If single value, return as slice with one element
	return []interface{}{result.Value()}
}
