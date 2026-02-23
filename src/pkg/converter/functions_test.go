package converter

import (
	"context"
	"math"
	"testing"
)

// =============================================================================
// AGGREGATION FUNCTION TESTS
// =============================================================================

func TestSumFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected float64
		wantErr  bool
	}{
		// Normal cases
		{"sum integers", []interface{}{[]interface{}{1, 2, 3}}, 6, false},
		{"sum floats", []interface{}{[]interface{}{1.5, 2.5, 3.0}}, 7, false},
		{"sum mixed int/float", []interface{}{[]interface{}{1, 2.5, 3}}, 6.5, false},

		// Type coercion
		{"sum strings", []interface{}{[]interface{}{"1", "2", "3"}}, 6, false},
		{"sum mixed types", []interface{}{[]interface{}{1, "2", 3.5}}, 6.5, false},

		// Nil handling
		{"sum with nil values", []interface{}{[]interface{}{1, nil, 2}}, 3, false},
		{"sum all nil", []interface{}{[]interface{}{nil, nil}}, 0, false},

		// Edge cases
		{"sum empty array", []interface{}{[]interface{}{}}, 0, false},
		{"sum single value", []interface{}{[]interface{}{42}}, 42, false},
		{"sum negative values", []interface{}{[]interface{}{-1, -2, 3}}, 0, false},

		// Error cases
		{"sum no args", []interface{}{}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := sumFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestAvgFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected float64
		wantErr  bool
	}{
		{"avg integers", []interface{}{[]interface{}{1, 2, 3}}, 2, false},
		{"avg floats", []interface{}{[]interface{}{1.5, 2.5, 3.5}}, 2.5, false},
		{"avg strings", []interface{}{[]interface{}{"10", "20", "30"}}, 20, false},
		{"avg with nil", []interface{}{[]interface{}{10, nil, 20}}, 15, false},
		{"avg single value", []interface{}{[]interface{}{42}}, 42, false},
		{"avg empty", []interface{}{[]interface{}{}}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := avgFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr {
				if diff := math.Abs(result.(float64) - tt.expected); diff > 0.0001 {
					t.Errorf("got %v, want %v", result, tt.expected)
				}
			}
		})
	}
}

func TestCountFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected float64
		wantErr  bool
	}{
		{"count integers", []interface{}{[]interface{}{1, 2, 3}}, 3, false},
		{"count with nil", []interface{}{[]interface{}{1, nil, 2, nil, 3}}, 3, false},
		{"count all nil", []interface{}{[]interface{}{nil, nil}}, 0, false},
		{"count empty", []interface{}{[]interface{}{}}, 0, false},
		{"count single", []interface{}{[]interface{}{42}}, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := countFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMaxFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected float64
		wantErr  bool
	}{
		{"max integers", []interface{}{[]interface{}{1, 2, 3}}, 3, false},
		{"max floats", []interface{}{[]interface{}{1.5, 2.5, 3.5}}, 3.5, false},
		{"max mixed", []interface{}{[]interface{}{1, "20", 3.5}}, 20, false},
		{"max with nil", []interface{}{[]interface{}{1, nil, 2}}, 2, false},
		{"max negative", []interface{}{[]interface{}{-1, -2, -3}}, -1, false},
		{"max single", []interface{}{[]interface{}{42}}, 42, false},
		{"max empty", []interface{}{[]interface{}{}}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := maxFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMinFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected float64
		wantErr  bool
	}{
		{"min integers", []interface{}{[]interface{}{1, 2, 3}}, 1, false},
		{"min floats", []interface{}{[]interface{}{1.5, 2.5, 3.5}}, 1.5, false},
		{"min mixed", []interface{}{[]interface{}{10, "5", 3.5}}, 3.5, false},
		{"min with nil", []interface{}{[]interface{}{10, nil, 5}}, 5, false},
		{"min negative", []interface{}{[]interface{}{-1, -2, -3}}, -3, false},
		{"min single", []interface{}{[]interface{}{42}}, 42, false},
		{"min empty", []interface{}{[]interface{}{}}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := minFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// =============================================================================
// STRING FUNCTION TESTS
// =============================================================================

func TestConcatFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected interface{}
		wantErr  bool
	}{
		{"concat strings", []interface{}{"hello", " ", "world"}, "hello world", false},
		{"concat with int", []interface{}{"id:", 123}, "id:123", false},
		{"concat single", []interface{}{"hello"}, "hello", false},
		{"concat nil", []interface{}{nil}, nil, false},
		{"concat no args", []interface{}{}, "", true},
		{"concat mixed types", []interface{}{"a", 1, "b", 2.5}, "a1b2.5", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := concatFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestUppercaseFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected interface{}
		wantErr  bool
	}{
		{"uppercase string", []interface{}{"hello"}, "HELLO", false},
		{"uppercase mixed case", []interface{}{"HeLLo"}, "HELLO", false},
		{"uppercase number", []interface{}{123}, "123", false},
		{"uppercase nil", []interface{}{nil}, nil, false},
		{"uppercase empty string", []interface{}{""}, "", false},
		{"uppercase no args", []interface{}{}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := uppercaseFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestLowercaseFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected interface{}
		wantErr  bool
	}{
		{"lowercase string", []interface{}{"HELLO"}, "hello", false},
		{"lowercase mixed case", []interface{}{"HeLLo"}, "hello", false},
		{"lowercase number", []interface{}{123}, "123", false},
		{"lowercase nil", []interface{}{nil}, nil, false},
		{"lowercase empty string", []interface{}{""}, "", false},
		{"lowercase no args", []interface{}{}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := lowercaseFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestTrimFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected interface{}
		wantErr  bool
	}{
		{"trim whitespace", []interface{}{"  hello  "}, "hello", false},
		{"trim tabs/newlines", []interface{}{"\t\nhello\n\t"}, "hello", false},
		{"trim no whitespace", []interface{}{"hello"}, "hello", false},
		{"trim custom chars", []interface{}{"xxxhelloyyy", "xy"}, "hello", false},
		{"trim nil", []interface{}{nil}, nil, false},
		{"trim empty string", []interface{}{""}, "", false},
		{"trim no args", []interface{}{}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := trimFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestSplitFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected []interface{}
		wantErr  bool
	}{
		{"split by comma", []interface{}{"a,b,c", ","}, []interface{}{"a", "b", "c"}, false},
		{"split by space", []interface{}{"hello world foo", " "}, []interface{}{"hello", "world", "foo"}, false},
		{"split no separator match", []interface{}{"hello", ","}, []interface{}{"hello"}, false},
		{"split nil", []interface{}{nil, ","}, nil, false},
		{"split no args", []interface{}{}, nil, true},
		{"split one arg", []interface{}{"hello"}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := splitFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr {
				if result == nil && tt.expected == nil {
					// OK
				} else if result == nil || tt.expected == nil {
					t.Errorf("got %v, want %v", result, tt.expected)
				} else {
					resultArr := result.([]interface{})
					if len(resultArr) != len(tt.expected) {
						t.Errorf("got %v, want %v", result, tt.expected)
					} else {
						for i, v := range resultArr {
							if v != tt.expected[i] {
								t.Errorf("got %v, want %v", result, tt.expected)
								break
							}
						}
					}
				}
			}
		})
	}
}

func TestReplaceFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected interface{}
		wantErr  bool
	}{
		{"replace all", []interface{}{"hello", "l", "L"}, "heLLo", false},
		{"replace none", []interface{}{"hello", "x", "y"}, "hello", false},
		{"replace empty target", []interface{}{"hello", "", "x"}, "xhxexlxlxox", false},
		{"replace nil", []interface{}{nil, "a", "b"}, nil, false},
		{"replace no args", []interface{}{}, "", true},
		{"replace one arg", []interface{}{"hello"}, "", true},
		{"replace two args", []interface{}{"hello", "l"}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := replaceFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// =============================================================================
// MATH FUNCTION TESTS
// =============================================================================

func TestMultiplyFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected float64
		wantErr  bool
	}{
		{"multiply integers", []interface{}{3, 4}, 12, false},
		{"multiply floats", []interface{}{2.5, 4.0}, 10, false},
		{"multiply strings", []interface{}{"3", "4"}, 12, false},
		{"multiply mixed", []interface{}{3, "4"}, 12, false},
		{"multiply by zero", []interface{}{5, 0}, 0, false},
		{"multiply negative", []interface{}{-3, 4}, -12, false},
		{"multiply no args", []interface{}{}, 0, true},
		{"multiply one arg", []interface{}{3}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := multiplyFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestDivideFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected float64
		wantErr  bool
	}{
		{"divide integers", []interface{}{10, 2}, 5, false},
		{"divide floats", []interface{}{10.0, 2.5}, 4, false},
		{"divide strings", []interface{}{"10", "2"}, 5, false},
		{"divide by zero", []interface{}{10, 0}, 0, false}, // Graceful degradation
		{"divide negative", []interface{}{-10, 2}, -5, false},
		{"divide no args", []interface{}{}, 0, true},
		{"divide one arg", []interface{}{10}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := divideFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// =============================================================================
// TYPE CONVERSION FUNCTION TESTS
// =============================================================================

func TestAsStringFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected interface{}
		wantErr  bool
	}{
		{"string to string", []interface{}{"hello"}, "hello", false},
		{"int to string", []interface{}{123}, "123", false},
		{"float to string", []interface{}{3.14}, "3.14", false},
		{"bool to string", []interface{}{true}, "true", false},
		{"nil to string", []interface{}{nil}, nil, false},
		{"no args", []interface{}{}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := asStringFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestAsNumberFunc(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []interface{}
		expected interface{}
		wantErr  bool
	}{
		{"string to number", []interface{}{"123"}, 123.0, false},
		{"string float to number", []interface{}{"123.5"}, 123.5, false},
		{"int to number", []interface{}{123}, 123.0, false},
		{"float to number", []interface{}{123.5}, 123.5, false},
		{"bool to number", []interface{}{true}, 1.0, false},
		{"non-convertible string", []interface{}{"abc"}, nil, false}, // Graceful
		{"nil to number", []interface{}{nil}, nil, false},
		{"no args", []interface{}{}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := asNumberFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// =============================================================================
// DATE/TIME FUNCTION TESTS
// =============================================================================

func TestNowFunc(t *testing.T) {
	ctx := context.Background()

	result, err := nowFunc(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Check that result is a valid RFC3339 string
	ts := result.(string)
	if _, err := parseTime(ts); err != nil {
		t.Errorf("result is not valid RFC3339: %s", ts)
	}
}

func TestDateFormatFunc(t *testing.T) {
	ctx := context.Background()
	baseTime := "2025-02-23T10:30:00Z"

	tests := []struct {
		name    string
		args    []interface{}
		checkFn func(interface{}) bool
		wantErr bool
	}{
		{"format as date", []interface{}{baseTime, "date"}, func(v interface{}) bool {
			return v.(string) == "2025-02-23"
		}, false},
		{"format as datetime", []interface{}{baseTime, "datetime"}, func(v interface{}) bool {
			_, err := parseTime(v.(string))
			return err == nil
		}, false},
		{"format with nil", []interface{}{nil, "date"}, func(v interface{}) bool {
			return v == nil
		}, false},
		{"format no args", []interface{}{}, nil, true},
		{"format one arg", []interface{}{baseTime}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := dateFormatFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && !tt.checkFn(result) {
				t.Errorf("result check failed: %v", result)
			}
		})
	}
}

func TestDateAddFunc(t *testing.T) {
	ctx := context.Background()
	baseTime := "2025-02-23T00:00:00Z"

	tests := []struct {
		name    string
		args    []interface{}
		checkFn func(interface{}) bool
		wantErr bool
	}{
		{"add days", []interface{}{baseTime, 30}, func(v interface{}) bool {
			ts := v.(string)
			t, _ := parseTime(ts)
			// Check it's roughly 30 days later
			return t.Day() == 25 || t.Day() == 24 // March 25 or March 24 depending on timezone
		}, false},
		{"add negative days", []interface{}{baseTime, -7}, func(v interface{}) bool {
			ts := v.(string)
			_, err := parseTime(ts)
			return err == nil
		}, false},
		{"add nil", []interface{}{nil, 30}, func(v interface{}) bool {
			return v == nil
		}, false},
		{"add no args", []interface{}{}, nil, true},
		{"add one arg", []interface{}{baseTime}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := dateAddFunc(ctx, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && tt.checkFn != nil && !tt.checkFn(result) {
				t.Errorf("result check failed: %v", result)
			}
		})
	}
}

// =============================================================================
// HELPER FUNCTION TESTS
// =============================================================================

func TestToFloat64(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		input    interface{}
		expected float64
		wantErr  bool
	}{
		{"float64", 3.14, 3.14, false},
		{"int", 42, 42, false},
		{"string", "123.45", 123.45, false},
		{"bool true", true, 1, false},
		{"bool false", false, 0, false},
		{"nil", nil, 0, true},
		{"invalid string", "abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toFloat64(tt.input, ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestToString(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{"string", "hello", "hello"},
		{"int", 42, "42"},
		{"float", 3.14, "3.14"},
		{"bool", true, "true"},
		{"nil", nil, ""},
		{"float whole number", 42.0, "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toString(tt.input)
			if result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestFilterNumerics(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		input    interface{}
		expected []float64
	}{
		{"all numbers", []interface{}{1, 2, 3}, []float64{1, 2, 3}},
		{"mixed types", []interface{}{1, "2", 3}, []float64{1, 2, 3}},
		{"with nil", []interface{}{1, nil, 2}, []float64{1, 2}},
		{"empty", []interface{}{}, []float64{}},
		{"non-array", "not an array", []float64{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterNumerics(tt.input, ctx)
			if len(result) != len(tt.expected) {
				t.Errorf("got length %d, want %d", len(result), len(tt.expected))
			} else {
				for i, v := range result {
					if v != tt.expected[i] {
						t.Errorf("got %v, want %v", result, tt.expected)
						break
					}
				}
			}
		})
	}
}
