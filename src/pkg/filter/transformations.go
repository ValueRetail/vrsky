package filter

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
)

// TransformationEngine handles metadata transformations
type TransformationEngine interface {
	// ApplyTransformations applies a list of transformations to envelope metadata
	// Returns error if any transformation fails (fail-fast behavior)
	ApplyTransformations(env *envelope.Envelope, transformations []*Transformation, payload interface{}) error
}

// TransformationEngineImpl implements the TransformationEngine interface
type TransformationEngineImpl struct {
	templateEngine  *TemplateEngine
	conditionEngine *ConditionEngine
}

// NewTransformationEngine creates a new transformation engine
func NewTransformationEngine(conditionEngine *ConditionEngine) TransformationEngine {
	return &TransformationEngineImpl{
		templateEngine:  NewTemplateEngine(),
		conditionEngine: conditionEngine,
	}
}

// ApplyTransformations applies all transformations in sequence to envelope metadata
func (te *TransformationEngineImpl) ApplyTransformations(env *envelope.Envelope, transformations []*Transformation, payload interface{}) error {
	if env == nil {
		return fmt.Errorf("envelope cannot be nil")
	}

	// Initialize metadata if nil
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}

	// Apply each transformation in order
	for _, trans := range transformations {
		if trans == nil {
			continue
		}

		switch trans.Action {
		case "add_field":
			if err := te.addField(env, trans); err != nil {
				return fmt.Errorf("add_field transformation failed: %w", err)
			}

		case "remove_field":
			if err := te.removeField(env, trans); err != nil {
				return fmt.Errorf("remove_field transformation failed: %w", err)
			}

		case "rename_field":
			if err := te.renameField(env, trans); err != nil {
				return fmt.Errorf("rename_field transformation failed: %w", err)
			}

		case "set_field":
			if err := te.setField(env, trans); err != nil {
				return fmt.Errorf("set_field transformation failed: %w", err)
			}

		case "extract_field":
			if err := te.extractField(env, trans, payload); err != nil {
				return fmt.Errorf("extract_field transformation failed: %w", err)
			}

		case "enrich_from_config":
			if err := te.enrichFromConfig(env, trans); err != nil {
				return fmt.Errorf("enrich_from_config transformation failed: %w", err)
			}

		default:
			return fmt.Errorf("unknown transformation action: %s", trans.Action)
		}
	}

	return nil
}

// addField adds a new field to metadata (or overwrites if exists)
// Supports template expressions in Value
func (te *TransformationEngineImpl) addField(env *envelope.Envelope, trans *Transformation) error {
	if trans.Field == "" {
		return fmt.Errorf("field name required for add_field transformation")
	}

	// Resolve template if value is string
	value := trans.Value
	if strVal, ok := trans.Value.(string); ok {
		resolved, err := te.templateEngine.Resolve(strVal)
		if err != nil {
			return fmt.Errorf("resolve template '%s': %w", strVal, err)
		}
		value = resolved
	}

	env.Metadata[trans.Field] = value
	return nil
}

// removeField removes a field from metadata
func (te *TransformationEngineImpl) removeField(env *envelope.Envelope, trans *Transformation) error {
	if trans.Field == "" {
		return fmt.Errorf("field name required for remove_field transformation")
	}

	delete(env.Metadata, trans.Field)
	return nil
}

// renameField renames an existing field in metadata
func (te *TransformationEngineImpl) renameField(env *envelope.Envelope, trans *Transformation) error {
	if trans.Field == "" {
		return fmt.Errorf("field name required for rename_field transformation")
	}
	if trans.Source == "" {
		return fmt.Errorf("source field required for rename_field transformation")
	}

	// Get value from source field
	value, exists := env.Metadata[trans.Source]
	if !exists {
		return fmt.Errorf("source field '%s' does not exist in metadata", trans.Source)
	}

	// Remove source and add to target
	delete(env.Metadata, trans.Source)
	env.Metadata[trans.Field] = value
	return nil
}

// setField sets a field value in metadata (overwrite if exists)
// Different from add_field: does not support templates, uses raw value
func (te *TransformationEngineImpl) setField(env *envelope.Envelope, trans *Transformation) error {
	if trans.Field == "" {
		return fmt.Errorf("field name required for set_field transformation")
	}

	env.Metadata[trans.Field] = trans.Value
	return nil
}

// extractField extracts a value from payload and adds to metadata
func (te *TransformationEngineImpl) extractField(env *envelope.Envelope, trans *Transformation, payload interface{}) error {
	if trans.Field == "" {
		return fmt.Errorf("field name required for extract_field transformation")
	}
	if trans.Source == "" {
		return fmt.Errorf("source field path required for extract_field transformation")
	}

	// Extract value from payload using dot notation
	value, err := te.conditionEngine.GetFieldValue(payload, trans.Source)
	if err != nil {
		return fmt.Errorf("extract field '%s' from payload: %w", trans.Source, err)
	}

	env.Metadata[trans.Field] = value
	return nil
}

// enrichFromConfig adds predefined enrichment values to metadata
// For future extensibility with configuration-based enrichments
func (te *TransformationEngineImpl) enrichFromConfig(env *envelope.Envelope, trans *Transformation) error {
	if trans.Field == "" {
		return fmt.Errorf("field name required for enrich_from_config transformation")
	}

	// Value should contain enrichment data (map)
	enrichData, ok := trans.Value.(map[string]interface{})
	if !ok {
		return fmt.Errorf("enrich_from_config value must be a map")
	}

	// Merge enrichment data into metadata
	for k, v := range enrichData {
		env.Metadata[k] = v
	}
	return nil
}

// TemplateEngine resolves template expressions in strings
type TemplateEngine struct {
	// Template functions for expression evaluation
	functions map[string]func() interface{}
}

// NewTemplateEngine creates a new template engine
func NewTemplateEngine() *TemplateEngine {
	te := &TemplateEngine{
		functions: make(map[string]func() interface{}),
	}

	// Register built-in functions
	te.functions["now"] = func() interface{} {
		return time.Now().Format(time.RFC3339Nano)
	}
	te.functions["uuid"] = func() interface{} {
		return uuid.New().String()
	}

	return te
}

// Resolve resolves all template expressions in the input string
// Supported expressions:
//   - ${field} -> looks up metadata field (not supported in this version)
//   - ${now()} -> current timestamp RFC3339Nano
//   - ${uuid()} -> new UUID v4
//   - ${env:VAR_NAME} -> environment variable
//   - ${random:min:max} -> random integer between min and max
func (te *TemplateEngine) Resolve(template string) (interface{}, error) {
	if !strings.Contains(template, "${") {
		// No templates, return as-is
		return template, nil
	}

	result := template

	// Pattern: ${...}
	pattern := regexp.MustCompile(`\$\{([^}]+)\}`)
	matches := pattern.FindAllStringSubmatchIndex(result, -1)

	// Process matches in reverse order to maintain string indices
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		expr := result[match[2]:match[3]] // e.g., "uuid()"

		value, err := te.resolveExpression(expr)
		if err != nil {
			return nil, fmt.Errorf("resolve expression '%s': %w", expr, err)
		}

		// Convert value to string for template substitution
		valueStr := fmt.Sprintf("%v", value)
		result = result[:match[0]] + valueStr + result[match[1]:]
	}

	return result, nil
}

// resolveExpression resolves a single template expression
func (te *TemplateEngine) resolveExpression(expr string) (interface{}, error) {
	expr = strings.TrimSpace(expr)

	// Function calls: now(), uuid()
	if strings.HasSuffix(expr, "()") {
		funcName := expr[:len(expr)-2]
		if fn, ok := te.functions[funcName]; ok {
			return fn(), nil
		}
		return nil, fmt.Errorf("unknown function: %s", funcName)
	}

	// Environment variables: env:VAR_NAME
	if strings.HasPrefix(expr, "env:") {
		varName := expr[4:]
		value := os.Getenv(varName)
		if value == "" {
			return nil, fmt.Errorf("environment variable not set: %s", varName)
		}
		return value, nil
	}

	// Random numbers: random:min:max
	if strings.HasPrefix(expr, "random:") {
		parts := strings.Split(expr, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid random expression: %s (format: random:min:max)", expr)
		}

		min, err := parseIntValue(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid min value: %w", err)
		}

		max, err := parseIntValue(parts[2])
		if err != nil {
			return nil, fmt.Errorf("invalid max value: %w", err)
		}

		if min > max {
			return nil, fmt.Errorf("min (%d) cannot be greater than max (%d)", min, max)
		}

		// Use crypto/rand for thread-safe random number generation
		// This avoids race conditions when transformations are applied concurrently
		randNum, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
		if err != nil {
			return nil, fmt.Errorf("failed to generate random number: %w", err)
		}

		return min + int(randNum.Int64()), nil
	}

	// Field reference: field_name (not supported in priority 2, reserved for future)
	if !strings.Contains(expr, "(") && !strings.Contains(expr, ":") {
		return nil, fmt.Errorf("field references (${field}) not supported in transformations")
	}

	return nil, fmt.Errorf("unknown expression type: %s", expr)
}

// parseIntValue parses an integer from string with error handling
func parseIntValue(s string) (int, error) {
	s = strings.TrimSpace(s)
	var i int
	_, err := fmt.Sscanf(s, "%d", &i)
	if err != nil {
		return 0, fmt.Errorf("cannot parse as integer: %s", s)
	}
	return i, nil
}
