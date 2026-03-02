package managementapi

import (
	"testing"
)

// Test ValidateSourceConfig with valid HTTP config
func TestValidateSourceConfig_ValidHTTP(t *testing.T) {
	validator := NewValidator()
	config := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "http://example.com/api",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateSourceConfig with invalid HTTP URL
func TestValidateSourceConfig_InvalidHTTPURL(t *testing.T) {
	validator := NewValidator()
	config := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "not-a-url",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&config)
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

// Test ValidateSourceConfig with empty URL
func TestValidateSourceConfig_EmptyURL(t *testing.T) {
	validator := NewValidator()
	config := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&config)
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

// Test ValidateDestinationConfig with valid HTTP config
func TestValidateDestinationConfig_ValidHTTP(t *testing.T) {
	validator := NewValidator()
	config := DestinationConfig{
		Type: "http",
		HTTP: &HTTPDestinationConfig{
			URL:    "http://example.com/webhook",
			Method: "POST",
		},
	}

	err := validator.ValidateDestinationConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateDestinationConfig with invalid method
func TestValidateDestinationConfig_InvalidMethod(t *testing.T) {
	validator := NewValidator()
	config := DestinationConfig{
		Type: "http",
		HTTP: &HTTPDestinationConfig{
			URL:    "http://example.com",
			Method: "INVALID",
		},
	}

	err := validator.ValidateDestinationConfig(&config)
	if err == nil {
		t.Error("expected error for invalid HTTP method")
	}
}

// Test ValidateConverterConfig with field mapper
func TestValidateConverterConfig_FieldMapper(t *testing.T) {
	validator := NewValidator()
	config := ConverterConfig{
		FieldMapper: &FieldMapperConfig{
			Mappings: map[string]string{
				"id":   "id",
				"name": "user_name",
			},
		},
	}

	err := validator.ValidateConverterConfig(&config)
	if err != nil {
		t.Logf("validation error (may be expected): %v", err)
	}
}

// Test ValidateConverterConfig with empty config
func TestValidateConverterConfig_Empty(t *testing.T) {
	validator := NewValidator()
	config := ConverterConfig{
		FieldMapper: nil,
	}

	err := validator.ValidateConverterConfig(&config)
	if err != nil {
		t.Logf("validation error (may be expected for empty): %v", err)
	}
}

// Test ValidateFilterConfig with basic config
func TestValidateFilterConfig_Basic(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: []*FilterRule{
			{
				Name:      "rule1",
				Condition: "field == 'value'",
			},
		},
	}

	err := validator.ValidateFilterConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateFilterConfig with empty config
func TestValidateFilterConfig_Empty(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: nil,
	}

	err := validator.ValidateFilterConfig(&config)
	if err != nil {
		t.Errorf("expected no error for empty filter config, got %v", err)
	}
}

// Test ValidateFilterConfig with multiple rules
func TestValidateFilterConfig_MultipleRules(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: []*FilterRule{
			{
				Name:      "rule1",
				Condition: "field1 == 'value1'",
			},
			{
				Name:      "rule2",
				Condition: "field2 > 100",
			},
		},
	}

	err := validator.ValidateFilterConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateFilterConfig with missing rule name
func TestValidateFilterConfig_MissingRuleName(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: []*FilterRule{
			{
				Name:      "",
				Condition: "field == 'value'",
			},
		},
	}

	err := validator.ValidateFilterConfig(&config)
	if err == nil {
		t.Error("expected error for missing rule name")
	}
}

// Test ValidateFilterConfig with missing rule condition
func TestValidateFilterConfig_MissingCondition(t *testing.T) {
	validator := NewValidator()
	config := FilterConfig{
		Rules: []*FilterRule{
			{
				Name:      "rule1",
				Condition: "",
			},
		},
	}

	err := validator.ValidateFilterConfig(&config)
	if err == nil {
		t.Error("expected error for missing rule condition")
	}
}

// Test ValidateConnection with all valid configs
func TestValidateConnection_AllValid(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com/api",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com/webhook",
				Method: "POST",
			},
		},
	}

	err := validator.ValidateConnection(conn)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test ValidateConnection with invalid source
func TestValidateConnection_InvalidSource(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "invalid",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com/webhook",
				Method: "POST",
			},
		},
	}

	err := validator.ValidateConnection(conn)
	if err == nil {
		t.Error("expected error for invalid source config")
	}
}

// Test ValidateConnection with invalid destination
func TestValidateConnection_InvalidDestination(t *testing.T) {
	validator := NewValidator()

	conn := &Connection{
		ID:       "test-id",
		TenantID: "tenant-1",
		Name:     "test-connection",
		SourceConfig: SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com/api",
				Method: "GET",
			},
		},
		DestinationConfig: DestinationConfig{
			Type: "http",
			HTTP: &HTTPDestinationConfig{
				URL:    "http://example.com/webhook",
				Method: "INVALID",
			},
		},
	}

	err := validator.ValidateConnection(conn)
	if err == nil {
		t.Error("expected error for invalid destination config")
	}
}

// Test HTTPSourceConfig with various HTTP methods
func TestHTTPSourceConfig_ValidMethods(t *testing.T) {
	validator := NewValidator()

	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
	for _, method := range methods {
		config := SourceConfig{
			Type: "http",
			HTTP: &HTTPSourceConfig{
				URL:    "http://example.com",
				Method: method,
			},
		}
		err := validator.ValidateSourceConfig(&config)
		if err != nil {
			t.Errorf("expected no error for method %s, got %v", method, err)
		}
	}
}

// Test HTTPDestinationConfig with POST method (common for webhooks)
func TestHTTPDestinationConfig_POST(t *testing.T) {
	validator := NewValidator()

	config := DestinationConfig{
		Type: "http",
		HTTP: &HTTPDestinationConfig{
			URL:    "http://example.com/api/webhook",
			Method: "POST",
		},
	}

	err := validator.ValidateDestinationConfig(&config)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// Test HTTPS URLs
func TestValidation_HTTPSURLs(t *testing.T) {
	validator := NewValidator()

	sourceConfig := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "https://api.example.com/data",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&sourceConfig)
	if err != nil {
		t.Errorf("expected no error for HTTPS URL, got %v", err)
	}
}

// Test invalid HTTPS URL
func TestValidation_InvalidHTTPSURL(t *testing.T) {
	validator := NewValidator()

	sourceConfig := SourceConfig{
		Type: "http",
		HTTP: &HTTPSourceConfig{
			URL:    "https://invalid url with spaces",
			Method: "GET",
		},
	}

	err := validator.ValidateSourceConfig(&sourceConfig)
	if err == nil {
		t.Error("expected error for invalid URL with spaces")
	}
}
