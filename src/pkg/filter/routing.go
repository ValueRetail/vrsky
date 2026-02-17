package filter

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// RoutingRule represents a rule that routes messages to specific output topics
type RoutingRule struct {
	ID              string            // Unique rule identifier
	Name            string            // Human-readable name
	Priority        int               // Execution order (lower = first)
	Condition       *Condition        // Single condition (required)
	OutputTopic     string            // NATS topic for matched messages (required)
	Transformations []*Transformation // Optional metadata modifications
	StopOnMatch     bool              // Stop evaluation after this rule matches
}

// Transformation represents a metadata transformation to apply
type Transformation struct {
	Action string      // add_field, remove_field, rename_field, set_field, extract_field, enrich_from_config
	Field  string      // Target field name in Metadata
	Value  interface{} // New value or template expression
	Source string      // For rename/copy operations
}

// RoutingDecision represents the outcome of routing rule evaluation
type RoutingDecision struct {
	RuleID            string
	OutputTopic       string
	Transformations   []*Transformation
	TransformationErr error // Set if transformation fails
}

// RoutingEngine interface defines the contract for routing implementations
type RoutingEngine interface {
	// EvaluateRules returns the first matching rule's decision, or nil if no match
	EvaluateRules(payload interface{}, metadata map[string]interface{}) (*RoutingDecision, error)

	// GetRules returns all configured routing rules
	GetRules() []*RoutingRule
}

// RoutingEngineImpl implements the RoutingEngine interface
type RoutingEngineImpl struct {
	rules []*RoutingRule
	mu    sync.RWMutex

	// Engine dependencies
	conditionEngine *ConditionEngine
}

// NewRoutingEngine creates a new routing engine with parsed rules
func NewRoutingEngine(rawRules []interface{}, conditionEngine *ConditionEngine) (RoutingEngine, error) {
	if conditionEngine == nil {
		return nil, fmt.Errorf("condition engine cannot be nil")
	}

	rules, err := parseRoutingRules(rawRules)
	if err != nil {
		return nil, fmt.Errorf("parse routing rules: %w", err)
	}

	// Validate at least one catch-all rule exists
	hasCatchAll := false
	for _, rule := range rules {
		if rule.Condition != nil && rule.Condition.Operator == "always" {
			hasCatchAll = true
			break
		}
	}
	if !hasCatchAll {
		return nil, fmt.Errorf("routing rules must include at least one catch-all rule (operator: always)")
	}

	// Sort rules by priority (lower number = higher priority)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})

	return &RoutingEngineImpl{
		rules:           rules,
		conditionEngine: conditionEngine,
	}, nil
}

// EvaluateRules evaluates all routing rules in priority order
// Returns the first matching rule's decision or error if no match found (should never happen with catch-all)
func (re *RoutingEngineImpl) EvaluateRules(payload interface{}, metadata map[string]interface{}) (*RoutingDecision, error) {
	re.mu.RLock()
	defer re.mu.RUnlock()

	for _, rule := range re.rules {
		// Evaluate condition
		matches, err := re.conditionEngine.Evaluate(rule.Condition, payload)
		if err != nil {
			return nil, fmt.Errorf("evaluate routing condition in rule %s: %w", rule.ID, err)
		}

		if matches {
			// Matched! Return decision for this rule
			decision := &RoutingDecision{
				RuleID:          rule.ID,
				OutputTopic:     rule.OutputTopic,
				Transformations: rule.Transformations,
			}
			return decision, nil
		}
	}

	// Should never reach here due to catch-all requirement
	return nil, fmt.Errorf("no matching routing rules found (this should not happen with catch-all rule)")
}

// GetRules returns all configured routing rules (read-only)
func (re *RoutingEngineImpl) GetRules() []*RoutingRule {
	re.mu.RLock()
	defer re.mu.RUnlock()
	return re.rules
}

// parseRoutingRules converts raw rule configs to RoutingRule structs
func parseRoutingRules(rawRules []interface{}) ([]*RoutingRule, error) {
	rules := make([]*RoutingRule, 0)

	for i, rawRule := range rawRules {
		ruleMap, ok := rawRule.(map[interface{}]interface{})
		if !ok {
			return nil, fmt.Errorf("routing rule %d is not a map", i)
		}

		// Parse required fields
		id := getStringKey(ruleMap, "id")
		if id == "" {
			id = fmt.Sprintf("routing_rule_%d", i)
		}
		name := getStringKey(ruleMap, "name", fmt.Sprintf("Routing Rule %d", i))
		outputTopic := getStringKey(ruleMap, "output_topic")
		if outputTopic == "" {
			return nil, fmt.Errorf("routing rule %d (id=%s) missing output_topic", i, id)
		}

		priority := getIntKey(ruleMap, "priority", 100)
		stopOnMatch := getBoolKey(ruleMap, "stop_on_match", false)

		// Parse condition (required for routing rules)
		var condition *Condition
		if condRaw, ok := ruleMap["condition"]; ok {
			condMap, ok := condRaw.(map[interface{}]interface{})
			if !ok {
				return nil, fmt.Errorf("routing rule %d (id=%s) condition is not a map", i, id)
			}
			condition = parseCondition(condMap)
		} else {
			return nil, fmt.Errorf("routing rule %d (id=%s) missing condition", i, id)
		}

		if condition == nil {
			return nil, fmt.Errorf("routing rule %d (id=%s) condition is invalid", i, id)
		}

		// Parse transformations (optional)
		var transformations []*Transformation
		if transRaw, ok := ruleMap["transformations"]; ok {
			transList, ok := transRaw.([]interface{})
			if !ok {
				return nil, fmt.Errorf("routing rule %d (id=%s) transformations is not a list", i, id)
			}
			for j, rawTrans := range transList {
				transMap, ok := rawTrans.(map[interface{}]interface{})
				if !ok {
					return nil, fmt.Errorf("routing rule %d (id=%s) transformation %d is not a map", i, id, j)
				}
				trans := parseTransformation(transMap)
				if trans == nil {
					return nil, fmt.Errorf("routing rule %d (id=%s) transformation %d is invalid", i, id, j)
				}
				transformations = append(transformations, trans)
			}
		}

		rule := &RoutingRule{
			ID:              id,
			Name:            name,
			Priority:        priority,
			Condition:       condition,
			OutputTopic:     outputTopic,
			Transformations: transformations,
			StopOnMatch:     stopOnMatch,
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

// parseTransformation converts raw transformation map to Transformation struct
func parseTransformation(raw map[interface{}]interface{}) *Transformation {
	action := getStringKey(raw, "action")
	if action == "" {
		return nil
	}

	field := getStringKey(raw, "field")
	source := getStringKey(raw, "source")
	value := raw["value"]

	return &Transformation{
		Action: action,
		Field:  field,
		Source: source,
		Value:  value,
	}
}

// ApplyRoutingToEnvelope applies routing decision to envelope metadata and step history
func ApplyRoutingToEnvelope(env *envelope.Envelope, decision *RoutingDecision, filterID string) error {
	if env == nil || decision == nil {
		return fmt.Errorf("envelope and decision cannot be nil")
	}

	// Initialize metadata if nil
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}

	// Record routing decision in step history
	env.StepHistory = append(env.StepHistory, fmt.Sprintf("%s:ROUTED(%s)", filterID, decision.RuleID))

	// Add routing metadata
	env.Metadata["routing_rule_id"] = decision.RuleID
	env.Metadata["routed_to"] = decision.OutputTopic

	return nil
}

// Helper functions for parsing

// getStringKey safely extracts string from map with multiple key variants
func getStringKey(m map[interface{}]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}


// getBoolKey safely extracts bool from map
func getBoolKey(m map[interface{}]interface{}, key string, defaultVal bool) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return defaultVal
}
