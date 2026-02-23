package converter

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// =============================================================================
// AGGREGATION FUNCTIONS: sum, avg, count, max, min
// =============================================================================

// sumFunc calculates the sum of numeric values in an array
func sumFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("sum requires at least 1 argument")
	}

	arr := args[0]
	floats := filterNumerics(arr, ctx)
	if len(floats) == 0 {
		return float64(0), nil
	}

	sum := 0.0
	for _, v := range floats {
		sum += v
	}
	return sum, nil
}

// avgFunc calculates the average of numeric values in an array
func avgFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("avg requires at least 1 argument")
	}

	arr := args[0]
	floats := filterNumerics(arr, ctx)
	if len(floats) == 0 {
		return float64(0), nil
	}

	sum := 0.0
	for _, v := range floats {
		sum += v
	}
	return sum / float64(len(floats)), nil
}

// countFunc counts non-null elements in an array
func countFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("count requires at least 1 argument")
	}

	arr, ok := args[0].([]interface{})
	if !ok {
		return float64(0), nil
	}

	count := 0
	for _, v := range arr {
		if v != nil {
			count++
		}
	}
	return float64(count), nil
}

// maxFunc returns the maximum value in an array
func maxFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("max requires at least 1 argument")
	}

	arr := args[0]
	floats := filterNumerics(arr, ctx)
	if len(floats) == 0 {
		return float64(0), nil
	}

	max := floats[0]
	for _, v := range floats[1:] {
		if v > max {
			max = v
		}
	}
	return max, nil
}

// minFunc returns the minimum value in an array
func minFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("min requires at least 1 argument")
	}

	arr := args[0]
	floats := filterNumerics(arr, ctx)
	if len(floats) == 0 {
		return float64(0), nil
	}

	min := floats[0]
	for _, v := range floats[1:] {
		if v < min {
			min = v
		}
	}
	return min, nil
}

// =============================================================================
// STRING FUNCTIONS: concat, uppercase, lowercase, trim, split, replace
// =============================================================================

// concatFunc joins multiple values as strings
func concatFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("concat requires at least 1 argument")
	}

	// Handle nil as first argument
	if args[0] == nil {
		return nil, nil
	}

	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == nil {
			continue
		}
		parts = append(parts, toString(arg))
	}

	return strings.Join(parts, ""), nil
}

// uppercaseFunc converts string to uppercase
func uppercaseFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("uppercase requires at least 1 argument")
	}

	if args[0] == nil {
		return nil, nil
	}

	return strings.ToUpper(toString(args[0])), nil
}

// lowercaseFunc converts string to lowercase
func lowercaseFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("lowercase requires at least 1 argument")
	}

	if args[0] == nil {
		return nil, nil
	}

	return strings.ToLower(toString(args[0])), nil
}

// trimFunc removes leading/trailing whitespace or custom characters
func trimFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("trim requires at least 1 argument")
	}

	if args[0] == nil {
		return nil, nil
	}

	str := toString(args[0])

	// If no second argument, trim whitespace
	if len(args) < 2 {
		return strings.TrimSpace(str), nil
	}

	// Otherwise trim specific characters
	chars := toString(args[1])
	return strings.Trim(str, chars), nil
}

// splitFunc splits a string by separator
func splitFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("split requires at least 2 arguments")
	}

	if args[0] == nil {
		return nil, nil
	}

	str := toString(args[0])
	sep := toString(args[1])

	parts := strings.Split(str, sep)
	result := make([]interface{}, len(parts))
	for i, p := range parts {
		result[i] = p
	}
	return result, nil
}

// replaceFunc replaces all occurrences of old with new
func replaceFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("replace requires at least 3 arguments")
	}

	if args[0] == nil {
		return nil, nil
	}

	str := toString(args[0])
	old := toString(args[1])
	new := toString(args[2])

	return strings.ReplaceAll(str, old, new), nil
}

// =============================================================================
// MATH FUNCTIONS: multiply, divide
// =============================================================================

// multiplyFunc multiplies two numbers
func multiplyFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("multiply requires at least 2 arguments")
	}

	a, err := toFloat64(args[0], ctx)
	if err != nil {
		return nil, fmt.Errorf("multiply: first argument: %w", err)
	}

	b, err := toFloat64(args[1], ctx)
	if err != nil {
		return nil, fmt.Errorf("multiply: second argument: %w", err)
	}

	return a * b, nil
}

// divideFunc divides two numbers (returns 0 if divisor is 0)
func divideFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("divide requires at least 2 arguments")
	}

	a, err := toFloat64(args[0], ctx)
	if err != nil {
		return nil, fmt.Errorf("divide: first argument: %w", err)
	}

	b, err := toFloat64(args[1], ctx)
	if err != nil {
		return nil, fmt.Errorf("divide: second argument: %w", err)
	}

	if b == 0 {
		// Graceful degradation: log warning and return 0
		return float64(0), nil
	}

	return a / b, nil
}

// =============================================================================
// TYPE CONVERSION FUNCTIONS: as_string, as_number
// =============================================================================

// asStringFunc converts a value to string
func asStringFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("as_string requires at least 1 argument")
	}

	if args[0] == nil {
		return nil, nil
	}

	return toString(args[0]), nil
}

// asNumberFunc converts a value to float64
func asNumberFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("as_number requires at least 1 argument")
	}

	if args[0] == nil {
		return nil, nil
	}

	num, err := toFloat64(args[0], ctx)
	if err != nil {
		// Graceful degradation: return nil on non-convertible
		return nil, nil
	}

	return num, nil
}

// =============================================================================
// DATE/TIME FUNCTIONS: now, date_format, date_add
// =============================================================================

// nowFunc returns current time in RFC3339 format (UTC)
func nowFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	return time.Now().UTC().Format(time.RFC3339), nil
}

// dateFormatFunc formats a timestamp
func dateFormatFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("date_format requires at least 2 arguments")
	}

	if args[0] == nil {
		return nil, nil
	}

	// Parse the timestamp
	ts := toString(args[0])
	t, err := parseTime(ts)
	if err != nil {
		return nil, fmt.Errorf("date_format: invalid timestamp: %w", err)
	}

	// Get format string
	format := toString(args[1])

	switch format {
	case "date":
		return t.Format("2006-01-02"), nil
	case "datetime":
		return t.Format(time.RFC3339), nil
	default:
		// Assume it's a Go time format string
		return t.Format(format), nil
	}
}

// dateAddFunc adds days to a timestamp
func dateAddFunc(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("date_add requires at least 2 arguments")
	}

	if args[0] == nil {
		return nil, nil
	}

	// Parse the timestamp
	ts := toString(args[0])
	t, err := parseTime(ts)
	if err != nil {
		return nil, fmt.Errorf("date_add: invalid timestamp: %w", err)
	}

	// Get number of days
	days, err := toFloat64(args[1], ctx)
	if err != nil {
		return nil, fmt.Errorf("date_add: invalid days value: %w", err)
	}

	// Add days and return in RFC3339 format
	newTime := t.AddDate(0, 0, int(days))
	return newTime.Format(time.RFC3339), nil
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// toFloat64 converts a value to float64 with lenient coercion
func toFloat64(v interface{}, ctx context.Context) (float64, error) {
	if v == nil {
		return 0, fmt.Errorf("cannot convert nil to number")
	}

	switch val := v.(type) {
	case float64:
		return val, nil
	case float32:
		return float64(val), nil
	case int:
		return float64(val), nil
	case int32:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot convert string '%s' to number", val)
		}
		return f, nil
	case bool:
		if val {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("cannot convert type %T to number", v)
	}
}

// toString converts a value to string
func toString(v interface{}) string {
	if v == nil {
		return ""
	}

	switch val := v.(type) {
	case string:
		return val
	case float64:
		// Use a reasonable format for floats
		if val == float64(int64(val)) {
			return fmt.Sprintf("%.0f", val)
		}
		return fmt.Sprintf("%g", val)
	case float32:
		return fmt.Sprintf("%g", val)
	case int:
		return strconv.Itoa(val)
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case bool:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// filterNumerics extracts numeric values from an array, skipping non-convertible values
func filterNumerics(arr interface{}, ctx context.Context) []float64 {
	slice, ok := arr.([]interface{})
	if !ok {
		return []float64{}
	}

	var result []float64
	for _, v := range slice {
		if v == nil {
			continue
		}

		if f, err := toFloat64(v, ctx); err == nil {
			result = append(result, f)
		}
		// Otherwise skip non-convertible value
	}

	return result
}

// parseTime parses a timestamp in various formats
func parseTime(ts string) (time.Time, error) {
	// Try RFC3339 first (most common)
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t, nil
	}

	// Try RFC3339Nano
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t, nil
	}

	// Try common date format
	if t, err := time.Parse("2006-01-02", ts); err == nil {
		return t, nil
	}

	// Try Unix timestamp
	if i, err := strconv.ParseInt(ts, 10, 64); err == nil {
		return time.Unix(i, 0), nil
	}

	return time.Time{}, fmt.Errorf("cannot parse time: %s", ts)
}
