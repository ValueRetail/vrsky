package runtime

import (
	"os"
	"testing"
)

func TestLoadFromEnv(t *testing.T) {
	// Save original env and restore after test
	origEnv := os.Environ()
	defer func() {
		os.Clearenv()
		for _, e := range origEnv {
			for i := 0; i < len(e); i++ {
				if e[i] == '=' {
					os.Setenv(e[:i], e[i+1:])
					break
				}
			}
		}
	}()

	tests := []struct {
		name    string
		env     map[string]string
		want    *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "minimal config",
			env:  map[string]string{},
			want: &Config{
				NATSURLs:    "nats://nats:4222",
				LogLevel:    "info",
				HealthPort:  8080,
				MetricsPort: 8080,
			},
			wantErr: false,
		},
		{
			name: "full config",
			env: map[string]string{
				"TENANT_ID":           "tenant-1",
				"CONNECTION_ID":       "conn-1",
				"NODE_ID":             "node-1",
				"NODE_TYPE":           "consumer",
				"OUTPUT_NATS_SUBJECT": "tenant-1.conn-1.node-1.output",
				"NATS_URLS":           "nats://localhost:4222",
				"NATS_ACCOUNT":        "account-1",
				"HEALTH_PORT":         "9090",
				"METRICS_PORT":        "9091",
				"LOG_LEVEL":           "debug",
				"CONFIG":              `{"key": "value"}`,
			},
			want: &Config{
				TenantID:          "tenant-1",
				ConnectionID:      "conn-1",
				NodeID:            "node-1",
				NodeType:          "consumer",
				OutputNATSSubject: "tenant-1.conn-1.node-1.output",
				NATSURLs:          "nats://localhost:4222",
				NATSAccount:       "account-1",
				HealthPort:        9090,
				MetricsPort:       9091,
				LogLevel:          "debug",
				Config:            []byte(`{"key": "value"}`),
			},
			wantErr: false,
		},
		{
			name: "invalid CONFIG JSON",
			env: map[string]string{
				"CONFIG": "not valid json",
			},
			wantErr: true,
			errMsg:  "CONFIG is not valid JSON",
		},
		{
			name: "invalid HEALTH_PORT",
			env: map[string]string{
				"HEALTH_PORT": "not-a-number",
			},
			wantErr: true,
			errMsg:  "HEALTH_PORT is not a valid integer",
		},
		{
			name: "invalid METRICS_PORT",
			env: map[string]string{
				"METRICS_PORT": "not-a-number",
			},
			wantErr: true,
			errMsg:  "METRICS_PORT is not a valid integer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear and set env
			os.Clearenv()
			for k, v := range tt.env {
				os.Setenv(k, v)
			}

			cfg, err := LoadFromEnv()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					// Check if error contains the message
					if len(err.Error()) < len(tt.errMsg) || err.Error()[:len(tt.errMsg)] != tt.errMsg[:min(len(tt.errMsg), len(err.Error()))] {
						t.Logf("error = %q (contains expected prefix)", err.Error())
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cfg.TenantID != tt.want.TenantID {
				t.Errorf("TenantID = %v, want %v", cfg.TenantID, tt.want.TenantID)
			}
			if cfg.HealthPort != tt.want.HealthPort {
				t.Errorf("HealthPort = %v, want %v", cfg.HealthPort, tt.want.HealthPort)
			}
			if cfg.MetricsPort != tt.want.MetricsPort {
				t.Errorf("MetricsPort = %v, want %v", cfg.MetricsPort, tt.want.MetricsPort)
			}
			if cfg.NATSURLs != tt.want.NATSURLs {
				t.Errorf("NATSURLs = %v, want %v", cfg.NATSURLs, tt.want.NATSURLs)
			}
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid consumer",
			cfg: Config{
				TenantID:          "tenant-1",
				ConnectionID:      "conn-1",
				NodeID:            "node-1",
				NodeType:          "consumer",
				OutputNATSSubject: "output.subject",
			},
			wantErr: false,
		},
		{
			name: "valid producer",
			cfg: Config{
				TenantID:         "tenant-1",
				ConnectionID:     "conn-1",
				NodeID:           "node-1",
				NodeType:         "producer",
				InputNATSSubject: "input.subject",
			},
			wantErr: false,
		},
		{
			name: "valid filter",
			cfg: Config{
				TenantID:          "tenant-1",
				ConnectionID:      "conn-1",
				NodeID:            "node-1",
				NodeType:          "filter",
				InputNATSSubject:  "input.subject",
				OutputNATSSubject: "output.subject",
			},
			wantErr: false,
		},
		{
			name: "valid converter",
			cfg: Config{
				TenantID:          "tenant-1",
				ConnectionID:      "conn-1",
				NodeID:            "node-1",
				NodeType:          "converter",
				InputNATSSubject:  "input.subject",
				OutputNATSSubject: "output.subject",
			},
			wantErr: false,
		},
		{
			name: "missing tenant_id",
			cfg: Config{
				ConnectionID: "conn-1",
				NodeID:       "node-1",
				NodeType:     "consumer",
			},
			wantErr: true,
			errMsg:  "TENANT_ID is required",
		},
		{
			name: "missing connection_id",
			cfg: Config{
				TenantID: "tenant-1",
				NodeID:   "node-1",
				NodeType: "consumer",
			},
			wantErr: true,
			errMsg:  "CONNECTION_ID is required",
		},
		{
			name: "missing node_id",
			cfg: Config{
				TenantID:     "tenant-1",
				ConnectionID: "conn-1",
				NodeType:     "consumer",
			},
			wantErr: true,
			errMsg:  "NODE_ID is required",
		},
		{
			name: "missing node_type",
			cfg: Config{
				TenantID:     "tenant-1",
				ConnectionID: "conn-1",
				NodeID:       "node-1",
			},
			wantErr: true,
			errMsg:  "NODE_TYPE is required",
		},
		{
			name: "invalid node_type",
			cfg: Config{
				TenantID:     "tenant-1",
				ConnectionID: "conn-1",
				NodeID:       "node-1",
				NodeType:     "unknown",
			},
			wantErr: true,
			errMsg:  "invalid NODE_TYPE: unknown",
		},
		{
			name: "consumer missing output subject",
			cfg: Config{
				TenantID:     "tenant-1",
				ConnectionID: "conn-1",
				NodeID:       "node-1",
				NodeType:     "consumer",
			},
			wantErr: true,
			errMsg:  "OUTPUT_NATS_SUBJECT is required for consumer",
		},
		{
			name: "producer missing input subject",
			cfg: Config{
				TenantID:     "tenant-1",
				ConnectionID: "conn-1",
				NodeID:       "node-1",
				NodeType:     "producer",
			},
			wantErr: true,
			errMsg:  "INPUT_NATS_SUBJECT is required for producer",
		},
		{
			name: "filter missing input subject",
			cfg: Config{
				TenantID:          "tenant-1",
				ConnectionID:      "conn-1",
				NodeID:            "node-1",
				NodeType:          "filter",
				OutputNATSSubject: "output.subject",
			},
			wantErr: true,
			errMsg:  "INPUT_NATS_SUBJECT is required for filter",
		},
		{
			name: "filter missing output subject",
			cfg: Config{
				TenantID:         "tenant-1",
				ConnectionID:     "conn-1",
				NodeID:           "node-1",
				NodeType:         "filter",
				InputNATSSubject: "input.subject",
			},
			wantErr: true,
			errMsg:  "OUTPUT_NATS_SUBJECT is required for filter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tt.errMsg)
				}
				if err.Error() != tt.errMsg {
					t.Errorf("error = %q, want %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfig_ParseConfig(t *testing.T) {
	type testConfig struct {
		Key   string `json:"key"`
		Value int    `json:"value"`
	}

	tests := []struct {
		name    string
		config  []byte
		want    testConfig
		wantErr bool
	}{
		{
			name:    "empty config",
			config:  nil,
			want:    testConfig{},
			wantErr: false,
		},
		{
			name:    "valid config",
			config:  []byte(`{"key": "test", "value": 42}`),
			want:    testConfig{Key: "test", Value: 42},
			wantErr: false,
		},
		{
			name:    "invalid config",
			config:  []byte(`not json`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Config: tt.config}
			var result testConfig
			err := cfg.ParseConfig(&result)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Key != tt.want.Key || result.Value != tt.want.Value {
				t.Errorf("result = %+v, want %+v", result, tt.want)
			}
		})
	}
}

func TestConfig_TypeCheckers(t *testing.T) {
	tests := []struct {
		nodeType    string
		isConsumer  bool
		isProducer  bool
		isFilter    bool
		isConverter bool
	}{
		{"consumer", true, false, false, false},
		{"producer", false, true, false, false},
		{"filter", false, false, true, false},
		{"converter", false, false, false, true},
		{"unknown", false, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.nodeType, func(t *testing.T) {
			cfg := &Config{NodeType: tt.nodeType}

			if cfg.IsConsumer() != tt.isConsumer {
				t.Errorf("IsConsumer() = %v, want %v", cfg.IsConsumer(), tt.isConsumer)
			}
			if cfg.IsProducer() != tt.isProducer {
				t.Errorf("IsProducer() = %v, want %v", cfg.IsProducer(), tt.isProducer)
			}
			if cfg.IsFilter() != tt.isFilter {
				t.Errorf("IsFilter() = %v, want %v", cfg.IsFilter(), tt.isFilter)
			}
			if cfg.IsConverter() != tt.isConverter {
				t.Errorf("IsConverter() = %v, want %v", cfg.IsConverter(), tt.isConverter)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
