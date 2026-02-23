package converter

import (
	"encoding/json"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestConverterConfigMarshalYAML tests YAML marshaling of ConverterConfig
func TestConverterConfigMarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		config  *ConverterConfig
		wantErr bool
	}{
		{
			name: "valid config marshals to YAML",
			config: &ConverterConfig{
				ConverterID:  "test-converter",
				TenantID:     "test-tenant",
				InputTopic:   "hp.webhook.received",
				OutputTopic:  "hp.webhook.received.converted",
				ErrorTopic:   "hp.webhook.errors",
				MaxRetries:   3,
				RetryBackoff: "exponential",
				ErrorHandling: ErrorHandlingConfig{
					MissingFields:   "fail",
					TypeMismatch:    "fail",
					ValidationError: "fail",
				},
			},
			wantErr: false,
		},
		{
			name: "config with transformations marshals to YAML",
			config: &ConverterConfig{
				ConverterID:  "test-converter",
				TenantID:     "test-tenant",
				InputTopic:   "elkjop.order",
				OutputTopic:  "elkjop.order.converted",
				ErrorTopic:   "elkjop.order.errors",
				MaxRetries:   3,
				RetryBackoff: "exponential",
				Transformations: []Transformation{
					{
						Source: "order.id",
						Target: "orderID",
						Type:   "string",
					},
				},
				ErrorHandling: ErrorHandlingConfig{
					MissingFields:   "fail",
					TypeMismatch:    "fail",
					ValidationError: "fail",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := yaml.Marshal(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("YAML marshal error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil && len(data) == 0 {
				t.Error("expected YAML data, got empty")
			}
		})
	}
}

// TestConverterConfigUnmarshalYAML tests YAML unmarshaling into ConverterConfig
func TestConverterConfigUnmarshalYAML(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantID    string
		wantTopic string
		wantErr   bool
	}{
		{
			name: "valid YAML unmarshals to config",
			yaml: `
converter_id: test-converter
tenant_id: test-tenant
input_topic: hp.webhook.received
output_topic: hp.webhook.received.converted
error_topic: hp.webhook.errors
max_retries: 3
retry_backoff: exponential
error_handling:
  missing_fields: fail
  type_mismatch: fail
  validation_error: fail
`,
			wantID:    "test-converter",
			wantTopic: "hp.webhook.received",
			wantErr:   false,
		},
		{
			name: "YAML with empty transformations unmarshals",
			yaml: `
converter_id: elkjop-converter
tenant_id: elkjop-tenant
input_topic: elkjop.order
output_topic: elkjop.order.converted
error_topic: elkjop.order.errors
max_retries: 2
retry_backoff: exponential
transformations: []
error_handling:
  missing_fields: skip
  type_mismatch: coerce
  validation_error: fail
`,
			wantID:    "elkjop-converter",
			wantTopic: "elkjop.order",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var config ConverterConfig
			err := yaml.Unmarshal([]byte(tt.yaml), &config)
			if (err != nil) != tt.wantErr {
				t.Errorf("YAML unmarshal error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				if config.ConverterID != tt.wantID {
					t.Errorf("got converter_id = %q, want %q", config.ConverterID, tt.wantID)
				}
				if config.InputTopic != tt.wantTopic {
					t.Errorf("got input_topic = %q, want %q", config.InputTopic, tt.wantTopic)
				}
			}
		})
	}
}

// TestConverterConfigMarshalJSON tests JSON marshaling of ConverterConfig
func TestConverterConfigMarshalJSON(t *testing.T) {
	config := &ConverterConfig{
		ConverterID:  "test-converter",
		TenantID:     "test-tenant",
		InputTopic:   "hp.webhook.received",
		OutputTopic:  "hp.webhook.received.converted",
		ErrorTopic:   "hp.webhook.errors",
		MaxRetries:   3,
		RetryBackoff: "exponential",
		ErrorHandling: ErrorHandlingConfig{
			MissingFields:   "fail",
			TypeMismatch:    "fail",
			ValidationError: "fail",
		},
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Errorf("JSON marshal error = %v", err)
		return
	}

	if len(data) == 0 {
		t.Error("expected JSON data, got empty")
	}

	// Verify we can unmarshal it back
	var unmarshaled ConverterConfig
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Errorf("JSON unmarshal error = %v", err)
		return
	}

	if unmarshaled.ConverterID != config.ConverterID {
		t.Errorf("got converter_id = %q, want %q", unmarshaled.ConverterID, config.ConverterID)
	}
}

// TestTransformationStruct tests Transformation structure
func TestTransformationStruct(t *testing.T) {
	tests := []struct {
		name string
		tx   Transformation
		want bool
	}{
		{
			name: "simple field mapping",
			tx: Transformation{
				Source: "order.id",
				Target: "orderID",
				Type:   "string",
			},
			want: true,
		},
		{
			name: "transformation with expression",
			tx: Transformation{
				Target:     "total",
				Expression: "sum(order.line_items[].price)",
				Type:       "float",
			},
			want: true,
		},
		{
			name: "transformation with function",
			tx: Transformation{
				Target:   "account",
				Function: "lookup_customer_account(order.customer.email)",
				Type:     "string",
			},
			want: true,
		},
		{
			name: "conditional transformation",
			tx: Transformation{
				Target:    "status",
				Condition: "order.total > 5000",
				Value:     "premium",
				Type:      "string",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.tx.Target == "" {
				t.Error("expected Target to be set")
			}
		})
	}
}

// TestValidationSchema tests ValidationSchema structure
func TestValidationSchema(t *testing.T) {
	schema := &ValidationSchema{
		RequiredFields: []string{"order.id", "order.customer.email"},
	}

	if len(schema.RequiredFields) != 2 {
		t.Errorf("got %d required fields, want 2", len(schema.RequiredFields))
	}

	if schema.RequiredFields[0] != "order.id" {
		t.Errorf("got first field = %q, want \"order.id\"", schema.RequiredFields[0])
	}
}

// TestErrorHandlingConfig tests ErrorHandlingConfig structure
func TestErrorHandlingConfig(t *testing.T) {
	tests := []struct {
		name             string
		config           ErrorHandlingConfig
		wantMissing      string
		wantTypeMismatch string
	}{
		{
			name: "fail strategy",
			config: ErrorHandlingConfig{
				MissingFields:   "fail",
				TypeMismatch:    "fail",
				ValidationError: "fail",
			},
			wantMissing:      "fail",
			wantTypeMismatch: "fail",
		},
		{
			name: "mixed strategies",
			config: ErrorHandlingConfig{
				MissingFields:   "skip",
				TypeMismatch:    "coerce",
				ValidationError: "fail",
			},
			wantMissing:      "skip",
			wantTypeMismatch: "coerce",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.MissingFields != tt.wantMissing {
				t.Errorf("got missing_fields = %q, want %q", tt.config.MissingFields, tt.wantMissing)
			}
			if tt.config.TypeMismatch != tt.wantTypeMismatch {
				t.Errorf("got type_mismatch = %q, want %q", tt.config.TypeMismatch, tt.wantTypeMismatch)
			}
		})
	}
}

// TestTransformResult tests TransformResult structure
func TestTransformResult(t *testing.T) {
	errors := []TransformationError{
		{
			Field:     "order.id",
			Message:   "field not found",
			Type:      "extraction",
			Timestamp: time.Now(),
		},
	}

	result := &TransformResult{
		Success:    false,
		Data:       nil,
		Errors:     errors,
		RetryCount: 1,
	}

	if result.Success {
		t.Error("expected Success to be false")
	}
	if result.RetryCount != 1 {
		t.Errorf("got RetryCount = %d, want 1", result.RetryCount)
	}
	if len(result.Errors) != 1 {
		t.Errorf("got %d errors, want 1", len(result.Errors))
	}
}

// TestTransformationError tests TransformationError structure
func TestTransformationError(t *testing.T) {
	now := time.Now()
	err := TransformationError{
		Field:     "customer.email",
		Message:   "invalid email format",
		Type:      "validation",
		Timestamp: now,
	}

	if err.Field != "customer.email" {
		t.Errorf("got Field = %q, want \"customer.email\"", err.Field)
	}
	if err.Type != "validation" {
		t.Errorf("got Type = %q, want \"validation\"", err.Type)
	}
	if err.Timestamp != now {
		t.Errorf("got Timestamp = %v, want %v", err.Timestamp, now)
	}
}

// TestTransformResultSuccess tests successful transform result
func TestTransformResultSuccess(t *testing.T) {
	data := map[string]interface{}{
		"orderID": "12345",
		"total":   99.99,
	}

	result := &TransformResult{
		Success:    true,
		Data:       data,
		Errors:     nil,
		RetryCount: 0,
	}

	if !result.Success {
		t.Error("expected Success to be true")
	}
	if len(result.Errors) != 0 {
		t.Errorf("got %d errors, want 0", len(result.Errors))
	}
}
