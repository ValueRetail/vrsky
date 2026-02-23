package converter

import (
	"context"
	"testing"
)

func TestExpressionEvaluator_Evaluate(t *testing.T) {
	logger := NewTestLogger()
	registry := NewFunctionRegistry(context.Background(), logger)
	ee := NewExpressionEvaluator(context.Background(), logger, registry)

	tests := []struct {
		name       string
		expression string
		variables  map[string]interface{}
		expected   interface{}
		wantErr    bool
	}{
		// Simple literals
		{"number literal", "42", nil, 42, false},
		{"string literal", `"hello"`, nil, "hello", false},
		{"bool literal true", "true", nil, true, false},
		{"bool literal false", "false", nil, false, false},

		// Variable references
		{"simple variable", "name", map[string]interface{}{"name": "John"}, "John", false},
		{"number variable", "age", map[string]interface{}{"age": float64(30)}, float64(30), false},

		// Arithmetic operators
		{"addition", "5 + 3", nil, 8, false},
		{"subtraction", "10 - 4", nil, 6, false},
		{"multiplication", "6 * 7", nil, 42, false},
		{"division", "20 / 4", nil, float64(5), false},
		{"modulo", "17 % 5", nil, 2, false},
		{"complex arithmetic", "(10 + 5) * 2", nil, 30, false},

		// Comparison operators
		{"equal numbers", "5 == 5", nil, true, false},
		{"not equal numbers", "5 != 3", nil, true, false},
		{"less than", "3 < 5", nil, true, false},
		{"less than or equal", "5 <= 5", nil, true, false},
		{"greater than", "5 > 3", nil, true, false},
		{"greater than or equal", "5 >= 5", nil, true, false},

		// Logical operators
		{"logical and true", "true && true", nil, true, false},
		{"logical and false", "true && false", nil, false, false},
		{"logical or true", "false || true", nil, true, false},
		{"logical or false", "false || false", nil, false, false},
		{"logical not", "!true", nil, false, false},

		// String operations
		{"string concatenation", `"Hello" + " " + "World"`, nil, "Hello World", false},

		// Field references with variables
		{"nested field", "order.total", map[string]interface{}{
			"order": map[string]interface{}{"total": float64(99.99)},
		}, float64(99.99), false},

		// Array operations
		{"array length", `len([1, 2, 3])`, nil, 3, false},

		// Conditions
		{"condition expression", "price > 100", map[string]interface{}{"price": float64(150)}, true, false},

		// Error cases
		{"undefined variable", "undefined_var", nil, nil, true},
		{"invalid syntax", "5 +", nil, nil, true},
		{"type error", `"string" + 5`, nil, nil, true},

		// Empty expression
		{"empty expression", "", nil, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ee.Evaluate(tt.expression, tt.variables)

			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got != tt.expected {
				t.Errorf("Evaluate() got %v (type %T), want %v (type %T)", got, got, tt.expected, tt.expected)
			}
		})
	}
}

func TestExpressionEvaluator_EvaluateCondition(t *testing.T) {
	logger := NewTestLogger()
	registry := NewFunctionRegistry(context.Background(), logger)
	ee := NewExpressionEvaluator(context.Background(), logger, registry)

	tests := []struct {
		name       string
		expression string
		variables  map[string]interface{}
		expected   bool
		wantErr    bool
	}{
		// Simple conditions
		{"true condition", "true", nil, true, false},
		{"false condition", "false", nil, false, false},

		// Comparison conditions
		{"greater than true", "10 > 5", nil, true, false},
		{"greater than false", "3 > 5", nil, false, false},
		{"equal true", "5 == 5", nil, true, false},
		{"equal false", "5 == 3", nil, false, false},

		// Logical conditions
		{"and true", "true && true", nil, true, false},
		{"and false", "true && false", nil, false, false},
		{"or true", "false || true", nil, true, false},

		// Field conditions
		{"field condition", "age >= 18", map[string]interface{}{"age": float64(25)}, true, false},
		{"field condition false", "age >= 18", map[string]interface{}{"age": float64(15)}, false, false},

		// Type coercion
		{"non-empty string is true", `"hello" != ""`, nil, true, false},
		{"non-zero is true", "1", nil, true, false},

		// Empty condition (always true)
		{"empty condition", "", nil, true, false},

		// Error cases
		{"invalid syntax", "5 +", nil, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ee.EvaluateCondition(tt.expression, tt.variables)

			if (err != nil) != tt.wantErr {
				t.Errorf("EvaluateCondition() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got != tt.expected {
				t.Errorf("EvaluateCondition() got %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExpressionEvaluator_Caching(t *testing.T) {
	logger := NewTestLogger()
	registry := NewFunctionRegistry(context.Background(), logger)
	ee := NewExpressionEvaluator(context.Background(), logger, registry)

	expression := "10 + 5"
	variables := map[string]interface{}{}

	// First evaluation compiles and caches
	result1, err := ee.Evaluate(expression, variables)
	if err != nil {
		t.Fatalf("first Evaluate failed: %v", err)
	}

	cacheSize := len(ee.compiled)
	if cacheSize != 1 {
		t.Errorf("expected cache size 1, got %d", cacheSize)
	}

	// Second evaluation uses cache
	result2, err := ee.Evaluate(expression, variables)
	if err != nil {
		t.Fatalf("second Evaluate failed: %v", err)
	}

	if cacheSize != len(ee.compiled) {
		t.Errorf("cache size changed after second evaluation")
	}

	if result1 != result2 {
		t.Errorf("cached results differ: %v vs %v", result1, result2)
	}

	// Clear cache
	ee.ClearCache()
	if len(ee.compiled) != 0 {
		t.Errorf("cache not cleared")
	}
}

func TestExpressionEvaluator_FieldAccess(t *testing.T) {
	logger := NewTestLogger()
	registry := NewFunctionRegistry(context.Background(), logger)
	ee := NewExpressionEvaluator(context.Background(), logger, registry)

	tests := []struct {
		name       string
		expression string
		variables  map[string]interface{}
		expected   interface{}
		wantErr    bool
	}{
		// Nested object access
		{"nested access", "user.name", map[string]interface{}{
			"user": map[string]interface{}{"name": "Alice"},
		}, "Alice", false},

		// Array access using bracket notation
		{"array bracket access", `items[0]`, map[string]interface{}{
			"items": []interface{}{"a", "b", "c"},
		}, "a", false},

		// Nested with bracket
		{"nested with bracket", `order.items[0].price`, map[string]interface{}{
			"order": map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"price": float64(29.99)},
				},
			},
		}, float64(29.99), false},

		// Null/nil field returns nil
		{"null field", "value", map[string]interface{}{"value": nil}, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ee.Evaluate(tt.expression, tt.variables)

			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got != tt.expected {
				t.Errorf("Evaluate() got %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExpressionEvaluator_SupportedOperators(t *testing.T) {
	logger := NewTestLogger()
	registry := NewFunctionRegistry(context.Background(), logger)
	ee := NewExpressionEvaluator(context.Background(), logger, registry)

	ops := ee.SupportedOperators()
	if ops == nil {
		t.Fatal("SupportedOperators returned nil")
	}

	expectedKeys := []string{"Arithmetic", "Comparison", "Logical", "Array", "String", "Type"}
	for _, key := range expectedKeys {
		if _, exists := ops[key]; !exists {
			t.Errorf("expected operator category '%s' not found", key)
		}
	}
}
