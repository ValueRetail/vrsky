package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func newTestRegistry() prometheus.Registerer {
	return prometheus.NewRegistry()
}

func TestNewBase(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			cfg: Config{
				TenantID:     "tenant-1",
				ConnectionID: "conn-1",
				NodeID:       "node-1",
				NodeType:     TypeConsumer,
				Registerer:   newTestRegistry(),
			},
			wantErr: false,
		},
		{
			name: "missing tenant_id",
			cfg: Config{
				ConnectionID: "conn-1",
				NodeID:       "node-1",
				NodeType:     TypeConsumer,
				Registerer:   newTestRegistry(),
			},
			wantErr: true,
			errMsg:  "tenant_id is required",
		},
		{
			name: "missing connection_id",
			cfg: Config{
				TenantID:   "tenant-1",
				NodeID:     "node-1",
				NodeType:   TypeConsumer,
				Registerer: newTestRegistry(),
			},
			wantErr: true,
			errMsg:  "connection_id is required",
		},
		{
			name: "missing node_id",
			cfg: Config{
				TenantID:     "tenant-1",
				ConnectionID: "conn-1",
				NodeType:     TypeConsumer,
				Registerer:   newTestRegistry(),
			},
			wantErr: true,
			errMsg:  "node_id is required",
		},
		{
			name: "missing node_type",
			cfg: Config{
				TenantID:     "tenant-1",
				ConnectionID: "conn-1",
				NodeID:       "node-1",
				Registerer:   newTestRegistry(),
			},
			wantErr: true,
			errMsg:  "node_type is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewBase(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("error = %q, want %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m == nil {
				t.Fatal("expected non-nil Base")
			}
		})
	}
}

func TestBase_RecordReceived(t *testing.T) {
	m, err := NewBase(Config{
		TenantID:     "tenant-1",
		ConnectionID: "conn-1",
		NodeID:       "node-1",
		NodeType:     TypeConsumer,
		Registerer:   newTestRegistry(),
	})
	if err != nil {
		t.Fatalf("failed to create metrics: %v", err)
	}

	// Should not panic
	m.RecordReceived()
	m.RecordReceived()
	m.RecordReceived()
}

func TestBase_RecordProcessed(t *testing.T) {
	m, err := NewBase(Config{
		TenantID:     "tenant-1",
		ConnectionID: "conn-1",
		NodeID:       "node-1",
		NodeType:     TypeProducer,
		Registerer:   newTestRegistry(),
	})
	if err != nil {
		t.Fatalf("failed to create metrics: %v", err)
	}

	// Should not panic
	m.RecordProcessed()
}

func TestBase_RecordFailed(t *testing.T) {
	m, err := NewBase(Config{
		TenantID:     "tenant-1",
		ConnectionID: "conn-1",
		NodeID:       "node-1",
		NodeType:     TypeFilter,
		Registerer:   newTestRegistry(),
	})
	if err != nil {
		t.Fatalf("failed to create metrics: %v", err)
	}

	// Should not panic and accept different categories
	m.RecordFailed("parse_error")
	m.RecordFailed("validation_error")
	m.RecordFailed("timeout")
}

func TestBase_RecordDuration(t *testing.T) {
	m, err := NewBase(Config{
		TenantID:     "tenant-1",
		ConnectionID: "conn-1",
		NodeID:       "node-1",
		NodeType:     TypeConverter,
		Registerer:   newTestRegistry(),
	})
	if err != nil {
		t.Fatalf("failed to create metrics: %v", err)
	}

	// Should not panic
	m.RecordDuration(100 * time.Millisecond)
	m.RecordDuration(1 * time.Second)
}

func TestBase_RecordError(t *testing.T) {
	m, err := NewBase(Config{
		TenantID:     "tenant-1",
		ConnectionID: "conn-1",
		NodeID:       "node-1",
		NodeType:     TypeConsumer,
		Registerer:   newTestRegistry(),
	})
	if err != nil {
		t.Fatalf("failed to create metrics: %v", err)
	}

	// Should not panic
	m.RecordError("nats_connection")
	m.RecordError("database")
}

func TestBase_ObserveProcessing(t *testing.T) {
	m, err := NewBase(Config{
		TenantID:     "tenant-1",
		ConnectionID: "conn-1",
		NodeID:       "node-1",
		NodeType:     TypeConsumer,
		Registerer:   newTestRegistry(),
	})
	if err != nil {
		t.Fatalf("failed to create metrics: %v", err)
	}

	// Test successful processing
	start := time.Now().Add(-100 * time.Millisecond)
	m.ObserveProcessing(start, nil)

	// Test failed processing
	start = time.Now().Add(-50 * time.Millisecond)
	m.ObserveProcessing(start, errors.New("test error"))
}

func TestBase_Labels(t *testing.T) {
	m, err := NewBase(Config{
		TenantID:     "tenant-1",
		ConnectionID: "conn-1",
		NodeID:       "node-1",
		NodeType:     TypeConsumer,
		Registerer:   newTestRegistry(),
	})
	if err != nil {
		t.Fatalf("failed to create metrics: %v", err)
	}

	labels := m.Labels()
	if labels.TenantID != "tenant-1" {
		t.Errorf("TenantID = %v, want tenant-1", labels.TenantID)
	}
	if labels.ConnectionID != "conn-1" {
		t.Errorf("ConnectionID = %v, want conn-1", labels.ConnectionID)
	}
	if labels.NodeID != "node-1" {
		t.Errorf("NodeID = %v, want node-1", labels.NodeID)
	}
	if labels.NodeType != "consumer" {
		t.Errorf("NodeType = %v, want consumer", labels.NodeType)
	}
}

func TestComponentTypes(t *testing.T) {
	types := []ComponentType{TypeConsumer, TypeFilter, TypeConverter, TypeProducer}
	expected := []string{"consumer", "filter", "converter", "producer"}

	for i, ct := range types {
		if string(ct) != expected[i] {
			t.Errorf("ComponentType %v = %v, want %v", i, string(ct), expected[i])
		}
	}
}
