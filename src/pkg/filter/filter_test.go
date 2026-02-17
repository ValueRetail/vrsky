package filter

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// TestNewFilter tests filter creation
func TestNewFilter(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		config  *Config
		conn    *nats.Conn
		logger  *slog.Logger
		metrics prometheus.Registerer
		wantErr bool
	}{
		{
			"valid filter creation",
			"test-filter",
			&Config{
				FilterID:       "test",
				InputTopic:     "input",
				OutputTopic:    "output",
				RejectionTopic: "rejection",
				Rules:          []interface{}{},
			},
			nil,
			slog.Default(),
			prometheus.DefaultRegisterer,
			true, // Will fail because natsConn is nil
		},
		{
			"empty filter id",
			"",
			&Config{
				FilterID:       "test",
				InputTopic:     "input",
				OutputTopic:    "output",
				RejectionTopic: "rejection",
				Rules:          []interface{}{},
			},
			nil,
			slog.Default(),
			prometheus.DefaultRegisterer,
			true,
		},
		{
			"nil config",
			"test-filter",
			nil,
			nil,
			slog.Default(),
			prometheus.DefaultRegisterer,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewFilter(tt.id, tt.config, tt.conn, tt.logger, tt.metrics)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewFilter() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestFilterInterface tests implementation of Filter interface
func TestFilterInterface(t *testing.T) {
	// Create a mock filter with minimal setup
	config := &Config{
		FilterID:       "test",
		InputTopic:     "input",
		OutputTopic:    "output",
		RejectionTopic: "rejection",
		Rules:          []interface{}{},
	}

	registry := prometheus.NewRegistry()
	f := &FilterImpl{
		id:      "test-filter",
		config:  config,
		logger:  slog.Default(),
		health:  "stopped",
		metrics: NewFilterMetrics("test-filter", registry),
	}

	// Test Name()
	name := f.Name()
	if name != "Filter/test-filter" {
		t.Errorf("Name() = %q, want %q", name, "Filter/test-filter")
	}

	// Test Type()
	compType := f.Type()
	if compType != "filter" {
		t.Errorf("Type() = %q, want %q", compType, "filter")
	}

	// Test Version()
	version := f.Version()
	if version != "1.0.0" {
		t.Errorf("Version() = %q, want %q", version, "1.0.0")
	}
}

// TestProcessMessage tests message processing logic
func TestProcessMessage(t *testing.T) {
	tests := []struct {
		name       string
		payload    map[string]interface{}
		rules      []*Rule
		wantAction Action
		wantErr    bool
	}{
		{
			"accept when rule matches",
			map[string]interface{}{"status": "active"},
			[]*Rule{
				{
					ID:   "rule_1",
					Name: "Accept active",
					Condition: &Condition{
						Operator: "==",
						Field:    "status",
						Value:    "active",
					},
				},
			},
			ActionAccept,
			false,
		},
		{
			"reject when no rule matches",
			map[string]interface{}{"status": "inactive"},
			[]*Rule{
				{
					ID:   "rule_1",
					Name: "Accept active",
					Condition: &Condition{
						Operator: "==",
						Field:    "status",
						Value:    "active",
					},
				},
			},
			ActionReject,
			false,
		},
		{
			"accept when no condition",
			map[string]interface{}{"status": "anything"},
			[]*Rule{
				{
					ID:        "rule_1",
					Name:      "Accept all",
					Condition: nil,
				},
			},
			ActionAccept,
			false,
		},
		{
			"reject invalid json",
			nil,
			[]*Rule{},
			ActionReject,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condEngine := NewConditionEngine()
			registry := prometheus.NewRegistry() // Use new registry for each test
			f := &FilterImpl{
				id:              "test",
				config:          &Config{},
				rules:           tt.rules,
				logger:          slog.Default(),
				metrics:         NewFilterMetrics("test", registry),
				conditionEngine: condEngine,
				schemaValidator: &SchemaValidator{mode: "strict"},
			}

			// Create envelope with payload
			var payloadBytes []byte
			if tt.payload != nil {
				var err error
				payloadBytes, err = json.Marshal(tt.payload)
				if err != nil {
					t.Fatalf("failed to marshal payload: %v", err)
				}
			} else {
				payloadBytes = []byte("invalid json {")
			}

			env := &envelope.Envelope{
				ID:      "test-env",
				Payload: payloadBytes,
			}

			decision, err := f.ProcessMessage(context.Background(), env)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProcessMessage() error = %v, wantErr %v", err, tt.wantErr)
			}

			if decision != nil && decision.Action != tt.wantAction {
				t.Errorf("ProcessMessage() action = %v, want %v", decision.Action, tt.wantAction)
			}
		})
	}
}

// TestEvaluateRule tests rule evaluation logic
func TestEvaluateRule(t *testing.T) {
	tests := []struct {
		name      string
		rule      *Rule
		payload   interface{}
		wantMatch bool
		wantErr   bool
	}{
		{
			"condition evaluates to true",
			&Rule{
				ID:   "rule_1",
				Name: "Check status",
				Condition: &Condition{
					Operator: "==",
					Field:    "status",
					Value:    "active",
				},
			},
			map[string]interface{}{"status": "active"},
			true,
			false,
		},
		{
			"condition evaluates to false",
			&Rule{
				ID:   "rule_1",
				Name: "Check status",
				Condition: &Condition{
					Operator: "==",
					Field:    "status",
					Value:    "active",
				},
			},
			map[string]interface{}{"status": "inactive"},
			false,
			false,
		},
		{
			"no condition returns true",
			&Rule{
				ID:        "rule_1",
				Name:      "Accept all",
				Condition: nil,
			},
			map[string]interface{}{"status": "anything"},
			true,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condEngine := NewConditionEngine()
			f := &FilterImpl{
				id:              "test",
				logger:          slog.Default(),
				conditionEngine: condEngine,
				schemaValidator: &SchemaValidator{mode: "strict"},
			}

			matches, err := f.evaluateRule(context.Background(), tt.rule, tt.payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("evaluateRule() error = %v, wantErr %v", err, tt.wantErr)
			}

			if matches != tt.wantMatch {
				t.Errorf("evaluateRule() matches = %v, want %v", matches, tt.wantMatch)
			}
		})
	}
}

// TestParseRules tests raw rule parsing
func TestParseRules(t *testing.T) {
	tests := []struct {
		name    string
		raw     []interface{}
		count   int
		wantErr bool
	}{
		{
			"single rule",
			[]interface{}{
				map[interface{}]interface{}{
					"name":      "rule1",
					"schema_id": "schema1",
				},
			},
			1,
			false,
		},
		{
			"multiple rules",
			[]interface{}{
				map[interface{}]interface{}{"name": "rule1"},
				map[interface{}]interface{}{"name": "rule2"},
				map[interface{}]interface{}{"name": "rule3"},
			},
			3,
			false,
		},
		{
			"rule with condition",
			[]interface{}{
				map[interface{}]interface{}{
					"name": "rule1",
					"condition": map[interface{}]interface{}{
						"operator": "==",
						"field":    "status",
						"value":    "active",
					},
				},
			},
			1,
			false,
		},
		{
			"invalid rule (not a map)",
			[]interface{}{
				"not a map",
			},
			0,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, err := parseRules(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRules() error = %v, wantErr %v", err, tt.wantErr)
			}

			if len(rules) != tt.count {
				t.Errorf("parseRules() returned %d rules, want %d", len(rules), tt.count)
			}
		})
	}
}

// TestParseCondition tests condition parsing
func TestParseCondition(t *testing.T) {
	tests := []struct {
		name         string
		raw          map[interface{}]interface{}
		wantOperator string
		wantField    string
		wantValue    interface{}
	}{
		{
			"simple condition",
			map[interface{}]interface{}{
				"operator": "==",
				"field":    "status",
				"value":    "active",
			},
			"==",
			"status",
			"active",
		},
		{
			"condition with missing fields",
			map[interface{}]interface{}{
				"operator": "contains",
			},
			"contains",
			"",
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond := parseCondition(tt.raw)
			if cond.Operator != tt.wantOperator {
				t.Errorf("operator = %q, want %q", cond.Operator, tt.wantOperator)
			}
			if cond.Field != tt.wantField {
				t.Errorf("field = %q, want %q", cond.Field, tt.wantField)
			}
		})
	}
}

// TestGetString tests safe string extraction
func TestGetString(t *testing.T) {
	tests := []struct {
		name       string
		m          map[interface{}]interface{}
		key        string
		defaultVal string
		want       string
	}{
		{
			"existing string key",
			map[interface{}]interface{}{"name": "test"},
			"name",
			"default",
			"test",
		},
		{
			"missing key",
			map[interface{}]interface{}{"other": "value"},
			"name",
			"default",
			"default",
		},
		{
			"non-string value",
			map[interface{}]interface{}{"name": 123},
			"name",
			"default",
			"default",
		},
		{
			"empty map",
			map[interface{}]interface{}{},
			"name",
			"default",
			"default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getString(tt.m, tt.key, tt.defaultVal)
			if result != tt.want {
				t.Errorf("getString() = %q, want %q", result, tt.want)
			}
		})
	}
}

// TestFilterConcurrency tests concurrent message processing
func TestFilterConcurrency(t *testing.T) {
	condEngine := NewConditionEngine()
	registry := prometheus.NewRegistry()
	f := &FilterImpl{
		id:              "test",
		config:          &Config{},
		rules:           []*Rule{},
		logger:          slog.Default(),
		metrics:         NewFilterMetrics("test", registry),
		conditionEngine: condEngine,
		schemaValidator: &SchemaValidator{mode: "strict"},
	}

	// Create multiple goroutines processing messages
	var wg sync.WaitGroup
	numGoroutines := 10
	messagesPerGoroutine := 5

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerGoroutine; j++ {
				payload := map[string]interface{}{"id": id, "seq": j}
				payloadBytes, _ := json.Marshal(payload)
				env := &envelope.Envelope{
					ID:      "test-env",
					Payload: payloadBytes,
				}

				_, _ = f.ProcessMessage(context.Background(), env)
			}
		}(i)
	}

	wg.Wait()
	// If we get here without panic, concurrency handling is working
}

// TestHealthStatus tests health status transitions
func TestHealthStatus(t *testing.T) {
	f := &FilterImpl{
		id:     "test",
		config: &Config{},
		logger: slog.Default(),
		health: "stopped",
	}

	// Check initial health
	if status := f.Health(); status != "stopped" {
		t.Errorf("Health() = %q, want %q", status, "stopped")
	}

	// Simulate starting
	f.mu.Lock()
	f.health = "healthy"
	f.mu.Unlock()

	if status := f.Health(); status != "healthy" {
		t.Errorf("Health() = %q, want %q", status, "healthy")
	}
}

// TestStop tests graceful shutdown
func TestStop(t *testing.T) {
	f := &FilterImpl{
		id:     "test",
		config: &Config{},
		logger: slog.Default(),
		closed: false,
		health: "healthy",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Create a context for the filter
	f.ctx, f.cancel = context.WithCancel(context.Background())

	err := f.Stop(ctx)
	if err != nil {
		t.Errorf("Stop() error = %v", err)
	}

	if !f.closed {
		t.Errorf("Stop() did not mark filter as closed")
	}
}

// BenchmarkProcessMessage benchmarks message processing
func BenchmarkProcessMessage(b *testing.B) {
	condEngine := NewConditionEngine()
	registry := prometheus.NewRegistry()
	f := &FilterImpl{
		id:      "bench",
		config:  &Config{},
		rules:   []*Rule{},
		logger:  slog.Default(),
		metrics: NewFilterMetrics("bench", registry),

		conditionEngine: condEngine,
		schemaValidator: &SchemaValidator{mode: "strict"},
	}

	payload := map[string]interface{}{
		"id":     "test",
		"status": "active",
		"data":   "benchmark data",
	}
	payloadBytes, _ := json.Marshal(payload)

	env := &envelope.Envelope{
		ID:      "bench-env",
		Payload: payloadBytes,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = f.ProcessMessage(context.Background(), env)
	}
}
