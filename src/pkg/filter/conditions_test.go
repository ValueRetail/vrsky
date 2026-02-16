package filter

import (
	"testing"
)

func TestConditionEngine_Equal(t *testing.T) {
	tests := []struct {
		name      string
		field     interface{}
		value     interface{}
		want      bool
		wantError bool
	}{
		{"equal numbers", 5, 5, true, false},
		{"different numbers", 5, 6, false, false},
		{"equal strings", "hello", "hello", true, false},
		{"different strings", "hello", "world", false, false},
		{"numeric string comparison", "5", 5, true, false},
		{"both nil", nil, nil, true, false},
		{"nil vs value", nil, 5, false, false},
	}

	ce := NewConditionEngine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ce.opEqual(tt.field, tt.value)
			if (err != nil) != tt.wantError {
				t.Errorf("opEqual error = %v, wantError %v", err, tt.wantError)
				return
			}
			if got != tt.want {
				t.Errorf("opEqual got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionEngine_Comparison(t *testing.T) {
	tests := []struct {
		name     string
		field    interface{}
		value    interface{}
		operator string
		want     bool
	}{
		{"greater than", 10, 5, ">", true},
		{"less than", 5, 10, "<", true},
		{"greater or equal", 10, 10, ">=", true},
		{"less or equal", 10, 10, "<=", true},
		{"not equal", 5, 10, "!=", true},
	}

	ce := NewConditionEngine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			var err error

			switch tt.operator {
			case ">":
				got, err = ce.opGreaterThan(tt.field, tt.value)
			case "<":
				got, err = ce.opLessThan(tt.field, tt.value)
			case ">=":
				got, err = ce.opGreaterThanOrEqual(tt.field, tt.value)
			case "<=":
				got, err = ce.opLessThanOrEqual(tt.field, tt.value)
			case "!=":
				got, err = ce.opNotEqual(tt.field, tt.value)
			}

			if err != nil {
				t.Errorf("operator %s error = %v", tt.operator, err)
				return
			}
			if got != tt.want {
				t.Errorf("operator %s got = %v, want %v", tt.operator, got, tt.want)
			}
		})
	}
}

func TestConditionEngine_StringOperations(t *testing.T) {
	tests := []struct {
		name     string
		field    interface{}
		value    interface{}
		operator string
		want     bool
	}{
		{"contains", "hello world", "world", "contains", true},
		{"contains missing", "hello", "xyz", "contains", false},
		{"startswith", "hello world", "hello", "startswith", true},
		{"startswith false", "hello world", "world", "startswith", false},
		{"endswith", "hello world", "world", "endswith", true},
		{"endswith false", "hello world", "hello", "endswith", false},
	}

	ce := NewConditionEngine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			var err error

			switch tt.operator {
			case "contains":
				got, err = ce.opContains(tt.field, tt.value)
			case "startswith":
				got, err = ce.opStartsWith(tt.field, tt.value)
			case "endswith":
				got, err = ce.opEndsWith(tt.field, tt.value)
			}

			if err != nil {
				t.Errorf("operator %s error = %v", tt.operator, err)
				return
			}
			if got != tt.want {
				t.Errorf("operator %s got = %v, want %v", tt.operator, got, tt.want)
			}
		})
	}
}

func TestConditionEngine_RegexMatch(t *testing.T) {
	tests := []struct {
		name    string
		field   interface{}
		pattern interface{}
		want    bool
		wantErr bool
	}{
		{"match digit", "123", `^\d+$`, true, false},
		{"match email", "test@example.com", `^[a-z0-9]+@[a-z]+\.[a-z]+$`, true, false},
		{"no match", "abc", `^\d+$`, false, false},
		{"invalid regex", "test", "[", false, true},
	}

	ce := NewConditionEngine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ce.opRegexMatch(tt.field, tt.pattern)
			if (err != nil) != tt.wantErr {
				t.Errorf("opRegexMatch error = %v, wantError %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("opRegexMatch got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionEngine_InList(t *testing.T) {
	tests := []struct {
		name    string
		field   interface{}
		list    interface{}
		want    bool
		wantErr bool
	}{
		{"in list", "apple", []interface{}{"apple", "banana", "cherry"}, true, false},
		{"not in list", "grape", []interface{}{"apple", "banana"}, false, false},
		{"in string list", "apple", []string{"apple", "banana"}, true, false},
	}

	ce := NewConditionEngine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ce.opInList(tt.field, tt.list)
			if (err != nil) != tt.wantErr {
				t.Errorf("opInList error = %v, wantError %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("opInList got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionEngine_Evaluate(t *testing.T) {
	tests := []struct {
		name      string
		condition *Condition
		payload   interface{}
		want      bool
		wantErr   bool
	}{
		{
			"simple equal",
			&Condition{Operator: "==", Field: "status", Value: "active"},
			map[string]interface{}{"status": "active"},
			true,
			false,
		},
		{
			"nested field",
			&Condition{Operator: ">", Field: "user.age", Value: 18},
			map[string]interface{}{
				"user": map[string]interface{}{
					"age": 25,
				},
			},
			true,
			false,
		},
		{
			"missing field",
			&Condition{Operator: "==", Field: "missing", Value: "value"},
			map[string]interface{}{"status": "active"},
			false,
			false,
		},
		{
			"always operator",
			&Condition{Operator: "always"},
			map[string]interface{}{},
			true,
			false,
		},
	}

	ce := NewConditionEngine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ce.Evaluate(tt.condition, tt.payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate error = %v, wantError %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Evaluate got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionEngine_GetFieldValue(t *testing.T) {
	tests := []struct {
		name    string
		payload interface{}
		path    string
		want    interface{}
		wantErr bool
	}{
		{
			"simple field",
			map[string]interface{}{"name": "John"},
			"name",
			"John",
			false,
		},
		{
			"nested field",
			map[string]interface{}{
				"user": map[string]interface{}{
					"name": "John",
				},
			},
			"user.name",
			"John",
			false,
		},
		{
			"missing field returns nil",
			map[string]interface{}{"name": "John"},
			"age",
			nil,
			false,
		},
		{
			"empty path returns payload",
			map[string]interface{}{"name": "John"},
			"",
			map[string]interface{}{"name": "John"},
			false,
		},
	}

	ce := NewConditionEngine()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ce.getFieldValue(tt.payload, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("getFieldValue error = %v, wantError %v", err, tt.wantErr)
				return
			}

			// Special handling for maps (can't compare directly)
			if gotMap, ok := got.(map[string]interface{}); ok {
				if wantMap, ok := tt.want.(map[string]interface{}); ok {
					if len(gotMap) != len(wantMap) {
						t.Errorf("getFieldValue map length mismatch: got %d, want %d", len(gotMap), len(wantMap))
						return
					}
					for k, v := range wantMap {
						if gotMap[k] != v {
							t.Errorf("getFieldValue map[%s] = %v, want %v", k, gotMap[k], v)
						}
					}
					return
				}
			}

			if got != tt.want {
				t.Errorf("getFieldValue got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		want    float64
		wantErr bool
	}{
		{"float64", 5.5, 5.5, false},
		{"int", 5, 5.0, false},
		{"string number", "5.5", 5.5, false},
		{"invalid string", "abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toFloat64(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("toFloat64 error = %v, wantError %v", err, tt.wantErr)
				return
			}
			if err == nil && got != tt.want {
				t.Errorf("toFloat64 got = %v, want %v", got, tt.want)
			}
		})
	}
}
