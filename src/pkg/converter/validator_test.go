package converter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSchemaValidator(t *testing.T) {
	sv := NewSchemaValidator()
	assert.NotNil(t, sv)
	assert.NotNil(t, sv.schemas)
	assert.NotNil(t, sv.cache)
}

func TestRegisterSchema_ValidSchema(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"name": {"type": "string"},
			"age": {"type": "integer"}
		},
		"required": ["id", "name"]
	}`)

	err := sv.RegisterSchema("tenant1", "user_schema", schemaData)
	require.NoError(t, err)

	// Verify schema is registered
	schema, err := sv.GetSchema("tenant1", "user_schema")
	require.NoError(t, err)
	assert.NotNil(t, schema)
}

func TestRegisterSchema_InvalidJSON(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`invalid json`)

	err := sv.RegisterSchema("tenant1", "bad_schema", schemaData)
	assert.Error(t, err)
	// Error could come from either AddResource or Compile
	assert.True(t, strings.Contains(err.Error(), "schema") || strings.Contains(err.Error(), "json"))
}

func TestRegisterSchema_EmptyTenantID(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{"type": "object"}`)

	err := sv.RegisterSchema("", "schema_name", schemaData)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestRegisterSchema_EmptySchemaName(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{"type": "object"}`)

	err := sv.RegisterSchema("tenant1", "", schemaData)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestRegisterSchema_EmptySchemaData(t *testing.T) {
	sv := NewSchemaValidator()

	err := sv.RegisterSchema("tenant1", "schema_name", []byte{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be empty")
}

func TestRegisterSchema_MultiTenantIsolation(t *testing.T) {
	sv := NewSchemaValidator()
	schema1 := []byte(`{"type": "object", "properties": {"id": {"type": "string"}}}`)
	schema2 := []byte(`{"type": "object", "properties": {"id": {"type": "integer"}}}`)

	err := sv.RegisterSchema("tenant1", "id_schema", schema1)
	require.NoError(t, err)

	err = sv.RegisterSchema("tenant2", "id_schema", schema2)
	require.NoError(t, err)

	// Verify both schemas are registered separately
	schema, err := sv.GetSchema("tenant1", "id_schema")
	require.NoError(t, err)
	assert.NotNil(t, schema)

	schema, err = sv.GetSchema("tenant2", "id_schema")
	require.NoError(t, err)
	assert.NotNil(t, schema)
}

func TestValidateInput_ValidData(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"name": {"type": "string"}
		},
		"required": ["id", "name"]
	}`)

	err := sv.RegisterSchema("tenant1", "user", schemaData)
	require.NoError(t, err)

	data := map[string]interface{}{
		"id":   "123",
		"name": "John",
	}

	err = sv.ValidateInput("tenant1", "user", data)
	assert.NoError(t, err)
}

func TestValidateInput_MissingRequiredField(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"name": {"type": "string"}
		},
		"required": ["id", "name"]
	}`)

	err := sv.RegisterSchema("tenant1", "user", schemaData)
	require.NoError(t, err)

	data := map[string]interface{}{
		"id": "123",
	}

	err = sv.ValidateInput("tenant1", "user", data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}

func TestValidateInput_TypeMismatch(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"age": {"type": "integer"}
		}
	}`)

	err := sv.RegisterSchema("tenant1", "user", schemaData)
	require.NoError(t, err)

	data := map[string]interface{}{
		"id":  "123",
		"age": "not a number",
	}

	err = sv.ValidateInput("tenant1", "user", data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}

func TestValidateInput_SchemaNotFound(t *testing.T) {
	sv := NewSchemaValidator()

	data := map[string]interface{}{"id": "123"}

	err := sv.ValidateInput("tenant1", "nonexistent", data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestValidateInput_EmptyTenantID(t *testing.T) {
	sv := NewSchemaValidator()
	data := map[string]interface{}{"id": "123"}

	err := sv.ValidateInput("", "schema", data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestValidateOutput_ValidData(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"status": {"type": "string"}
		},
		"required": ["id", "status"]
	}`)

	err := sv.RegisterSchema("tenant1", "order_output", schemaData)
	require.NoError(t, err)

	data := map[string]interface{}{
		"id":     "order_123",
		"status": "completed",
	}

	err = sv.ValidateOutput("tenant1", "order_output", data)
	assert.NoError(t, err)
}

func TestValidateOutput_InvalidData(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"status": {"type": "string"}
		},
		"required": ["id", "status"]
	}`)

	err := sv.RegisterSchema("tenant1", "order_output", schemaData)
	require.NoError(t, err)

	data := map[string]interface{}{
		"id": "order_123",
	}

	err = sv.ValidateOutput("tenant1", "order_output", data)
	assert.Error(t, err)
}

func TestGetSchema_Exists(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{"type": "object"}`)

	err := sv.RegisterSchema("tenant1", "test_schema", schemaData)
	require.NoError(t, err)

	schema, err := sv.GetSchema("tenant1", "test_schema")
	require.NoError(t, err)
	assert.NotNil(t, schema)
}

func TestGetSchema_NotFound(t *testing.T) {
	sv := NewSchemaValidator()

	schema, err := sv.GetSchema("tenant1", "nonexistent")
	assert.Error(t, err)
	assert.Nil(t, schema)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetSchema_EmptyTenantID(t *testing.T) {
	sv := NewSchemaValidator()

	schema, err := sv.GetSchema("", "schema_name")
	assert.Error(t, err)
	assert.Nil(t, schema)
	assert.Contains(t, err.Error(), "required")
}

func TestClearSchema_Exists(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{"type": "object"}`)

	err := sv.RegisterSchema("tenant1", "test_schema", schemaData)
	require.NoError(t, err)

	// Verify it exists
	_, err = sv.GetSchema("tenant1", "test_schema")
	require.NoError(t, err)

	// Clear it
	err = sv.ClearSchema("tenant1", "test_schema")
	require.NoError(t, err)

	// Verify it's gone
	_, err = sv.GetSchema("tenant1", "test_schema")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestClearSchema_NotFound(t *testing.T) {
	sv := NewSchemaValidator()

	err := sv.ClearSchema("tenant1", "nonexistent")
	// Should not error even if schema doesn't exist
	require.NoError(t, err)
}

func TestClearSchema_EmptyTenantID(t *testing.T) {
	sv := NewSchemaValidator()

	err := sv.ClearSchema("", "schema_name")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestClearAllSchemas(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{"type": "object"}`)

	err := sv.RegisterSchema("tenant1", "schema1", schemaData)
	require.NoError(t, err)

	err = sv.RegisterSchema("tenant1", "schema2", schemaData)
	require.NoError(t, err)

	err = sv.RegisterSchema("tenant2", "schema1", schemaData)
	require.NoError(t, err)

	// Clear all
	sv.ClearAllSchemas()

	// Verify all are gone
	_, err = sv.GetSchema("tenant1", "schema1")
	assert.Error(t, err)

	_, err = sv.GetSchema("tenant1", "schema2")
	assert.Error(t, err)

	_, err = sv.GetSchema("tenant2", "schema1")
	assert.Error(t, err)
}

func TestValidateRequired_AllPresent(t *testing.T) {
	data := map[string]interface{}{
		"id":   "123",
		"name": "John",
		"age":  30,
	}

	missing := ValidateRequired(data, []string{"id", "name", "age"})
	assert.Empty(t, missing)
}

func TestValidateRequired_SomeMissing(t *testing.T) {
	data := map[string]interface{}{
		"id":   "123",
		"name": "John",
	}

	missing := ValidateRequired(data, []string{"id", "name", "age", "email"})
	assert.Equal(t, 2, len(missing))
	assert.Contains(t, missing, "age")
	assert.Contains(t, missing, "email")
}

func TestValidateRequired_AllMissing(t *testing.T) {
	data := map[string]interface{}{}

	missing := ValidateRequired(data, []string{"id", "name"})
	assert.Equal(t, 2, len(missing))
}

func TestValidateRequired_NilValue(t *testing.T) {
	data := map[string]interface{}{
		"id":   "123",
		"name": nil,
	}

	missing := ValidateRequired(data, []string{"id", "name"})
	assert.Equal(t, 1, len(missing))
	assert.Contains(t, missing, "name")
}

func TestValidateRequired_EmptyFields(t *testing.T) {
	data := map[string]interface{}{
		"id": "123",
	}

	missing := ValidateRequired(data, []string{})
	assert.Empty(t, missing)
}

// TestComplexSchemaValidation tests validation with nested objects
func TestComplexSchemaValidation(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"user": {
				"type": "object",
				"properties": {
					"name": {"type": "string"},
					"email": {"type": "string"}
				},
				"required": ["name", "email"]
			}
		},
		"required": ["id", "user"]
	}`)

	err := sv.RegisterSchema("tenant1", "complex", schemaData)
	require.NoError(t, err)

	// Valid nested data
	validData := map[string]interface{}{
		"id": "123",
		"user": map[string]interface{}{
			"name":  "John",
			"email": "john@example.com",
		},
	}

	err = sv.ValidateInput("tenant1", "complex", validData)
	assert.NoError(t, err)

	// Invalid nested data (missing required field)
	invalidData := map[string]interface{}{
		"id": "123",
		"user": map[string]interface{}{
			"name": "John",
		},
	}

	err = sv.ValidateInput("tenant1", "complex", invalidData)
	assert.Error(t, err)
}

// TestConcurrentValidation tests thread-safety of validator
func TestConcurrentValidation(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"}
		}
	}`)

	err := sv.RegisterSchema("tenant1", "test", schemaData)
	require.NoError(t, err)

	// Run validations concurrently
	done := make(chan error, 10)

	for i := 0; i < 10; i++ {
		go func() {
			data := map[string]interface{}{"id": "123"}
			done <- sv.ValidateInput("tenant1", "test", data)
		}()
	}

	// Collect results
	for i := 0; i < 10; i++ {
		err := <-done
		assert.NoError(t, err)
	}
}

// TestValidationWithJSONUnmarshal tests validation of parsed JSON data
func TestValidationWithJSONUnmarshal(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"amount": {"type": "number"},
			"active": {"type": "boolean"}
		},
		"required": ["id", "amount"]
	}`)

	err := sv.RegisterSchema("tenant1", "order", schemaData)
	require.NoError(t, err)

	jsonData := `{"id": "order_123", "amount": 99.99, "active": true}`
	var data map[string]interface{}
	err = json.Unmarshal([]byte(jsonData), &data)
	require.NoError(t, err)

	err = sv.ValidateInput("tenant1", "order", data)
	assert.NoError(t, err)
}

// TestArrayValidation tests validation of arrays in schemas
func TestArrayValidation(t *testing.T) {
	sv := NewSchemaValidator()
	schemaData := []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"},
			"items": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"sku": {"type": "string"},
						"qty": {"type": "integer"}
					},
					"required": ["sku", "qty"]
				}
			}
		}
	}`)

	err := sv.RegisterSchema("tenant1", "order_with_items", schemaData)
	require.NoError(t, err)

	validData := map[string]interface{}{
		"id": "order_123",
		"items": []interface{}{
			map[string]interface{}{"sku": "SKU001", "qty": float64(5)},
			map[string]interface{}{"sku": "SKU002", "qty": float64(3)},
		},
	}

	err = sv.ValidateInput("tenant1", "order_with_items", validData)
	assert.NoError(t, err)

	// Invalid array item (missing required field)
	invalidData := map[string]interface{}{
		"id": "order_123",
		"items": []interface{}{
			map[string]interface{}{"sku": "SKU001"},
		},
	}

	err = sv.ValidateInput("tenant1", "order_with_items", invalidData)
	assert.Error(t, err)
}
