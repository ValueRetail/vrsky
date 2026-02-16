package filter

import (
	"fmt"
)

// GatingDecision represents the result of gating evaluation
type GatingDecision struct {
	Accept      bool     // Whether to accept or reject
	RuleID      string   // Which rule triggered this decision
	Reason      string   // Human-readable reason
	RuleMatches []string // All rules that matched
}

// Gating provides the core gating logic for the filter
type Gating struct {
	conditionEngine *ConditionEngine
}

// NewGating creates a new gating instance
func NewGating(engine *ConditionEngine) *Gating {
	return &Gating{
		conditionEngine: engine,
	}
}

// Evaluate evaluates all rules and returns a gating decision
func (g *Gating) Evaluate(rules []*Rule, payload interface{}) *GatingDecision {
	matchedRules := []string{}

	// Evaluate each rule in order
	for _, rule := range rules {
		matches, err := g.evaluateRule(rule, payload)
		if err != nil {
			continue
		}

		if matches {
			matchedRules = append(matchedRules, rule.ID)
		}
	}

	// Decision logic: if any rule matched, accept
	if len(matchedRules) > 0 {
		return &GatingDecision{
			Accept:      true,
			RuleID:      matchedRules[0],
			Reason:      fmt.Sprintf("Rule '%s' accepted message", matchedRules[0]),
			RuleMatches: matchedRules,
		}
	}

	// No rules matched - reject
	return &GatingDecision{
		Accept:      false,
		Reason:      "No matching rules found",
		RuleMatches: []string{},
	}
}

// evaluateRule checks if a message matches a single rule
func (g *Gating) evaluateRule(rule *Rule, payload interface{}) (bool, error) {
	// If no condition specified, consider it a match
	if rule.Condition == nil {
		return true, nil
	}

	// Evaluate condition
	return g.conditionEngine.Evaluate(rule.Condition, payload)
}
