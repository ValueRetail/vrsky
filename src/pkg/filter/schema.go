package filter

import (
	"fmt"
	"log/slog"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ValidationMode represents how strictly to validate schemas
type ValidationMode string

const (
	// ModeStrict fails on any validation error
	ModeStrict ValidationMode = "strict"
	// ModeLenient logs validation errors but allows processing to continue
	ModeLenient ValidationMode = "lenient"
)

// SchemaValidator handles JSON Schema validation
type SchemaValidator struct {
	schemas map[string]*jsonschema.Schema
	mode    ValidationMode
	logger  *slog.Logger
}

// NewSchemaValidator creates a new schema validator with strict mode
func NewSchemaValidator() (*SchemaValidator, error) {
	return &SchemaValidator{
		schemas: make(map[string]*jsonschema.Schema),
		mode:    ModeStrict,
		logger:  slog.Default(),
	}, nil
}

// NewSchemaValidatorWithMode creates a new schema validator with specified mode
func NewSchemaValidatorWithMode(mode ValidationMode, logger *slog.Logger) (*SchemaValidator, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if mode != ModeStrict && mode != ModeLenient {
		return nil, fmt.Errorf("invalid validation mode: %s", mode)
	}

	return &SchemaValidator{
		schemas: make(map[string]*jsonschema.Schema),
		mode:    mode,
		logger:  logger,
	}, nil
}

// SetMode changes the validation mode
func (sv *SchemaValidator) SetMode(mode ValidationMode) error {
	if mode != ModeStrict && mode != ModeLenient {
		return fmt.Errorf("invalid validation mode: %s", mode)
	}
	sv.mode = mode
	return nil
}

// RegisterSchema registers a JSON schema by ID
func (sv *SchemaValidator) RegisterSchema(schemaID string, schemaData []byte) error {
	if schemaID == "" {
		return fmt.Errorf("schema id cannot be empty")
	}

	// Compile schema
	// Note: CompileString requires (schemaURL, schemaData)
	schema, err := jsonschema.CompileString("", string(schemaData))
	if err != nil {
		return fmt.Errorf("compile schema %s: %w", schemaID, err)
	}

	sv.schemas[schemaID] = schema
	return nil
}

// Validate validates data against a registered schema
// Behavior depends on the validation mode (strict vs lenient)
func (sv *SchemaValidator) Validate(schemaID string, data interface{}) error {
	schema, ok := sv.schemas[schemaID]
	if !ok {
		return fmt.Errorf("schema not found: %s", schemaID)
	}

	// Validate
	if err := schema.Validate(data); err != nil {
		switch sv.mode {
		case ModeStrict:
			return fmt.Errorf("validation failed: %w", err)
		case ModeLenient:
			sv.logger.Warn("Schema validation failed (lenient mode)",
				"schema_id", schemaID,
				"error", err,
			)
			return nil // Don't fail in lenient mode
		default:
			return fmt.Errorf("unknown validation mode: %s", sv.mode)
		}
	}

	return nil
}

// ValidateStrict validates data against a schema string (always strict)
func (sv *SchemaValidator) ValidateStrict(schemaStr string, data interface{}) error {
	schema, err := jsonschema.CompileString("", schemaStr)
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}

	if err := schema.Validate(data); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	return nil
}

// ValidateLenient validates data against a schema string (always lenient)
func (sv *SchemaValidator) ValidateLenient(schemaStr string, data interface{}) error {
	schema, err := jsonschema.CompileString("", schemaStr)
	if err != nil {
		sv.logger.Warn("Failed to compile schema",
			"error", err,
		)
		return nil // Don't fail on schema compile in lenient mode
	}

	if err := schema.Validate(data); err != nil {
		sv.logger.Warn("Validation failed (lenient mode)",
			"error", err,
		)
		return nil // Don't fail on validation in lenient mode
	}

	return nil
}
