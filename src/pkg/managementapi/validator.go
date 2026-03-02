package managementapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Validator provides configuration validation
type Validator struct {
	schemaCompiler *jsonschema.Compiler
}

// NewValidator creates a new validator
func NewValidator() *Validator {
	return &Validator{
		schemaCompiler: jsonschema.NewCompiler(),
	}
}

// ValidateConnection validates a complete connection configuration
func (v *Validator) ValidateConnection(conn *Connection) error {
	if conn == nil {
		return &BadRequestError{Message: "connection cannot be nil"}
	}

	if strings.TrimSpace(conn.Name) == "" {
		return &BadRequestError{Message: "connection name is required"}
	}

	if err := v.ValidateSourceConfig(&conn.SourceConfig); err != nil {
		return err
	}

	if err := v.ValidateConverterConfig(&conn.ConverterConfig); err != nil {
		return err
	}

	if err := v.ValidateFilterConfig(&conn.FilterConfig); err != nil {
		return err
	}

	if err := v.ValidateDestinationConfig(&conn.DestinationConfig); err != nil {
		return err
	}

	return nil
}

// ValidateSourceConfig validates source configuration
func (v *Validator) ValidateSourceConfig(config *SourceConfig) error {
	if config == nil {
		return &ConfigError{Component: "source", Field: "type", Reason: "source config is required"}
	}

	if strings.TrimSpace(config.Type) == "" {
		return &ConfigError{Component: "source", Field: "type", Reason: "type is required (http, file, or database)"}
	}

	switch config.Type {
	case "http":
		return v.validateHTTPSource(config.HTTP)
	case "file":
		return v.validateFileSource(config.File)
	case "database":
		return v.validateDatabaseSource(config.Database)
	default:
		return &ConfigError{Component: "source", Field: "type", Reason: fmt.Sprintf("invalid type '%s', must be http, file, or database", config.Type)}
	}
}

// validateHTTPSource validates HTTP source configuration
func (v *Validator) validateHTTPSource(config *HTTPSourceConfig) error {
	if config == nil {
		return &ConfigError{Component: "source", Field: "http", Reason: "HTTP source config is required when type is 'http'"}
	}

	if strings.TrimSpace(config.URL) == "" {
		return &ConfigError{Component: "source", Field: "http.url", Reason: "URL is required"}
	}

	// Validate URL format
	parsedURL, err := url.Parse(config.URL)
	if err != nil {
		return &ConfigError{Component: "source", Field: "http.url", Reason: fmt.Sprintf("invalid URL format: %v", err)}
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return &ConfigError{Component: "source", Field: "http.url", Reason: fmt.Sprintf("URL must use http or https scheme, got '%s'", parsedURL.Scheme)}
	}

	// Validate method if provided
	if config.Method != "" && !isValidHTTPMethod(config.Method) {
		return &ConfigError{Component: "source", Field: "http.method", Reason: fmt.Sprintf("invalid HTTP method '%s'", config.Method)}
	}

	// Validate auth if provided
	if config.Auth != nil {
		if err := v.validateAuthConfig(config.Auth, "source.http.auth"); err != nil {
			return err
		}
	}

	return nil
}

// validateFileSource validates file source configuration
func (v *Validator) validateFileSource(config *FileSourceConfig) error {
	if config == nil {
		return &ConfigError{Component: "source", Field: "file", Reason: "file source config is required when type is 'file'"}
	}

	if strings.TrimSpace(config.Path) == "" {
		return &ConfigError{Component: "source", Field: "file.path", Reason: "path is required"}
	}

	// Validate regex pattern if provided
	if config.Pattern != "" {
		if _, err := regexp.Compile(config.Pattern); err != nil {
			return &ConfigError{Component: "source", Field: "file.pattern", Reason: fmt.Sprintf("invalid regex pattern: %v", err)}
		}
	}

	// Validate encoding
	if config.Encoding != "" && !isValidEncoding(config.Encoding) {
		return &ConfigError{Component: "source", Field: "file.encoding", Reason: fmt.Sprintf("unsupported encoding '%s'", config.Encoding)}
	}

	return nil
}

// validateDatabaseSource validates database source configuration
func (v *Validator) validateDatabaseSource(config *DatabaseSourceConfig) error {
	if config == nil {
		return &ConfigError{Component: "source", Field: "database", Reason: "database source config is required when type is 'database'"}
	}

	if strings.TrimSpace(config.ConnectionString) == "" {
		return &ConfigError{Component: "source", Field: "database.connection_string", Reason: "connection string is required"}
	}

	if strings.TrimSpace(config.Query) == "" {
		return &ConfigError{Component: "source", Field: "database.query", Reason: "query is required"}
	}

	// Validate poll interval if provided
	if config.PollInterval < 0 {
		return &ConfigError{Component: "source", Field: "database.poll_interval", Reason: "poll interval cannot be negative"}
	}

	return nil
}

// ValidateConverterConfig validates converter configuration
func (v *Validator) ValidateConverterConfig(config *ConverterConfig) error {
	if config == nil {
		return nil // Converter config is optional
	}

	if config.SchemaValidator != nil {
		if err := v.validateSchemaValidator(config.SchemaValidator); err != nil {
			return err
		}
	}

	if config.FieldMapper != nil {
		if err := v.validateFieldMapper(config.FieldMapper); err != nil {
			return err
		}
	}

	if config.RuleEngine != nil {
		if err := v.validateRuleEngine(config.RuleEngine); err != nil {
			return err
		}
	}

	return nil
}

// validateSchemaValidator validates schema validator configuration
func (v *Validator) validateSchemaValidator(config *SchemaValidatorConfig) error {
	if config.InputSchema != nil && len(config.InputSchema) > 0 {
		// Validate input schema is valid JSON
		var schema interface{}
		if err := json.Unmarshal(config.InputSchema, &schema); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.input_schema", Reason: fmt.Sprintf("invalid JSON: %v", err)}
		}

		// Create a temporary compiler and add the schema
		c := jsonschema.NewCompiler()
		if err := c.AddResource("input_schema.json", strings.NewReader(string(config.InputSchema))); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.input_schema", Reason: fmt.Sprintf("invalid JSON schema: %v", err)}
		}
		if _, err := c.Compile("input_schema.json"); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.input_schema", Reason: fmt.Sprintf("schema compilation failed: %v", err)}
		}
	}

	if config.OutputSchema != nil && len(config.OutputSchema) > 0 {
		// Validate output schema is valid JSON
		var schema interface{}
		if err := json.Unmarshal(config.OutputSchema, &schema); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.output_schema", Reason: fmt.Sprintf("invalid JSON: %v", err)}
		}

		// Create a temporary compiler and add the schema
		c := jsonschema.NewCompiler()
		if err := c.AddResource("output_schema.json", strings.NewReader(string(config.OutputSchema))); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.output_schema", Reason: fmt.Sprintf("invalid JSON schema: %v", err)}
		}
		if _, err := c.Compile("output_schema.json"); err != nil {
			return &ConfigError{Component: "converter", Field: "schema_validator.output_schema", Reason: fmt.Sprintf("schema compilation failed: %v", err)}
		}
	}

	return nil
}

// validateFieldMapper validates field mapper configuration
func (v *Validator) validateFieldMapper(config *FieldMapperConfig) error {
	if config.Mappings == nil || len(config.Mappings) == 0 {
		return &ConfigError{Component: "converter", Field: "field_mapper.mappings", Reason: "at least one mapping is required"}
	}

	// Validate no empty keys or values
	for key, value := range config.Mappings {
		if strings.TrimSpace(key) == "" {
			return &ConfigError{Component: "converter", Field: "field_mapper.mappings", Reason: "mapping source field cannot be empty"}
		}
		if strings.TrimSpace(value) == "" {
			return &ConfigError{Component: "converter", Field: "field_mapper.mappings", Reason: fmt.Sprintf("mapping destination field for '%s' cannot be empty", key)}
		}
	}

	return nil
}

// validateRuleEngine validates rule engine configuration
func (v *Validator) validateRuleEngine(config *RuleEngineConfig) error {
	if config.Rules == nil || len(config.Rules) == 0 {
		return &ConfigError{Component: "converter", Field: "rule_engine.rules", Reason: "at least one rule is required"}
	}

	for i, rule := range config.Rules {
		if strings.TrimSpace(rule.Name) == "" {
			return &ConfigError{Component: "converter", Field: fmt.Sprintf("rule_engine.rules[%d].name", i), Reason: "rule name is required"}
		}
		if strings.TrimSpace(rule.Condition) == "" {
			return &ConfigError{Component: "converter", Field: fmt.Sprintf("rule_engine.rules[%d].condition", i), Reason: "rule condition is required"}
		}
		if strings.TrimSpace(rule.Transformation) == "" {
			return &ConfigError{Component: "converter", Field: fmt.Sprintf("rule_engine.rules[%d].transformation", i), Reason: "rule transformation is required"}
		}
	}

	return nil
}

// ValidateFilterConfig validates filter configuration
func (v *Validator) ValidateFilterConfig(config *FilterConfig) error {
	if config == nil {
		return nil // Filter config is optional
	}

	if config.Rules != nil && len(config.Rules) > 0 {
		for i, rule := range config.Rules {
			if strings.TrimSpace(rule.Name) == "" {
				return &ConfigError{Component: "filter", Field: fmt.Sprintf("rules[%d].name", i), Reason: "rule name is required"}
			}
			if strings.TrimSpace(rule.Condition) == "" {
				return &ConfigError{Component: "filter", Field: fmt.Sprintf("rules[%d].condition", i), Reason: "rule condition is required"}
			}
		}
	}

	if config.WASM != nil && len(config.WASM.Binary) > 0 {
		if err := v.validateWASM(config.WASM); err != nil {
			return err
		}
	}

	return nil
}

// validateWASM validates WASM binary
func (v *Validator) validateWASM(config *WASMConfig) error {
	if len(config.Binary) == 0 {
		return &ConfigError{Component: "filter", Field: "wasm.binary", Reason: "WASM binary cannot be empty"}
	}

	// Basic WASM magic number validation (0x00 0x61 0x73 0x6d = \0asm)
	if len(config.Binary) < 4 || config.Binary[0] != 0x00 || config.Binary[1] != 0x61 || config.Binary[2] != 0x73 || config.Binary[3] != 0x6d {
		return &ConfigError{Component: "filter", Field: "wasm.binary", Reason: "invalid WASM binary format"}
	}

	return nil
}

// ValidateDestinationConfig validates destination configuration
func (v *Validator) ValidateDestinationConfig(config *DestinationConfig) error {
	if config == nil {
		return &ConfigError{Component: "destination", Field: "type", Reason: "destination config is required"}
	}

	if strings.TrimSpace(config.Type) == "" {
		return &ConfigError{Component: "destination", Field: "type", Reason: "type is required (http, file, or database)"}
	}

	switch config.Type {
	case "http":
		return v.validateHTTPDestination(config.HTTP)
	case "file":
		return v.validateFileDestination(config.File)
	case "database":
		return v.validateDatabaseDestination(config.Database)
	default:
		return &ConfigError{Component: "destination", Field: "type", Reason: fmt.Sprintf("invalid type '%s', must be http, file, or database", config.Type)}
	}
}

// validateHTTPDestination validates HTTP destination configuration
func (v *Validator) validateHTTPDestination(config *HTTPDestinationConfig) error {
	if config == nil {
		return &ConfigError{Component: "destination", Field: "http", Reason: "HTTP destination config is required when type is 'http'"}
	}

	if strings.TrimSpace(config.URL) == "" {
		return &ConfigError{Component: "destination", Field: "http.url", Reason: "URL is required"}
	}

	// Validate URL format
	parsedURL, err := url.Parse(config.URL)
	if err != nil {
		return &ConfigError{Component: "destination", Field: "http.url", Reason: fmt.Sprintf("invalid URL format: %v", err)}
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return &ConfigError{Component: "destination", Field: "http.url", Reason: fmt.Sprintf("URL must use http or https scheme, got '%s'", parsedURL.Scheme)}
	}

	// Validate method
	if strings.TrimSpace(config.Method) == "" {
		return &ConfigError{Component: "destination", Field: "http.method", Reason: "HTTP method is required"}
	}

	if !isValidHTTPMethod(config.Method) {
		return &ConfigError{Component: "destination", Field: "http.method", Reason: fmt.Sprintf("invalid HTTP method '%s'", config.Method)}
	}

	// Validate auth if provided
	if config.Auth != nil {
		if err := v.validateAuthConfig(config.Auth, "destination.http.auth"); err != nil {
			return err
		}
	}

	return nil
}

// validateFileDestination validates file destination configuration
func (v *Validator) validateFileDestination(config *FileDestinationConfig) error {
	if config == nil {
		return &ConfigError{Component: "destination", Field: "file", Reason: "file destination config is required when type is 'file'"}
	}

	if strings.TrimSpace(config.Path) == "" {
		return &ConfigError{Component: "destination", Field: "file.path", Reason: "path is required"}
	}

	// Validate format
	if config.Format != "" && !isValidFileFormat(config.Format) {
		return &ConfigError{Component: "destination", Field: "file.format", Reason: fmt.Sprintf("unsupported format '%s'", config.Format)}
	}

	return nil
}

// validateDatabaseDestination validates database destination configuration
func (v *Validator) validateDatabaseDestination(config *DatabaseDestinationConfig) error {
	if config == nil {
		return &ConfigError{Component: "destination", Field: "database", Reason: "database destination config is required when type is 'database'"}
	}

	if strings.TrimSpace(config.ConnectionString) == "" {
		return &ConfigError{Component: "destination", Field: "database.connection_string", Reason: "connection string is required"}
	}

	if strings.TrimSpace(config.Query) == "" {
		return &ConfigError{Component: "destination", Field: "database.query", Reason: "query is required"}
	}

	// Validate batch size if provided
	if config.BatchSize < 0 {
		return &ConfigError{Component: "destination", Field: "database.batch_size", Reason: "batch size cannot be negative"}
	}

	return nil
}

// validateAuthConfig validates authentication configuration
func (v *Validator) validateAuthConfig(config *AuthConfig, field string) error {
	if config == nil {
		return nil
	}

	if strings.TrimSpace(config.Type) == "" {
		return &ConfigError{Component: "auth", Field: field + ".type", Reason: "auth type is required"}
	}

	switch config.Type {
	case "basic":
		if config.Basic == nil {
			return &ConfigError{Component: "auth", Field: field, Reason: "basic auth config required"}
		}
		if strings.TrimSpace(config.Basic.Username) == "" {
			return &ConfigError{Component: "auth", Field: field + ".basic.username", Reason: "username is required"}
		}
		if strings.TrimSpace(config.Basic.Password) == "" {
			return &ConfigError{Component: "auth", Field: field + ".basic.password", Reason: "password is required"}
		}

	case "bearer":
		if config.Bearer == nil {
			return &ConfigError{Component: "auth", Field: field, Reason: "bearer auth config required"}
		}
		if strings.TrimSpace(config.Bearer.Token) == "" {
			return &ConfigError{Component: "auth", Field: field + ".bearer.token", Reason: "token is required"}
		}

	case "api_key":
		if config.APIKey == nil {
			return &ConfigError{Component: "auth", Field: field, Reason: "api_key auth config required"}
		}
		if strings.TrimSpace(config.APIKey.HeaderName) == "" {
			return &ConfigError{Component: "auth", Field: field + ".api_key.header_name", Reason: "header name is required"}
		}
		if strings.TrimSpace(config.APIKey.Key) == "" {
			return &ConfigError{Component: "auth", Field: field + ".api_key.key", Reason: "key is required"}
		}

	default:
		return &ConfigError{Component: "auth", Field: field + ".type", Reason: fmt.Sprintf("invalid auth type '%s'", config.Type)}
	}

	return nil
}

// Helper functions

func isValidHTTPMethod(method string) bool {
	validMethods := map[string]bool{
		"GET":     true,
		"POST":    true,
		"PUT":     true,
		"DELETE":  true,
		"PATCH":   true,
		"HEAD":    true,
		"OPTIONS": true,
	}
	return validMethods[strings.ToUpper(method)]
}

func isValidEncoding(encoding string) bool {
	validEncodings := map[string]bool{
		"utf-8":      true,
		"UTF-8":      true,
		"latin-1":    true,
		"latin1":     true,
		"iso-8859-1": true,
		"ascii":      true,
		"ASCII":      true,
	}
	return validEncodings[encoding]
}

func isValidFileFormat(format string) bool {
	validFormats := map[string]bool{
		"json":    true,
		"csv":     true,
		"xml":     true,
		"text":    true,
		"parquet": true,
		"avro":    true,
	}
	return validFormats[strings.ToLower(format)]
}
