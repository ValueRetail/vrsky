package converter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *ConverterConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &ConverterConfig{
				ConverterID: "test-conv",
				TenantID:    "test-tenant",
				InputTopic:  "test.input",
				ErrorTopic:  "test.error",
			},
			wantErr: false,
		},
		{
			name: "missing converter_id",
			config: &ConverterConfig{
				TenantID:   "test-tenant",
				InputTopic: "test.input",
				ErrorTopic: "test.error",
			},
			wantErr: true,
			errMsg:  "converter_id",
		},
		{
			name: "missing tenant_id",
			config: &ConverterConfig{
				ConverterID: "test-conv",
				InputTopic:  "test.input",
				ErrorTopic:  "test.error",
			},
			wantErr: true,
			errMsg:  "tenant_id",
		},
		{
			name: "missing input_topic",
			config: &ConverterConfig{
				ConverterID: "test-conv",
				TenantID:    "test-tenant",
				ErrorTopic:  "test.error",
			},
			wantErr: true,
			errMsg:  "input_topic",
		},
		{
			name: "missing error_topic",
			config: &ConverterConfig{
				ConverterID: "test-conv",
				TenantID:    "test-tenant",
				InputTopic:  "test.input",
			},
			wantErr: true,
			errMsg:  "error_topic",
		},
		{
			name: "invalid input_topic (special chars)",
			config: &ConverterConfig{
				ConverterID: "test-conv",
				TenantID:    "test-tenant",
				InputTopic:  "test@invalid",
				ErrorTopic:  "test.error",
			},
			wantErr: true,
			errMsg:  "invalid topic name",
		},
		{
			name: "auto-generates output_topic",
			config: &ConverterConfig{
				ConverterID: "test-conv",
				TenantID:    "test-tenant",
				InputTopic:  "test.input",
				ErrorTopic:  "test.error",
			},
			wantErr: false,
		},
		{
			name: "invalid max_retries (too high)",
			config: &ConverterConfig{
				ConverterID: "test-conv",
				TenantID:    "test-tenant",
				InputTopic:  "test.input",
				ErrorTopic:  "test.error",
				MaxRetries:  15,
			},
			wantErr: true,
			errMsg:  "max_retries",
		},
		{
			name: "invalid error_handling strategy",
			config: &ConverterConfig{
				ConverterID: "test-conv",
				TenantID:    "test-tenant",
				InputTopic:  "test.input",
				ErrorTopic:  "test.error",
				ErrorHandling: ErrorHandlingConfig{
					MissingFields: "invalid_strategy",
				},
			},
			wantErr: true,
			errMsg:  "missing_fields",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errMsg != "" && err != nil {
				if fmt.Sprint(err) != "" && !containsString(fmt.Sprint(err), tt.errMsg) {
					t.Errorf("ValidateConfig() error message should contain %q, got %v", tt.errMsg, err)
				}
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		serverSetup func() *httptest.Server
		wantErr     bool
		errType     error
	}{
		{
			name: "successful load",
			serverSetup: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/yaml")
					fmt.Fprint(w, `converter_id: test-conv
tenant_id: test-tenant
input_topic: test.input
error_topic: test.error
max_retries: 3
retry_backoff: exponential
error_handling:
  missing_fields: fail
  type_mismatch: fail
  validation_error: fail
`)
				}))
			},
			wantErr: false,
		},
		{
			name: "config not found",
			serverSetup: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			wantErr: true,
		},
		{
			name: "server error",
			serverSetup: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprint(w, "server error")
				}))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.serverSetup()
			defer server.Close()

			// Mock config service URL
			t.Setenv("CONFIG_SERVICE_URL", server.URL)

			logger := SetupLogger("info")
			config, err := LoadConfig(context.Background(), "test-tenant", "test-conv", logger)

			if (err != nil) != tt.wantErr {
				t.Errorf("LoadConfig() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && config == nil {
				t.Errorf("LoadConfig() expected config, got nil")
			}
		})
	}
}

func TestGetConfigServiceEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     string
	}{
		{
			name:     "with URL",
			envValue: "http://config-service:8080",
			want:     "http://config-service:8080",
		},
		{
			name:     "with trailing slash",
			envValue: "http://config-service:8080/",
			want:     "http://config-service:8080",
		},
		{
			name:     "empty",
			envValue: "",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CONFIG_SERVICE_URL", tt.envValue)
			got := GetConfigServiceEndpoint()
			if got != tt.want {
				t.Errorf("GetConfigServiceEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Helper function
func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
