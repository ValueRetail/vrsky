package filter

import (
	"testing"
)

func TestSchemaValidator_RegisterAndValidate(t *testing.T) {
	sv, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("NewSchemaValidator error = %v", err)
	}

	// Register a simple schema
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age": {"type": "integer"}
		},
		"required": ["name"]
	}`)

	err = sv.RegisterSchema("person", schemaData)
	if err != nil {
		t.Fatalf("RegisterSchema error = %v", err)
	}

	tests := []struct {
		name    string
		data    interface{}
		wantErr bool
	}{
		{
			"valid data",
			map[string]interface{}{
				"name": "John",
				"age":  30,
			},
			false,
		},
		{
			"missing required field",
			map[string]interface{}{
				"age": 30,
			},
			true,
		},
		{
			"wrong type",
			map[string]interface{}{
				"name": "John",
				"age":  "thirty", // Should be integer
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sv.Validate("person", tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSchemaValidator_StrictMode(t *testing.T) {
	sv, err := NewSchemaValidatorWithMode(ModeStrict, nil)
	if err != nil {
		t.Fatalf("NewSchemaValidatorWithMode error = %v", err)
	}

	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"status": {"type": "string"}
		}
	}`)

	_ = sv.RegisterSchema("test", schemaData)

	// Valid data should pass
	err = sv.Validate("test", map[string]interface{}{"status": "active"})
	if err != nil {
		t.Errorf("Strict mode: valid data should not error = %v", err)
	}

	// Invalid data should fail
	err = sv.Validate("test", map[string]interface{}{"status": 123})
	if err == nil {
		t.Errorf("Strict mode: invalid data should error")
	}
}

func TestSchemaValidator_LenientMode(t *testing.T) {
	sv, err := NewSchemaValidatorWithMode(ModeLenient, nil)
	if err != nil {
		t.Fatalf("NewSchemaValidatorWithMode error = %v", err)
	}

	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"status": {"type": "string"}
		}
	}`)

	_ = sv.RegisterSchema("test", schemaData)

	// Invalid data should NOT fail in lenient mode
	err = sv.Validate("test", map[string]interface{}{"status": 123})
	if err != nil {
		t.Errorf("Lenient mode: invalid data should not error = %v", err)
	}
}

func TestSchemaValidator_ValidateStrict(t *testing.T) {
	sv, _ := NewSchemaValidator()

	schemaStr := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		},
		"required": ["name"]
	}`

	tests := []struct {
		name    string
		data    interface{}
		wantErr bool
	}{
		{
			"valid",
			map[string]interface{}{"name": "John"},
			false,
		},
		{
			"invalid",
			map[string]interface{}{},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sv.ValidateStrict(schemaStr, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateStrict error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSchemaValidator_NotFound(t *testing.T) {
	sv, _ := NewSchemaValidator()

	err := sv.Validate("nonexistent", map[string]interface{}{})
	if err == nil {
		t.Errorf("Validate should error for nonexistent schema")
	}
}
