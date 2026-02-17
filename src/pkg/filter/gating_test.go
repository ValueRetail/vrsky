package filter

import (
	"testing"
)

func TestGating_EvaluateAccept(t *testing.T) {
	ce := NewConditionEngine()
	gating := NewGating(ce)

	rules := []*Rule{
		{
			ID:   "rule_1",
			Name: "Check status",
			Condition: &Condition{
				Operator: "==",
				Field:    "status",
				Value:    "active",
			},
		},
	}

	payload := map[string]interface{}{
		"status": "active",
	}

	decision := gating.Evaluate(rules, payload)

	if !decision.Accept {
		t.Errorf("Expected accept, got reject")
	}
	if decision.RuleID != "rule_1" {
		t.Errorf("Expected rule_1, got %s", decision.RuleID)
	}
	if len(decision.RuleMatches) != 1 {
		t.Errorf("Expected 1 match, got %d", len(decision.RuleMatches))
	}
}

func TestGating_EvaluateReject(t *testing.T) {
	ce := NewConditionEngine()
	gating := NewGating(ce)

	rules := []*Rule{
		{
			ID:   "rule_1",
			Name: "Check status",
			Condition: &Condition{
				Operator: "==",
				Field:    "status",
				Value:    "active",
			},
		},
	}

	payload := map[string]interface{}{
		"status": "inactive",
	}

	decision := gating.Evaluate(rules, payload)

	if decision.Accept {
		t.Errorf("Expected reject, got accept")
	}
	if len(decision.RuleMatches) != 0 {
		t.Errorf("Expected 0 matches, got %d", len(decision.RuleMatches))
	}
}

func TestGating_MultipleRules(t *testing.T) {
	ce := NewConditionEngine()
	gating := NewGating(ce)

	rules := []*Rule{
		{
			ID: "rule_1",
			Condition: &Condition{
				Operator: "==",
				Field:    "type",
				Value:    "premium",
			},
		},
		{
			ID: "rule_2",
			Condition: &Condition{
				Operator: ">",
				Field:    "amount",
				Value:    100,
			},
		},
	}

	payload := map[string]interface{}{
		"type":   "standard",
		"amount": 150,
	}

	decision := gating.Evaluate(rules, payload)

	if !decision.Accept {
		t.Errorf("Expected accept (rule_2 matched), got reject")
	}
	if decision.RuleID != "rule_2" {
		t.Errorf("Expected rule_2, got %s", decision.RuleID)
	}
}

func TestGating_NoConditionRule(t *testing.T) {
	ce := NewConditionEngine()
	gating := NewGating(ce)

	rules := []*Rule{
		{
			ID:   "rule_1",
			Name: "Accept all",
			// No condition
		},
	}

	payload := map[string]interface{}{
		"data": "anything",
	}

	decision := gating.Evaluate(rules, payload)

	if !decision.Accept {
		t.Errorf("Expected accept (no condition = match), got reject")
	}
}

func TestGating_EmptyRules(t *testing.T) {
	ce := NewConditionEngine()
	gating := NewGating(ce)

	decision := gating.Evaluate([]*Rule{}, map[string]interface{}{})

	if decision.Accept {
		t.Errorf("Expected reject (no rules), got accept")
	}
}
