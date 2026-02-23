package converter

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// SchemaValidator provides thread-safe validation of input/output data against JSON schemas
// It supports multi-tenant schema isolation for performance optimization
type SchemaValidator struct {
	mu           sync.RWMutex
	schemas      map[string]*jsonschema.Schema // key: "tenantID:schemaName"
	compiler     *jsonschema.Compiler          // shared compiler for schema compilation
	compilerLock sync.Mutex                    // separate lock for compiler operations (not thread-safe)
}

// NewSchemaValidator creates a new schema validator with thread-safety
func NewSchemaValidator() *SchemaValidator {
	return &SchemaValidator{
		schemas:  make(map[string]*jsonschema.Schema),
		compiler: jsonschema.NewCompiler(),
	}
}

// RegisterSchema registers a JSON Schema for a tenant
// The schema is compiled and cached for subsequent validations
func (sv *SchemaValidator) RegisterSchema(tenantID, schemaName string, schemaData []byte) error {
	if tenantID == "" || schemaName == "" {
		return fmt.Errorf("tenant_id and schema_name are required")
	}
	if len(schemaData) == 0 {
		return fmt.Errorf("schema data cannot be empty")
	}

	// Create a unique URL for this schema to avoid conflicts
	schemaURL := fmt.Sprintf("schema://%s/%s", tenantID, schemaName)

	// Protect compiler operations (AddResource + Compile are not thread-safe)
	sv.compilerLock.Lock()
	defer sv.compilerLock.Unlock()

	// Add the resource to the compiler
	if err := sv.compiler.AddResource(schemaURL, makeReader(schemaData)); err != nil {
		return fmt.Errorf("add schema resource: %w", err)
	}

	// Compile the schema with the compiler
	schema, err := sv.compiler.Compile(schemaURL)
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}

	sv.mu.Lock()
	defer sv.mu.Unlock()

	key := sv.makeKey(tenantID, schemaName)
	sv.schemas[key] = schema

	return nil
}

// ValidateInput validates incoming data against a registered input schema
func (sv *SchemaValidator) ValidateInput(tenantID, schemaName string, data interface{}) error {
	return sv.validate(tenantID, schemaName, data)
}

// ValidateOutput validates outgoing data against a registered output schema
func (sv *SchemaValidator) ValidateOutput(tenantID, schemaName string, data interface{}) error {
	return sv.validate(tenantID, schemaName, data)
}

// validate performs the actual validation against a schema
func (sv *SchemaValidator) validate(tenantID, schemaName string, data interface{}) error {
	if tenantID == "" || schemaName == "" {
		return fmt.Errorf("tenant_id and schema_name are required")
	}

	sv.mu.RLock()
	key := sv.makeKey(tenantID, schemaName)
	schema, exists := sv.schemas[key]
	sv.mu.RUnlock()

	if !exists {
		return fmt.Errorf("schema not found: %s:%s", tenantID, schemaName)
	}

	if err := schema.Validate(data); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	return nil
}

// GetSchema retrieves a registered schema (for testing/inspection)
func (sv *SchemaValidator) GetSchema(tenantID, schemaName string) (*jsonschema.Schema, error) {
	if tenantID == "" || schemaName == "" {
		return nil, fmt.Errorf("tenant_id and schema_name are required")
	}

	sv.mu.RLock()
	defer sv.mu.RUnlock()

	key := sv.makeKey(tenantID, schemaName)
	schema, exists := sv.schemas[key]
	if !exists {
		return nil, fmt.Errorf("schema not found: %s:%s", tenantID, schemaName)
	}

	return schema, nil
}

// ClearSchema removes a cached schema for a tenant
func (sv *SchemaValidator) ClearSchema(tenantID, schemaName string) error {
	if tenantID == "" || schemaName == "" {
		return fmt.Errorf("tenant_id and schema_name are required")
	}

	sv.mu.Lock()
	defer sv.mu.Unlock()

	key := sv.makeKey(tenantID, schemaName)
	delete(sv.schemas, key)

	return nil
}

// ClearAllSchemas removes all cached schemas (useful for cleanup)
func (sv *SchemaValidator) ClearAllSchemas() {
	sv.mu.Lock()
	defer sv.mu.Unlock()

	sv.schemas = make(map[string]*jsonschema.Schema)
}

// makeKey creates a unique key for tenant-scoped schema isolation
func (sv *SchemaValidator) makeKey(tenantID, schemaName string) string {
	return fmt.Sprintf("%s:%s", tenantID, schemaName)
}

// ValidateRequired validates that all required fields are present in data
// This is a simplified validation that complements JSON Schema validation
func ValidateRequired(data map[string]interface{}, requiredFields []string) []string {
	var missing []string

	for _, field := range requiredFields {
		if val, exists := data[field]; !exists || val == nil {
			missing = append(missing, field)
		}
	}

	return missing
}

// makeReader creates an io.ReadCloser from a byte slice
func makeReader(data []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(data))
}
