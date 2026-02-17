package filter

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func TestTransformationEngine_AddField(t *testing.T) {
	tests := []struct {
		name     string
		trans    *Transformation
		metadata map[string]interface{}
		wantErr  bool
		check    func(t *testing.T, metadata map[string]interface{})
	}{
		{
			name: "add_simple_field",
			trans: &Transformation{
				Action: "add_field",
				Field:  "routing_priority",
				Value:  "high",
			},
			metadata: map[string]interface{}{},
			wantErr:  false,
			check: func(t *testing.T, m map[string]interface{}) {
				if m["routing_priority"] != "high" {
					t.Errorf("Field not added correctly")
				}
			},
		},
		{
			name: "add_field_with_integer",
			trans: &Transformation{
				Action: "add_field",
				Field:  "retry_count",
				Value:  3,
			},
			metadata: map[string]interface{}{},
			wantErr:  false,
			check: func(t *testing.T, m map[string]interface{}) {
				if m["retry_count"] != 3 {
					t.Errorf("Integer field not added correctly")
				}
			},
		},
		{
			name: "add_field_empty_field_name",
			trans: &Transformation{
				Action: "add_field",
				Field:  "",
				Value:  "value",
			},
			metadata: map[string]interface{}{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine := NewTransformationEngine(ce)

			env := &envelope.Envelope{
				ID:       "test-id",
				Payload:  []byte("{}"),
				Metadata: tt.metadata,
			}

			err := engine.ApplyTransformations(env, []*Transformation{tt.trans}, map[string]interface{}{})
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyTransformations error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, env.Metadata)
			}
		})
	}
}

func TestTransformationEngine_RemoveField(t *testing.T) {
	tests := []struct {
		name     string
		trans    *Transformation
		metadata map[string]interface{}
		wantErr  bool
		check    func(t *testing.T, metadata map[string]interface{})
	}{
		{
			name: "remove_existing_field",
			trans: &Transformation{
				Action: "remove_field",
				Field:  "internal_id",
			},
			metadata: map[string]interface{}{"internal_id": "123", "public_id": "456"},
			wantErr:  false,
			check: func(t *testing.T, m map[string]interface{}) {
				if _, exists := m["internal_id"]; exists {
					t.Errorf("Field not removed")
				}
				if m["public_id"] != "456" {
					t.Errorf("Other fields were affected")
				}
			},
		},
		{
			name: "remove_nonexistent_field",
			trans: &Transformation{
				Action: "remove_field",
				Field:  "nonexistent",
			},
			metadata: map[string]interface{}{"field": "value"},
			wantErr:  false,
			check: func(t *testing.T, m map[string]interface{}) {
				if len(m) != 1 {
					t.Errorf("Metadata was affected")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine := NewTransformationEngine(ce)

			env := &envelope.Envelope{
				ID:       "test-id",
				Payload:  []byte("{}"),
				Metadata: tt.metadata,
			}

			err := engine.ApplyTransformations(env, []*Transformation{tt.trans}, map[string]interface{}{})
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyTransformations error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, env.Metadata)
			}
		})
	}
}

func TestTransformationEngine_RenameField(t *testing.T) {
	tests := []struct {
		name     string
		trans    *Transformation
		metadata map[string]interface{}
		wantErr  bool
		check    func(t *testing.T, metadata map[string]interface{})
	}{
		{
			name: "rename_existing_field",
			trans: &Transformation{
				Action: "rename_field",
				Field:  "public_id",
				Source: "internal_id",
			},
			metadata: map[string]interface{}{"internal_id": "123", "other": "value"},
			wantErr:  false,
			check: func(t *testing.T, m map[string]interface{}) {
				if _, exists := m["internal_id"]; exists {
					t.Errorf("Source field not removed")
				}
				if m["public_id"] != "123" {
					t.Errorf("Field not renamed correctly")
				}
			},
		},
		{
			name: "rename_nonexistent_source",
			trans: &Transformation{
				Action: "rename_field",
				Field:  "new_name",
				Source: "nonexistent",
			},
			metadata: map[string]interface{}{"field": "value"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine := NewTransformationEngine(ce)

			env := &envelope.Envelope{
				ID:       "test-id",
				Payload:  []byte("{}"),
				Metadata: tt.metadata,
			}

			err := engine.ApplyTransformations(env, []*Transformation{tt.trans}, map[string]interface{}{})
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyTransformations error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, env.Metadata)
			}
		})
	}
}

func TestTransformationEngine_ExtractField(t *testing.T) {
	tests := []struct {
		name     string
		trans    *Transformation
		payload  interface{}
		metadata map[string]interface{}
		wantErr  bool
		check    func(t *testing.T, metadata map[string]interface{})
	}{
		{
			name: "extract_simple_field",
			trans: &Transformation{
				Action: "extract_field",
				Field:  "customer_id",
				Source: "customer.id",
			},
			payload:  map[string]interface{}{"customer": map[string]interface{}{"id": "cust-123", "name": "John"}},
			metadata: map[string]interface{}{},
			wantErr:  false,
			check: func(t *testing.T, m map[string]interface{}) {
				if m["customer_id"] != "cust-123" {
					t.Errorf("Field not extracted correctly, got %v", m["customer_id"])
				}
			},
		},
		{
			name: "extract_missing_field",
			trans: &Transformation{
				Action: "extract_field",
				Field:  "value",
				Source: "nonexistent.field",
			},
			payload:  map[string]interface{}{"other": "data"},
			metadata: map[string]interface{}{},
			wantErr:  false,
			check: func(t *testing.T, m map[string]interface{}) {
				if m["value"] != nil {
					t.Errorf("Should extract nil for missing field")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine := NewTransformationEngine(ce)

			env := &envelope.Envelope{
				ID:       "test-id",
				Payload:  []byte("{}"),
				Metadata: tt.metadata,
			}

			err := engine.ApplyTransformations(env, []*Transformation{tt.trans}, tt.payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyTransformations error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, env.Metadata)
			}
		})
	}
}

func TestTemplateEngine_ResolveExpressions(t *testing.T) {
	tests := []struct {
		name     string
		template string
		wantErr  bool
		check    func(t *testing.T, result interface{})
	}{
		{
			name:     "no_template",
			template: "plain_string",
			wantErr:  false,
			check: func(t *testing.T, result interface{}) {
				if result != "plain_string" {
					t.Errorf("Plain string changed")
				}
			},
		},
		{
			name:     "uuid_function",
			template: "${uuid()}",
			wantErr:  false,
			check: func(t *testing.T, result interface{}) {
				s, ok := result.(string)
				if !ok {
					t.Errorf("UUID result not a string")
					return
				}
				if len(s) != 36 { // Standard UUID length
					t.Errorf("UUID format incorrect: %s", s)
				}
			},
		},
		{
			name:     "now_function",
			template: "${now()}",
			wantErr:  false,
			check: func(t *testing.T, result interface{}) {
				s, ok := result.(string)
				if !ok {
					t.Errorf("Now result not a string")
					return
				}
				if len(s) < 20 { // Rough RFC3339Nano length check
					t.Errorf("Now timestamp too short: %s", s)
				}
			},
		},
		{
			name:     "mixed_template",
			template: "trace_${uuid()}",
			wantErr:  false,
			check: func(t *testing.T, result interface{}) {
				s, ok := result.(string)
				if !ok {
					t.Errorf("Result not a string")
					return
				}
				if !strings.HasPrefix(s, "trace_") {
					t.Errorf("Prefix not preserved: %s", s)
				}
			},
		},
		{
			name:     "env_variable",
			template: "${env:TEST_VAR}",
			wantErr:  false,
			check: func(t *testing.T, result interface{}) {
				if result != "test_value" {
					t.Errorf("Environment variable not resolved: %v", result)
				}
			},
		},
		{
			name:     "missing_env_variable",
			template: "${env:NONEXISTENT_VAR_XYZ}",
			wantErr:  true,
		},
		{
			name:     "random_range",
			template: "${random:1:10}",
			wantErr:  false,
			check: func(t *testing.T, result interface{}) {
				s, ok := result.(string)
				if !ok {
					t.Errorf("Result not a string")
					return
				}
				// Should be a number between 1 and 10
				var num int
				_, err := fmt.Sscanf(s, "%d", &num)
				if err != nil || num < 1 || num > 10 {
					t.Errorf("Random value out of range: %s (parsed as %d)", s, num)
				}
			},
		},
	}

	// Set test environment variable
	os.Setenv("TEST_VAR", "test_value")
	defer os.Unsetenv("TEST_VAR")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewTemplateEngine()
			result, err := engine.Resolve(tt.template)
			if (err != nil) != tt.wantErr {
				t.Errorf("Resolve error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, result)
			}
		})
	}
}

func TestTemplateEngine_InvalidExpressions(t *testing.T) {
	tests := []struct {
		name     string
		template string
	}{
		{
			name:     "unknown_function",
			template: "${unknown_func()}",
		},
		{
			name:     "invalid_random_format",
			template: "${random:invalid}",
		},
		{
			name:     "random_min_greater_than_max",
			template: "${random:10:1}",
		},
		{
			name:     "field_reference_not_supported",
			template: "${field_name}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewTemplateEngine()
			_, err := engine.Resolve(tt.template)
			if err == nil {
				t.Errorf("Expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestTransformationEngine_SetField(t *testing.T) {
	tests := []struct {
		name     string
		trans    *Transformation
		metadata map[string]interface{}
		check    func(t *testing.T, metadata map[string]interface{})
	}{
		{
			name: "set_field_overwrites",
			trans: &Transformation{
				Action: "set_field",
				Field:  "priority",
				Value:  "urgent",
			},
			metadata: map[string]interface{}{"priority": "normal"},
			check: func(t *testing.T, m map[string]interface{}) {
				if m["priority"] != "urgent" {
					t.Errorf("Field not set correctly")
				}
			},
		},
		{
			name: "set_field_with_complex_value",
			trans: &Transformation{
				Action: "set_field",
				Field:  "config",
				Value:  map[string]interface{}{"retry": 3, "timeout": 30},
			},
			metadata: map[string]interface{}{},
			check: func(t *testing.T, m map[string]interface{}) {
				if config, ok := m["config"].(map[string]interface{}); !ok || config["retry"] != 3 {
					t.Errorf("Complex value not set correctly")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine := NewTransformationEngine(ce)

			env := &envelope.Envelope{
				ID:       "test-id",
				Payload:  []byte("{}"),
				Metadata: tt.metadata,
			}

			err := engine.ApplyTransformations(env, []*Transformation{tt.trans}, map[string]interface{}{})
			if err != nil {
				t.Errorf("ApplyTransformations error = %v", err)
				return
			}

			if tt.check != nil {
				tt.check(t, env.Metadata)
			}
		})
	}
}

func TestTransformationEngine_MultipleTransformations(t *testing.T) {
	ce := NewConditionEngine()
	engine := NewTransformationEngine(ce)

	env := &envelope.Envelope{
		ID:       "test-id",
		Payload:  []byte("{}"),
		Metadata: map[string]interface{}{"existing": "value"},
	}

	transformations := []*Transformation{
		{
			Action: "add_field",
			Field:  "step",
			Value:  "routed",
		},
		{
			Action: "add_field",
			Field:  "trace_id",
			Value:  "${uuid()}",
		},
		{
			Action: "remove_field",
			Field:  "existing",
		},
	}

	err := engine.ApplyTransformations(env, transformations, map[string]interface{}{})
	if err != nil {
		t.Fatalf("ApplyTransformations error = %v", err)
	}

	if env.Metadata["step"] != "routed" {
		t.Errorf("Field 'step' not added")
	}

	if _, exists := env.Metadata["trace_id"]; !exists {
		t.Errorf("Field 'trace_id' not added")
	}

	if _, exists := env.Metadata["existing"]; exists {
		t.Errorf("Field 'existing' not removed")
	}
}

func TestTransformationEngine_EnrichFromConfig(t *testing.T) {
	tests := []struct {
		name     string
		trans    *Transformation
		metadata map[string]interface{}
		wantErr  bool
		check    func(t *testing.T, metadata map[string]interface{})
	}{
		{
			name: "enrich_from_config_merge",
			trans: &Transformation{
				Action: "enrich_from_config",
				Field:  "enrichment",
				Value: map[string]interface{}{
					"service": "order_processor",
					"version": "1.0.0",
					"env":     "production",
				},
			},
			metadata: map[string]interface{}{"existing": "data"},
			wantErr:  false,
			check: func(t *testing.T, m map[string]interface{}) {
				if m["service"] != "order_processor" {
					t.Errorf("Service not enriched")
				}
				if m["version"] != "1.0.0" {
					t.Errorf("Version not enriched")
				}
				if m["env"] != "production" {
					t.Errorf("Env not enriched")
				}
				if m["existing"] != "data" {
					t.Errorf("Existing data lost")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine := NewTransformationEngine(ce)

			env := &envelope.Envelope{
				ID:       "test-id",
				Payload:  []byte("{}"),
				Metadata: tt.metadata,
			}

			err := engine.ApplyTransformations(env, []*Transformation{tt.trans}, map[string]interface{}{})
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyTransformations error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, env.Metadata)
			}
		})
	}
}
