package converter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadConfig fetches converter configuration from the config service.
// It implements 3-attempt retry with exponential backoff (100ms → 1s → 10s).
// Total timeout is 30 seconds.
func LoadConfig(ctx context.Context, tenantID, converterID string, logger *slog.Logger) (*ConverterConfig, error) {
	if tenantID == "" || converterID == "" {
		return nil, fmt.Errorf("tenant_id and converter_id are required")
	}

	// Get config service endpoint from environment
	endpoint := GetConfigServiceEndpoint()
	if endpoint == "" {
		return nil, fmt.Errorf("CONFIG_SERVICE_URL environment variable not set")
	}

	// Construct URL
	url := fmt.Sprintf("%s/api/v1/config/converters/%s/%s", endpoint, tenantID, converterID)

	// Retry logic: 3 attempts with exponential backoff
	const maxAttempts = 3
	const timeout = 30 * time.Second

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Create reusable HTTP client with proper connection pooling
	// This prevents goroutine leaks from creating new clients per attempt
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}
	defer client.CloseIdleConnections()

	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Wait before retry (exponential backoff: 100ms, 1s, 10s)
		if attempt > 1 {
			var backoff time.Duration
			switch attempt {
			case 2:
				backoff = 100 * time.Millisecond
			case 3:
				backoff = 1 * time.Second
			default:
				backoff = 10 * time.Second
			}
			logger.WarnContext(ctx, "Retrying config fetch", "attempt", attempt, "backoff", backoff.String())
			time.Sleep(backoff)
		}

		// Fetch config with the reusable client
		config, err := fetchConfigFromService(ctx, url, client, logger)
		if err == nil {
			logger.InfoContext(ctx, "Config loaded successfully", "tenant_id", tenantID, "converter_id", converterID)
			return config, nil
		}

		lastErr = err
		logger.WarnContext(ctx, "Config fetch failed", "attempt", attempt, "error", err.Error())
	}

	return nil, fmt.Errorf("failed to load config after %d attempts: %w", maxAttempts, lastErr)
}

// fetchConfigFromService fetches config from a single endpoint using a reusable client
func fetchConfigFromService(ctx context.Context, url string, client *http.Client, logger *slog.Logger) (*ConverterConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/yaml")
	req.Header.Set("Content-Type", "application/yaml")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch config: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Check HTTP status
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrConfigNotFound
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	// Parse YAML
	config := &ConverterConfig{}
	if err := yaml.Unmarshal(body, config); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	// Validate config
	if err := ValidateConfig(config); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return config, nil
}

// ValidateConfig validates a converter configuration
func ValidateConfig(config *ConverterConfig) error {
	if config == nil {
		return &ConfigError{Message: "config is nil", Cause: ErrConfigInvalid}
	}

	// Check required fields
	if config.ConverterID == "" {
		return &ConfigError{Field: "converter_id", Message: "required", Cause: ErrConfigInvalid}
	}

	if config.TenantID == "" {
		return &ConfigError{Field: "tenant_id", Message: "required", Cause: ErrConfigInvalid}
	}

	if config.InputTopic == "" {
		return &ConfigError{Field: "input_topic", Message: "required", Cause: ErrConfigInvalid}
	}

	if config.ErrorTopic == "" {
		return &ConfigError{Field: "error_topic", Message: "required", Cause: ErrConfigInvalid}
	}

	// Validate topic names (alphanumeric, dots, underscores, hyphens)
	topicRegex := regexp.MustCompile(`^[a-zA-Z0-9._\-]+$`)
	if !topicRegex.MatchString(config.InputTopic) {
		return &ConfigError{
			Field:   "input_topic",
			Message: "invalid topic name (only alphanumeric, dots, underscores, hyphens allowed)",
			Cause:   ErrConfigInvalid,
		}
	}

	if !topicRegex.MatchString(config.ErrorTopic) {
		return &ConfigError{
			Field:   "error_topic",
			Message: "invalid topic name",
			Cause:   ErrConfigInvalid,
		}
	}

	// Auto-generate output topic if not provided
	if config.OutputTopic == "" {
		config.OutputTopic = config.InputTopic + ".converted"
	}

	// Validate error handling values
	validStrategies := map[string]bool{"skip": true, "coerce": true, "fail": true}

	if config.ErrorHandling.MissingFields == "" {
		config.ErrorHandling.MissingFields = "fail" // default
	} else if !validStrategies[config.ErrorHandling.MissingFields] {
		return &ConfigError{
			Field:   "error_handling.missing_fields",
			Message: "must be 'skip', 'coerce', or 'fail'",
			Cause:   ErrConfigInvalid,
		}
	}

	if config.ErrorHandling.TypeMismatch == "" {
		config.ErrorHandling.TypeMismatch = "fail" // default
	} else if !validStrategies[config.ErrorHandling.TypeMismatch] {
		return &ConfigError{
			Field:   "error_handling.type_mismatch",
			Message: "must be 'skip', 'coerce', or 'fail'",
			Cause:   ErrConfigInvalid,
		}
	}

	if config.ErrorHandling.ValidationError == "" {
		config.ErrorHandling.ValidationError = "fail" // default
	} else if !validStrategies[config.ErrorHandling.ValidationError] {
		return &ConfigError{
			Field:   "error_handling.validation_error",
			Message: "must be 'skip', 'coerce', or 'fail'",
			Cause:   ErrConfigInvalid,
		}
	}

	// Validate max retries
	if config.MaxRetries == 0 {
		config.MaxRetries = 3 // default
	} else if config.MaxRetries < 1 || config.MaxRetries > 10 {
		return &ConfigError{
			Field:   "max_retries",
			Message: "must be between 1 and 10",
			Cause:   ErrConfigInvalid,
		}
	}

	// Validate retry backoff
	if config.RetryBackoff == "" {
		config.RetryBackoff = "exponential" // default
	} else if config.RetryBackoff != "exponential" {
		return &ConfigError{
			Field:   "retry_backoff",
			Message: "must be 'exponential'",
			Cause:   ErrConfigInvalid,
		}
	}

	return nil
}

// GetConfigServiceEndpoint returns the config service endpoint URL from environment
func GetConfigServiceEndpoint() string {
	endpoint := os.Getenv("CONFIG_SERVICE_URL")
	// Remove trailing slashes
	endpoint = strings.TrimSuffix(endpoint, "/")
	return endpoint
}

// ConfigToYAML converts a ConverterConfig to YAML for testing
func ConfigToYAML(config *ConverterConfig) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(config); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// YAMLToConfig converts YAML bytes to ConverterConfig for testing
func YAMLToConfig(data []byte) (*ConverterConfig, error) {
	config := &ConverterConfig{}
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, err
	}
	return config, nil
}
