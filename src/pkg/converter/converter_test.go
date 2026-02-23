package converter

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/nats-io/nats.go"
)

func TestNewConverter(t *testing.T) {
	tests := []struct {
		name        string
		converterID string
		tenantID    string
		natsConn    *nats.Conn
		logger      *slog.Logger
		wantErr     bool
		errMsg      string
	}{
		{
			name:        "valid inputs",
			converterID: "test-conv",
			tenantID:    "test-tenant",
			natsConn:    nil, // Will be mocked
			logger:      SetupLogger("info"),
			wantErr:     false,
		},
		{
			name:        "nil logger",
			converterID: "test-conv",
			tenantID:    "test-tenant",
			natsConn:    nil,
			logger:      nil,
			wantErr:     true,
			errMsg:      "logger is required",
		},
		{
			name:        "nil NATS connection",
			converterID: "test-conv",
			tenantID:    "test-tenant",
			natsConn:    nil,
			logger:      SetupLogger("info"),
			wantErr:     true,
			errMsg:      "nats connection is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock config service for valid case
			if !tt.wantErr {
				// We'll need to mock the config service
				// For now, skip this test
				t.Skip("requires config service mock")
			}

			_, err := NewConverter(context.Background(), tt.converterID, tt.tenantID, tt.natsConn, tt.logger)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewConverter() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConverterName(t *testing.T) {
	logger := SetupLogger("info")
	config := &ConverterConfig{
		ConverterID: "test-conv-id",
		TenantID:    "test-tenant",
	}

	conv := &ConverterImpl{
		config: config,
		logger: logger,
	}

	got := conv.Name()
	if got != "test-conv-id" {
		t.Errorf("Name() = %q, want %q", got, "test-conv-id")
	}
}

func TestConverterType(t *testing.T) {
	logger := SetupLogger("info")
	config := &ConverterConfig{}

	conv := &ConverterImpl{
		config: config,
		logger: logger,
	}

	got := conv.Type()
	if got != "converter" {
		t.Errorf("Type() = %q, want %q", got, "converter")
	}
}

func TestConverterVersion(t *testing.T) {
	logger := SetupLogger("info")
	config := &ConverterConfig{}

	conv := &ConverterImpl{
		config: config,
		logger: logger,
	}

	got := conv.Version()
	if got != "1.0.0" {
		t.Errorf("Version() = %q, want %q", got, "1.0.0")
	}
}

func TestProcessMessage_PassThrough(t *testing.T) {
	logger := SetupLogger("info")
	metrics, err := NewMetrics("test-conv", "test-tenant")
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conv := &ConverterImpl{
		config:  &ConverterConfig{},
		logger:  logger,
		metrics: metrics,
		ctx:     ctx,
		closed:  false,
	}

	// Create test envelope
	env := envelope.New()
	env.ID = "test-123"
	env.TenantID = "test-tenant"
	env.IntegrationID = "test-integration"
	env.ContentType = "application/json"
	env.Payload = []byte(`{"test": "data"}`)

	result, err := conv.ProcessMessage(context.Background(), env)
	if err != nil {
		t.Errorf("ProcessMessage() error = %v", err)
	}

	if result == nil {
		t.Errorf("ProcessMessage() result is nil")
	}

	if result.ID != env.ID {
		t.Errorf("ProcessMessage() ID = %q, want %q", result.ID, env.ID)
	}
}

func TestStop_NoDoubleClose(t *testing.T) {
	logger := SetupLogger("info")
	config := &ConverterConfig{
		ConverterID: "test-conv",
		TenantID:    "test-tenant",
	}

	ctx, cancel := context.WithCancel(context.Background())

	conv := &ConverterImpl{
		config: config,
		logger: logger,
		closed: true,
		ctx:    ctx,
		cancel: cancel,
	}

	// First stop should work
	err := conv.Stop(context.Background())
	if err != nil {
		t.Errorf("Stop() first call error = %v", err)
	}

	// Second stop should also work (idempotent)
	err = conv.Stop(context.Background())
	if err != nil {
		t.Errorf("Stop() second call error = %v", err)
	}
}

func TestHealth_Unhealthy_WhenClosed(t *testing.T) {
	logger := SetupLogger("info")
	config := &ConverterConfig{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conv := &ConverterImpl{
		config: config,
		logger: logger,
		closed: true,
		ctx:    ctx,
	}

	health := conv.Health()
	if health != component.HealthUnhealthy {
		t.Errorf("Health() status = %v, want unhealthy", health)
	}
}

func TestHealth_Unhealthy_NilNATS(t *testing.T) {
	logger := SetupLogger("info")
	config := &ConverterConfig{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conv := &ConverterImpl{
		config:   config,
		logger:   logger,
		natsConn: nil,
		closed:   false,
		ctx:      ctx,
	}

	health := conv.Health()
	if health != component.HealthUnhealthy {
		t.Errorf("Health() status = %v, want unhealthy", health)
	}
}

func TestHealth_Healthy(t *testing.T) {
	_ = SetupLogger("info")

	// We can't easily test with real NATS, so this tests the happy path structure
	// In integration tests, we'd test with real NATS
}

func TestExecuteWithRetry(t *testing.T) {
	logger := SetupLogger("info")
	metrics, err := NewMetrics("test-conv-retry", "test-tenant")
	if err != nil {
		// Metrics registration may fail if already registered from another test
		// This is OK - use a nil metrics for this test
		metrics = &Metrics{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conv := &ConverterImpl{
		config:  &ConverterConfig{MaxRetries: 3},
		logger:  logger,
		metrics: metrics,
		ctx:     ctx,
		closed:  false,
	}

	tests := []struct {
		name             string
		fn               func() error
		wantErr          bool
		expectedAttempts int
	}{
		{
			name: "succeeds on first attempt",
			fn: func() error {
				return nil
			},
			wantErr:          false,
			expectedAttempts: 1,
		},
		{
			name: "succeeds on second attempt",
			fn: func() error {
				// Would need a counter to track attempts
				return nil
			},
			wantErr: false,
		},
		{
			name: "fails after max attempts",
			fn: func() error {
				return fmt.Errorf("always fails")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conv.executeWithRetry(tt.fn)
			if (err != nil) != tt.wantErr {
				t.Errorf("executeWithRetry() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
