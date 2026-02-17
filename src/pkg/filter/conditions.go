package filter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ConditionEngine evaluates conditions against message payloads
type ConditionEngine struct {
	operators map[string]OperatorFunc
}

// OperatorFunc is a function that evaluates a condition
type OperatorFunc func(field interface{}, value interface{}) (bool, error)

// NewConditionEngine creates a new condition engine
func NewConditionEngine() *ConditionEngine {
	ce := &ConditionEngine{
		operators: make(map[string]OperatorFunc),
	}

	// Register all operators
	ce.operators["=="] = ce.opEqual
	ce.operators["!="] = ce.opNotEqual
	ce.operators[">"] = ce.opGreaterThan
	ce.operators["<"] = ce.opLessThan
	ce.operators[">="] = ce.opGreaterThanOrEqual
	ce.operators["<="] = ce.opLessThanOrEqual
	ce.operators["contains"] = ce.opContains
	ce.operators["startswith"] = ce.opStartsWith
	ce.operators["endswith"] = ce.opEndsWith
	ce.operators["regex_match"] = ce.opRegexMatch
	ce.operators["in_list"] = ce.opInList
	ce.operators["always"] = ce.opAlways

	return ce
}

// Evaluate evaluates a condition against a payload
func (ce *ConditionEngine) Evaluate(cond *Condition, payload interface{}) (bool, error) {
	// Special case: "always" operator doesn't need field or value
	if cond.Operator == "always" {
		opFunc, ok := ce.operators[cond.Operator]
		if !ok {
			return false, fmt.Errorf("unknown operator: %s", cond.Operator)
		}
		return opFunc(nil, nil)
	}

	// Extract field value from payload using dot notation
	fieldValue, err := ce.GetFieldValue(payload, cond.Field)
	if err != nil {
		return false, err
	}

	// Get operator function
	opFunc, ok := ce.operators[cond.Operator]
	if !ok {
		return false, fmt.Errorf("unknown operator: %s", cond.Operator)
	}

	// Evaluate
	return opFunc(fieldValue, cond.Value)
}

// GetFieldValue extracts a value from payload using dot notation
// e.g., "user.name" or "items[0].price"
func (ce *ConditionEngine) GetFieldValue(payload interface{}, path string) (interface{}, error) {
	if path == "" {
		return payload, nil
	}

	current := payload
	parts := strings.Split(path, ".")

	for _, part := range parts {
		if current == nil {
			return nil, nil
		}

		// Handle map access
		if m, ok := current.(map[string]interface{}); ok {
			var found bool
			current, found = m[part]
			if !found {
				return nil, nil
			}
			continue
		}

		// Handle map[interface{}]interface{} (from YAML)
		if m, ok := current.(map[interface{}]interface{}); ok {
			var found bool
			current, found = m[part]
			if !found {
				return nil, nil
			}
			continue
		}

		// Handle array access (e.g., "items[0]")
		if strings.Contains(part, "[") && strings.Contains(part, "]") {
			// Parse array index syntax
			openIdx := strings.Index(part, "[")
			closeIdx := strings.Index(part, "]")
			fieldName := part[:openIdx]
			indexStr := part[openIdx+1 : closeIdx]

			index, err := strconv.Atoi(indexStr)
			if err != nil {
				return nil, fmt.Errorf("invalid array index: %s", part)
			}

			// Get the array
			var arr []interface{}
			if m, ok := current.(map[string]interface{}); ok {
				val, ok := m[fieldName]
				if !ok {
					return nil, nil
				}
				arr, ok = val.([]interface{})
				if !ok {
					return nil, fmt.Errorf("field %s is not an array", fieldName)
				}
			} else if m, ok := current.(map[interface{}]interface{}); ok {
				val, ok := m[fieldName]
				if !ok {
					return nil, nil
				}
				arr, ok = val.([]interface{})
				if !ok {
					return nil, fmt.Errorf("field %s is not an array", fieldName)
				}
			}

			if index < 0 || index >= len(arr) {
				return nil, fmt.Errorf("array index out of bounds: %d", index)
			}
			current = arr[index]
			continue
		}

		return nil, fmt.Errorf("cannot access field %s on non-map value", part)
	}

	return current, nil
}

// Operator implementations

func (ce *ConditionEngine) opEqual(field interface{}, value interface{}) (bool, error) {
	if field == nil && value == nil {
		return true, nil
	}
	if field == nil || value == nil {
		return false, nil
	}

	// Try numeric comparison first
	if fNum, err := toFloat64(field); err == nil {
		if vNum, err := toFloat64(value); err == nil {
			return fNum == vNum, nil
		}
	}

	// String comparison
	return fmt.Sprint(field) == fmt.Sprint(value), nil
}

func (ce *ConditionEngine) opNotEqual(field interface{}, value interface{}) (bool, error) {
	result, err := ce.opEqual(field, value)
	return !result, err
}

func (ce *ConditionEngine) opGreaterThan(field interface{}, value interface{}) (bool, error) {
	fNum, err := toFloat64(field)
	if err != nil {
		return false, fmt.Errorf("field is not numeric: %w", err)
	}

	vNum, err := toFloat64(value)
	if err != nil {
		return false, fmt.Errorf("value is not numeric: %w", err)
	}

	return fNum > vNum, nil
}

func (ce *ConditionEngine) opLessThan(field interface{}, value interface{}) (bool, error) {
	fNum, err := toFloat64(field)
	if err != nil {
		return false, fmt.Errorf("field is not numeric: %w", err)
	}

	vNum, err := toFloat64(value)
	if err != nil {
		return false, fmt.Errorf("value is not numeric: %w", err)
	}

	return fNum < vNum, nil
}

func (ce *ConditionEngine) opGreaterThanOrEqual(field interface{}, value interface{}) (bool, error) {
	fNum, err := toFloat64(field)
	if err != nil {
		return false, fmt.Errorf("field is not numeric: %w", err)
	}

	vNum, err := toFloat64(value)
	if err != nil {
		return false, fmt.Errorf("value is not numeric: %w", err)
	}

	return fNum >= vNum, nil
}

func (ce *ConditionEngine) opLessThanOrEqual(field interface{}, value interface{}) (bool, error) {
	fNum, err := toFloat64(field)
	if err != nil {
		return false, fmt.Errorf("field is not numeric: %w", err)
	}

	vNum, err := toFloat64(value)
	if err != nil {
		return false, fmt.Errorf("value is not numeric: %w", err)
	}

	return fNum <= vNum, nil
}

func (ce *ConditionEngine) opContains(field interface{}, value interface{}) (bool, error) {
	fieldStr := fmt.Sprint(field)
	valueStr := fmt.Sprint(value)
	return strings.Contains(fieldStr, valueStr), nil
}

func (ce *ConditionEngine) opStartsWith(field interface{}, value interface{}) (bool, error) {
	fieldStr := fmt.Sprint(field)
	valueStr := fmt.Sprint(value)
	return strings.HasPrefix(fieldStr, valueStr), nil
}

func (ce *ConditionEngine) opEndsWith(field interface{}, value interface{}) (bool, error) {
	fieldStr := fmt.Sprint(field)
	valueStr := fmt.Sprint(value)
	return strings.HasSuffix(fieldStr, valueStr), nil
}

func (ce *ConditionEngine) opRegexMatch(field interface{}, value interface{}) (bool, error) {
	fieldStr := fmt.Sprint(field)
	pattern := fmt.Sprint(value)

	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("invalid regex pattern: %w", err)
	}

	return re.MatchString(fieldStr), nil
}

func (ce *ConditionEngine) opInList(field interface{}, value interface{}) (bool, error) {
	// Value should be a list
	var list []interface{}

	switch v := value.(type) {
	case []interface{}:
		list = v
	case []string:
		for _, item := range v {
			list = append(list, item)
		}
	default:
		return false, fmt.Errorf("value for in_list must be an array")
	}

	// Check if field is in list
	fieldStr := fmt.Sprint(field)
	for _, item := range list {
		if fmt.Sprint(item) == fieldStr {
			return true, nil
		}
	}

	return false, nil
}

func (ce *ConditionEngine) opAlways(field interface{}, value interface{}) (bool, error) {
	return true, nil
}

// Helper function to convert interface{} to float64
func toFloat64(v interface{}) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case float32:
		return float64(val), nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case int32:
		return float64(val), nil
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot convert string to number: %w", err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}
