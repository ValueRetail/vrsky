package converter

import (
	"context"
	"testing"
)

func TestFieldMapper_ExtractField(t *testing.T) {
	logger := NewTestLogger()
	fm := NewFieldMapper(context.Background(), logger)

	tests := []struct {
		name     string
		payload  []byte
		path     string
		expected interface{}
	}{
		// Simple field access
		{"simple string field", []byte(`{"name":"John"}`), "name", "John"},
		{"simple number field", []byte(`{"age":30}`), "age", float64(30)},
		{"simple bool field", []byte(`{"active":true}`), "active", true},
		{"simple null field", []byte(`{"value":null}`), "value", nil},

		// Nested field access
		{"nested object", []byte(`{"user":{"name":"Jane"}}`), "user.name", "Jane"},
		{"deeply nested", []byte(`{"a":{"b":{"c":{"d":"deep"}}}}`), "a.b.c.d", "deep"},
		{"nested with number", []byte(`{"order":{"total":99.99}}`), "order.total", 99.99},

		// Array access
		{"array index", []byte(`{"items":["a","b","c"]}`), "items.0", "a"},
		{"array middle index", []byte(`{"items":["a","b","c"]}`), "items.1", "b"},
		{"array of objects", []byte(`{"items":[{"id":1},{"id":2}]}`), "items.0.id", float64(1)},

		// Array queries (JSONPath)
		{"array query length", []byte(`{"items":["a","b","c"]}`), "items.#", float64(3)},

		// Missing fields
		{"missing simple field", []byte(`{"name":"John"}`), "age", nil},
		{"missing nested field", []byte(`{"user":{"name":"Jane"}}`), "user.email", nil},
		{"missing root field", []byte(`{"name":"John"}`), "missing", nil},

		// Empty cases
		{"empty payload", []byte(``), "field", nil},
		{"empty path", []byte(`{"name":"John"}`), "", nil},

		// Complex nested structure
		{"complex structure", []byte(`{
			"order": {
				"id": "ORD-123",
				"customer": {"name":"Alice","email":"alice@example.com"},
				"items": [
					{"sku":"SKU1","qty":2,"price":10.50},
					{"sku":"SKU2","qty":1,"price":25.00}
				]
			}
		}`), "order.customer.name", "Alice"},

		// Special characters in keys
		{"key with hyphen", []byte(`{"user-name":"John"}`), "user-name", "John"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fm.ExtractField(tt.payload, tt.path)
			if got != tt.expected {
				t.Errorf("ExtractField() got %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFieldMapper_ExtractFieldWithType(t *testing.T) {
	logger := NewTestLogger()
	fm := NewFieldMapper(context.Background(), logger)

	tests := []struct {
		name       string
		payload    []byte
		path       string
		targetType string
		expected   interface{}
	}{
		// String conversions
		{"string to string", []byte(`{"value":"hello"}`), "value", "string", "hello"},
		{"number to string", []byte(`{"value":42}`), "value", "string", "42"},
		{"float to string", []byte(`{"value":3.14}`), "value", "string", "3.14"},
		{"bool to string", []byte(`{"value":true}`), "value", "string", "true"},

		// Int conversions
		{"string to int", []byte(`{"value":"42"}`), "value", "int", 42},
		{"float to int", []byte(`{"value":42.9}`), "value", "int", 42},
		{"bool true to int", []byte(`{"value":true}`), "value", "int", 1},
		{"bool false to int", []byte(`{"value":false}`), "value", "int", 0},

		// Int64 conversions
		{"string to int64", []byte(`{"value":"9223372036854775806"}`), "value", "int64", int64(9223372036854775806)},
		{"float to int64", []byte(`{"value":999.5}`), "value", "int64", int64(999)},

		// Float conversions
		{"string to float", []byte(`{"value":"3.14"}`), "value", "float", float32(3.14)},
		{"int to float", []byte(`{"value":42}`), "value", "float", float32(42)},

		// Float64 conversions
		{"string to float64", []byte(`{"value":"3.14159"}`), "value", "float64", 3.14159},
		{"int to float64", []byte(`{"value":42}`), "value", "float64", float64(42)},

		// Bool conversions
		{"bool to bool", []byte(`{"value":true}`), "value", "bool", true},
		{"number 1 to bool", []byte(`{"value":1}`), "value", "bool", true},
		{"number 0 to bool", []byte(`{"value":0}`), "value", "bool", false},
		{"string 'true' to bool", []byte(`{"value":"true"}`), "value", "bool", true},
		{"string 'false' to bool", []byte(`{"value":"false"}`), "value", "bool", false},

		// Null conversions
		{"null to string", []byte(`{"value":null}`), "value", "string", ""},
		{"null to int", []byte(`{"value":null}`), "value", "int", 0},
		{"null to float", []byte(`{"value":null}`), "value", "float", float32(0)},
		{"null to bool", []byte(`{"value":null}`), "value", "bool", false},

		// Missing field conversions (should return zero values)
		{"missing to string", []byte(`{"other":"value"}`), "missing", "string", ""},
		{"missing to int", []byte(`{"other":"value"}`), "missing", "int", 0},
		{"missing to bool", []byte(`{"other":"value"}`), "missing", "bool", false},

		// Unknown type falls through
		{"unknown type", []byte(`{"value":"test"}`), "value", "unknown", "test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fm.ExtractFieldWithType(tt.payload, tt.path, tt.targetType)
			if got != tt.expected {
				t.Errorf("ExtractFieldWithType() got %v (type %T), want %v (type %T)",
					got, got, tt.expected, tt.expected)
			}
		})
	}
}

func TestFieldMapper_ExtractAll(t *testing.T) {
	logger := NewTestLogger()
	fm := NewFieldMapper(context.Background(), logger)

	tests := []struct {
		name     string
		payload  []byte
		path     string
		expected []interface{}
	}{
		// Simple arrays
		{"string array", []byte(`{"items":["a","b","c"]}`), "items", []interface{}{"a", "b", "c"}},
		{"number array", []byte(`{"nums":[1,2,3]}`), "nums", []interface{}{float64(1), float64(2), float64(3)}},

		// Arrays of objects
		{"objects array", []byte(`{"users":[{"id":1},{"id":2}]}`), "users", []interface{}{
			map[string]interface{}{"id": float64(1)},
			map[string]interface{}{"id": float64(2)},
		}},

		// Nested arrays
		{"nested array", []byte(`{"items":[{"values":[1,2]},{"values":[3,4]}]}`), "items.0.values",
			[]interface{}{float64(1), float64(2)}},

		// Single value (returns as slice)
		{"single value", []byte(`{"name":"John"}`), "name", []interface{}{"John"}},

		// Missing field
		{"missing field", []byte(`{"other":"value"}`), "missing", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fm.ExtractAll(tt.payload, tt.path)
			if !sliceEqual(got, tt.expected) {
				t.Errorf("ExtractAll() got %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFieldMapper_CoerceType(t *testing.T) {
	logger := NewTestLogger()
	fm := NewFieldMapper(context.Background(), logger)

	tests := []struct {
		name       string
		value      interface{}
		targetType string
		expected   interface{}
	}{
		// Identity conversions
		{"string identity", "hello", "string", "hello"},
		{"float64 identity", float64(42), "float64", float64(42)},
		{"bool identity", true, "bool", true},

		// Cross-type conversions
		{"float string conversion", 3.14, "string", "3.14"},
		{"bool true string", true, "string", "true"},
		{"bool false string", false, "string", "false"},

		// Edge cases
		{"invalid bool string", "maybe", "bool", false}, // logs warning
		{"invalid int string", "abc", "int", 0},         // logs warning

		// Whole number float to string
		{"whole number float string", 42.0, "string", "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fm.coerceType(tt.value, tt.targetType)
			if got != tt.expected {
				t.Errorf("coerceType() got %v (type %T), want %v (type %T)",
					got, got, tt.expected, tt.expected)
			}
		})
	}
}

// Helper function to compare slices of interface{}
func sliceEqual(a, b []interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// Use JSON comparison for complex types
		if !valueEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// valueEqual compares two interface{} values, handling maps and floats
func valueEqual(a, b interface{}) bool {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !valueEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case nil:
		return b == nil
	default:
		return a == b
	}
}

// TestLogger for testing
type TestLogger struct{}

func NewTestLogger() Logger {
	return &TestLogger{}
}

func (tl *TestLogger) InfoContext(ctx context.Context, msg string, args ...interface{})  {}
func (tl *TestLogger) WarnContext(ctx context.Context, msg string, args ...interface{})  {}
func (tl *TestLogger) ErrorContext(ctx context.Context, msg string, args ...interface{}) {}
func (tl *TestLogger) Warn(msg string)                                                   {}
func (tl *TestLogger) Error(msg string)                                                  {}

// TestFieldMapper_ComplexJSONPath tests JSONPath query support
func TestFieldMapper_ComplexJSONPath(t *testing.T) {
	logger := NewTestLogger()
	ctx := context.Background()
	fm := NewFieldMapper(ctx, logger)

	payload := []byte(`{
		"order": {
			"id": "ORD001",
			"items": [
				{"sku": "SKU-001", "name": "Laptop", "price": 1299.99, "qty": 1},
				{"sku": "SKU-002", "name": "Mouse", "price": 29.99, "qty": 2},
				{"sku": "SKU-003", "name": "Keyboard", "price": 149.99, "qty": 1}
			]
		}
	}`)

	tests := []struct {
		name        string
		path        string
		wantCount   int
		wantContent bool
	}{
		{
			name:        "all items in array",
			path:        "order.items",
			wantCount:   3,
			wantContent: true,
		},
		{
			name:        "array access - first item",
			path:        "order.items.0",
			wantCount:   1,
			wantContent: true,
		},
		{
			name:        "array access - second item",
			path:        "order.items.1",
			wantCount:   1,
			wantContent: true,
		},
		{
			name:        "nested field access",
			path:        "order.items.0.name",
			wantCount:   1,
			wantContent: true,
		},
		{
			name:        "non-existent path",
			path:        "order.items.99.name",
			wantCount:   0,
			wantContent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test with ExtractAll for array queries
			if tt.wantCount > 1 {
				result := fm.ExtractAll(payload, tt.path)
				if tt.wantContent {
					if result == nil || len(result) != tt.wantCount {
						t.Errorf("expected %d results, got %d", tt.wantCount, len(result))
					}
				} else {
					if result != nil && len(result) > 0 {
						t.Errorf("expected empty result, got %v", result)
					}
				}
			} else {
				// Test with ExtractField for single values
				result := fm.ExtractField(payload, tt.path)
				if tt.wantContent {
					if result == nil {
						t.Errorf("expected non-nil result")
					}
				} else {
					if result != nil {
						t.Errorf("expected nil, got %v", result)
					}
				}
			}
		})
	}
}

// TestFieldMapper_ExtractFieldsByFilter tests the ExtractFieldsByFilter helper
func TestFieldMapper_ExtractFieldsByFilter(t *testing.T) {
	logger := NewTestLogger()
	ctx := context.Background()
	fm := NewFieldMapper(ctx, logger)

	payload := []byte(`{
		"items": [
			{"id": 1, "status": "active", "balance": 100.50},
			{"id": 2, "status": "inactive", "balance": 250.75},
			{"id": 3, "status": "active", "balance": 50.25}
		]
	}`)

	tests := []struct {
		name       string
		arrayPath  string
		filterExpr string
		wantCount  int
		wantErr    bool
	}{
		{
			name:       "extract all items without filter",
			arrayPath:  "items",
			filterExpr: "",
			wantCount:  0, // Empty filter returns nil
			wantErr:    false,
		},
		{
			name:       "empty array path",
			arrayPath:  "",
			filterExpr: "status == \"active\"",
			wantCount:  0,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fm.ExtractFieldsByFilter(payload, tt.arrayPath, tt.filterExpr)

			if len(result) != tt.wantCount {
				t.Errorf("expected %d results, got %d", tt.wantCount, len(result))
			}
		})
	}
}
