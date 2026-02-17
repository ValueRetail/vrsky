package filter

import (
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func TestRoutingEngine_EvaluateRules_SingleRule(t *testing.T) {
	tests := []struct {
		name           string
		rawRules       []interface{}
		payload        interface{}
		expectedRuleID string
		expectedTopic  string
		wantErr        bool
	}{
		{
			name: "simple_catch_all",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "catch_all",
					"name":         "Catch All",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.default",
					"priority":     1,
				},
			},
			payload:        map[string]interface{}{"type": "order"},
			expectedRuleID: "catch_all",
			expectedTopic:  "output.default",
			wantErr:        false,
		},
		{
			name: "condition_match_equals",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "premium",
					"condition":    map[interface{}]interface{}{"operator": "==", "field": "tier", "value": "premium"},
					"output_topic": "output.premium",
					"priority":     1,
				},
				map[interface{}]interface{}{
					"id":           "standard",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.standard",
					"priority":     2,
				},
			},
			payload:        map[string]interface{}{"tier": "premium"},
			expectedRuleID: "premium",
			expectedTopic:  "output.premium",
			wantErr:        false,
		},
		{
			name: "condition_no_match_fallback_to_catchall",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "premium",
					"condition":    map[interface{}]interface{}{"operator": "==", "field": "tier", "value": "premium"},
					"output_topic": "output.premium",
					"priority":     1,
				},
				map[interface{}]interface{}{
					"id":           "catch_all",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.default",
					"priority":     99,
				},
			},
			payload:        map[string]interface{}{"tier": "standard"},
			expectedRuleID: "catch_all",
			expectedTopic:  "output.default",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine, err := NewRoutingEngine(tt.rawRules, ce)
			if err != nil {
				t.Fatalf("NewRoutingEngine failed: %v", err)
			}

			decision, err := engine.EvaluateRules(tt.payload, map[string]interface{}{})
			if (err != nil) != tt.wantErr {
				t.Errorf("EvaluateRules error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if decision == nil {
				t.Errorf("EvaluateRules returned nil decision")
				return
			}

			if decision.RuleID != tt.expectedRuleID {
				t.Errorf("RuleID = %s, want %s", decision.RuleID, tt.expectedRuleID)
			}

			if decision.OutputTopic != tt.expectedTopic {
				t.Errorf("OutputTopic = %s, want %s", decision.OutputTopic, tt.expectedTopic)
			}
		})
	}
}

func TestRoutingEngine_PriorityOrdering(t *testing.T) {
	tests := []struct {
		name           string
		rawRules       []interface{}
		payload        interface{}
		expectedRuleID string
	}{
		{
			name: "priority_ordering_first_wins",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "rule_priority_10",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.first",
					"priority":     10,
				},
				map[interface{}]interface{}{
					"id":           "rule_priority_1",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.second",
					"priority":     1,
				},
			},
			payload:        map[string]interface{}{},
			expectedRuleID: "rule_priority_1", // Lower priority value should execute first
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine, err := NewRoutingEngine(tt.rawRules, ce)
			if err != nil {
				t.Fatalf("NewRoutingEngine failed: %v", err)
			}

			decision, err := engine.EvaluateRules(tt.payload, map[string]interface{}{})
			if err != nil {
				t.Errorf("EvaluateRules error = %v", err)
				return
			}

			if decision.RuleID != tt.expectedRuleID {
				t.Errorf("RuleID = %s, want %s", decision.RuleID, tt.expectedRuleID)
			}
		})
	}
}

func TestRoutingEngine_ComplexConditions(t *testing.T) {
	tests := []struct {
		name           string
		rawRules       []interface{}
		payload        interface{}
		expectedRuleID string
	}{
		{
			name: "nested_field_greater_than",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "high_value",
					"condition":    map[interface{}]interface{}{"operator": ">", "field": "order.amount", "value": 1000},
					"output_topic": "output.premium",
					"priority":     1,
				},
				map[interface{}]interface{}{
					"id":           "catch_all",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.standard",
					"priority":     99,
				},
			},
			payload:        map[string]interface{}{"order": map[string]interface{}{"amount": 1500}},
			expectedRuleID: "high_value",
		},
		{
			name: "string_contains",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "urgent",
					"condition":    map[interface{}]interface{}{"operator": "contains", "field": "tags", "value": "urgent"},
					"output_topic": "output.urgent",
					"priority":     1,
				},
				map[interface{}]interface{}{
					"id":           "catch_all",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.default",
					"priority":     99,
				},
			},
			payload:        map[string]interface{}{"tags": "urgent,important"},
			expectedRuleID: "urgent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine, err := NewRoutingEngine(tt.rawRules, ce)
			if err != nil {
				t.Fatalf("NewRoutingEngine failed: %v", err)
			}

			decision, err := engine.EvaluateRules(tt.payload, map[string]interface{}{})
			if err != nil {
				t.Errorf("EvaluateRules error = %v", err)
				return
			}

			if decision.RuleID != tt.expectedRuleID {
				t.Errorf("RuleID = %s, want %s", decision.RuleID, tt.expectedRuleID)
			}
		})
	}
}

func TestRoutingEngine_TransformationsParsing(t *testing.T) {
	tests := []struct {
		name               string
		rawRules           []interface{}
		expectedTransCount int
	}{
		{
			name: "single_transformation",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "with_trans",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.default",
					"transformations": []interface{}{
						map[interface{}]interface{}{"action": "add_field", "field": "routed_at", "value": "${now()}"},
					},
				},
			},
			expectedTransCount: 1,
		},
		{
			name: "multiple_transformations",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "with_trans",
					"condition":    map[interface{}]interface{}{"operator": "always"},
					"output_topic": "output.default",
					"transformations": []interface{}{
						map[interface{}]interface{}{"action": "add_field", "field": "routed_at", "value": "${now()}"},
						map[interface{}]interface{}{"action": "add_field", "field": "trace_id", "value": "${uuid()}"},
						map[interface{}]interface{}{"action": "remove_field", "field": "internal_id"},
					},
				},
			},
			expectedTransCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			engine, err := NewRoutingEngine(tt.rawRules, ce)
			if err != nil {
				t.Fatalf("NewRoutingEngine failed: %v", err)
			}

			decision, err := engine.EvaluateRules(map[string]interface{}{}, map[string]interface{}{})
			if err != nil {
				t.Errorf("EvaluateRules error = %v", err)
				return
			}

			if len(decision.Transformations) != tt.expectedTransCount {
				t.Errorf("Transformation count = %d, want %d", len(decision.Transformations), tt.expectedTransCount)
			}
		})
	}
}

func TestRoutingEngine_ValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		rawRules []interface{}
		wantErr  string
	}{
		{
			name: "missing_output_topic",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":        "bad_rule",
					"condition": map[interface{}]interface{}{"operator": "always"},
					"priority":  1,
				},
			},
			wantErr: "missing output_topic",
		},
		{
			name: "missing_condition",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "bad_rule",
					"output_topic": "output.default",
					"priority":     1,
				},
			},
			wantErr: "missing condition",
		},
		{
			name: "no_catch_all_rule",
			rawRules: []interface{}{
				map[interface{}]interface{}{
					"id":           "only_premium",
					"condition":    map[interface{}]interface{}{"operator": "==", "field": "tier", "value": "premium"},
					"output_topic": "output.premium",
					"priority":     1,
				},
			},
			wantErr: "must include at least one catch-all rule",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := NewConditionEngine()
			_, err := NewRoutingEngine(tt.rawRules, ce)
			if err == nil {
				t.Errorf("Expected error containing '%s', got nil", tt.wantErr)
				return
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("Error = %v, want to contain %s", err, tt.wantErr)
			}
		})
	}
}

func TestRoutingEngine_GetRules(t *testing.T) {
	rawRules := []interface{}{
		map[interface{}]interface{}{
			"id":           "rule1",
			"condition":    map[interface{}]interface{}{"operator": "always"},
			"output_topic": "output.default",
			"priority":     1,
		},
		map[interface{}]interface{}{
			"id":           "rule2",
			"condition":    map[interface{}]interface{}{"operator": "always"},
			"output_topic": "output.default",
			"priority":     2,
		},
	}

	ce := NewConditionEngine()
	engine, err := NewRoutingEngine(rawRules, ce)
	if err != nil {
		t.Fatalf("NewRoutingEngine failed: %v", err)
	}

	rules := engine.GetRules()
	if len(rules) != 2 {
		t.Errorf("GetRules count = %d, want 2", len(rules))
	}

	// Verify rules are sorted by priority
	if rules[0].Priority > rules[1].Priority {
		t.Errorf("Rules not sorted by priority")
	}
}

func TestApplyRoutingToEnvelope(t *testing.T) {
	tests := []struct {
		name     string
		envelope *envelope.Envelope
		decision *RoutingDecision
		filterID string
		wantErr  bool
		check    func(t *testing.T, env *envelope.Envelope)
	}{
		{
			name: "apply_routing_adds_metadata",
			envelope: &envelope.Envelope{
				ID:       "msg-123",
				Payload:  []byte("{}"),
				Metadata: map[string]interface{}{},
			},
			decision: &RoutingDecision{
				RuleID:      "rule_1",
				OutputTopic: "output.premium",
			},
			filterID: "filter_1",
			wantErr:  false,
			check: func(t *testing.T, env *envelope.Envelope) {
				if env.Metadata["routing_rule_id"] != "rule_1" {
					t.Errorf("routing_rule_id not set correctly")
				}
				if env.Metadata["routed_to"] != "output.premium" {
					t.Errorf("routed_to not set correctly")
				}
				if len(env.StepHistory) == 0 {
					t.Errorf("StepHistory not updated")
				}
			},
		},
		{
			name:     "apply_routing_nil_envelope",
			envelope: nil,
			decision: &RoutingDecision{
				RuleID:      "rule_1",
				OutputTopic: "output.default",
			},
			filterID: "filter_1",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ApplyRoutingToEnvelope(tt.envelope, tt.decision, tt.filterID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyRoutingToEnvelope error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, tt.envelope)
			}
		})
	}
}

// Helper function for substring checking
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
