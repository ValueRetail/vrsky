package converter

import (
	"context"
	"testing"
)

func TestRuleEngine_ExecuteTransformations(t *testing.T) {
	logger := NewTestLogger()
	ctx := context.Background()
	fm := NewFieldMapper(ctx, logger)
	ee := NewExpressionEvaluator(ctx, logger, NewFunctionRegistry(ctx, logger))
	re := NewRuleEngine(ctx, logger, fm, ee, NewFunctionRegistry(ctx, logger))

	tests := []struct {
		name     string
		payload  []byte
		rules    []Transformation
		expected map[string]interface{}
		wantErr  bool
	}{
		// Simple field mapping
		{
			name:    "simple field mapping",
			payload: []byte(`{"name":"John","age":30}`),
			rules: []Transformation{
				{Source: "name", Target: "customer_name"},
				{Source: "age", Target: "customer_age"},
			},
			expected: map[string]interface{}{
				"customer_name": "John",
				"customer_age":  float64(30),
			},
			wantErr: false,
		},

		// Type conversion
		{
			name:    "type conversion",
			payload: []byte(`{"total":"99.99"}`),
			rules: []Transformation{
				{Source: "total", Target: "amount", Type: "float"},
			},
			expected: map[string]interface{}{
				"amount": float32(99.99),
			},
			wantErr: false,
		},

		// Expression evaluation
		{
			name:    "expression evaluation",
			payload: []byte(`{"qty":5,"price":10}`),
			rules: []Transformation{
				{Expression: "qty * price", Target: "total"},
			},
			expected: map[string]interface{}{
				"total": float64(50),  // JSON unmarshals numbers to float64
			},
			wantErr: false,
		},

		// Static value
		{
			name:    "static value",
			payload: []byte(`{"id":"123"}`),
			rules: []Transformation{
				{Value: "completed", Target: "status"},
			},
			expected: map[string]interface{}{
				"status": "completed",
			},
			wantErr: false,
		},

		// Conditional transformation
		{
			name:    "conditional transformation",
			payload: []byte(`{"order_total":150,"premium":true}`),
			rules: []Transformation{
				{
					Target:    "discount_applied",
					Value:     true,
					Condition: "premium == true",
				},
			},
			expected: map[string]interface{}{
				"discount_applied": true,
			},
			wantErr: false,
		},

		// Conditional not applied
		{
			name:    "conditional not applied",
			payload: []byte(`{"order_total":50,"premium":false}`),
			rules: []Transformation{
				{
					Source:    "order_total",
					Target:    "discount_applied",
					Condition: "premium == true",
					Value:     true,
				},
			},
			expected: map[string]interface{}{},
			wantErr:  false,
		},

		// Nested field extraction
		{
			name:    "nested field extraction",
			payload: []byte(`{"customer":{"name":"Alice","email":"alice@example.com"}}`),
			rules: []Transformation{
				{Source: "customer.name", Target: "cust_name"},
				{Source: "customer.email", Target: "cust_email"},
			},
			expected: map[string]interface{}{
				"cust_name":  "Alice",
				"cust_email": "alice@example.com",
			},
			wantErr: false,
		},

		// Missing field (source optional)
		{
			name:    "missing field returns nil",
			payload: []byte(`{"name":"John"}`),
			rules: []Transformation{
				{Source: "age", Target: "customer_age"},
			},
			expected: map[string]interface{}{
				"customer_age": nil,
			},
			wantErr: false,
		},

		// Empty rules
		{
			name:     "empty rules",
			payload:  []byte(`{"name":"John"}`),
			rules:    []Transformation{},
			expected: map[string]interface{}{},
			wantErr:  false,
		},

		// Multiple transformations
		{
			name:    "multiple transformations",
			payload: []byte(`{"first":"John","last":"Doe","born":"1990"}`),
			rules: []Transformation{
				{Source: "first", Target: "first_name"},
				{Source: "last", Target: "last_name"},
				{Expression: "first + \" \" + last", Target: "full_name"},
				{Source: "born", Target: "birth_year", Type: "int"},
			},
			expected: map[string]interface{}{
				"first_name":  "John",
				"last_name":   "Doe",
				"full_name":   "John Doe",
				"birth_year":  1990,
			},
			wantErr: false,
		},

		// Error: missing target
		{
			name:    "missing target",
			payload: []byte(`{"name":"John"}`),
			rules: []Transformation{
				{Source: "name", Target: "", Value: nil},
			},
			expected: nil,
			wantErr:  true,
		},

		// Error: no source/expression/value
		{
			name:    "no source specified",
			payload: []byte(`{"name":"John"}`),
			rules: []Transformation{
				{Target: "output"},
			},
			expected: nil,
			wantErr:  true,
		},

		// Error: empty payload
		{
			name:    "empty payload",
			payload: []byte(``),
			rules: []Transformation{
				{Source: "name", Target: "output"},
			},
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := re.ExecuteTransformations(tt.payload, tt.rules)

			if (err != nil) != tt.wantErr {
				t.Errorf("ExecuteTransformations() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if !mapsEqual(got, tt.expected) {
					t.Errorf("ExecuteTransformations() got %v, want %v", got, tt.expected)
				}
			}
		})
	}
}

func TestRuleEngine_ConditionalTransformations(t *testing.T) {
	logger := NewTestLogger()
	ctx := context.Background()
	fm := NewFieldMapper(ctx, logger)
	ee := NewExpressionEvaluator(ctx, logger, NewFunctionRegistry(ctx, logger))
	re := NewRuleEngine(ctx, logger, fm, ee, NewFunctionRegistry(ctx, logger))

	payload := []byte(`{
		"order_total": 150,
		"customer_type": "premium",
		"items": 3
	}`)

	tests := []struct {
		name      string
		condition string
		shouldRun bool
	}{
		{"simple equality", `customer_type == "premium"`, true},
		{"greater than", `order_total > 100`, true},
		{"less than", `items < 5`, true},
		{"complex and", `customer_type == "premium" && order_total > 100`, true},
		{"complex or", `customer_type == "gold" || order_total > 100`, true},
		{"false condition", `customer_type == "gold"`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := []Transformation{
				{
					Source:    "order_total",
					Target:    "final_discount",
					Condition: tt.condition,
					Value:     "applied",
				},
			}

			result, err := re.ExecuteTransformations(payload, rules)
			if err != nil {
				t.Fatalf("ExecuteTransformations failed: %v", err)
			}

			if tt.shouldRun {
				if _, exists := result["final_discount"]; !exists {
					t.Errorf("condition should have been true, but transformation was skipped")
				}
			} else {
				if _, exists := result["final_discount"]; exists {
					t.Errorf("condition should have been false, but transformation was applied")
				}
			}
		})
	}
}

func TestRuleEngine_TypeConversion(t *testing.T) {
	logger := NewTestLogger()
	ctx := context.Background()
	fm := NewFieldMapper(ctx, logger)
	ee := NewExpressionEvaluator(ctx, logger, NewFunctionRegistry(ctx, logger))
	re := NewRuleEngine(ctx, logger, fm, ee, NewFunctionRegistry(ctx, logger))

	tests := []struct {
		name     string
		payload  []byte
		rule     Transformation
		expected interface{}
	}{
		{
			"string to int",
			[]byte(`{"value":"42"}`),
			Transformation{Source: "value", Target: "result", Type: "int"},
			42,
		},
		{
			"float to string",
			[]byte(`{"value":3.14}`),
			Transformation{Source: "value", Target: "result", Type: "string"},
			"3.14",
		},
		{
			"bool to string",
			[]byte(`{"value":true}`),
			Transformation{Source: "value", Target: "result", Type: "string"},
			"true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := re.ExecuteTransformations(tt.payload, []Transformation{tt.rule})
			if err != nil {
				t.Fatalf("ExecuteTransformations failed: %v", err)
			}

			if result[tt.rule.Target] != tt.expected {
				t.Errorf("type conversion got %v (type %T), want %v (type %T)",
					result[tt.rule.Target], result[tt.rule.Target], tt.expected, tt.expected)
			}
		})
	}
}

// Helper function to compare maps with interface{} values
func mapsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		bv, exists := b[k]
		if !exists || !valuesEqual(v, bv) {
			return false
		}
	}
	return true
}

// Helper function to compare interface{} values
func valuesEqual(a, b interface{}) bool {
	switch av := a.(type) {
	case float32:
		bv, ok := b.(float32)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case int:
		bv, ok := b.(int)
		return ok && av == bv
	case nil:
		return b == nil
	default:
		return a == b
	}
}
