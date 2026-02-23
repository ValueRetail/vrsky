package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// RuleEngine executes transformation rules against JSON payloads.
// It orchestrates field extraction, expression evaluation, and type conversion.
type RuleEngine struct {
	fieldMapper         *FieldMapper
	expressionEvaluator *ExpressionEvaluator
	functionRegistry    *FunctionRegistry
	logger              Logger
	ctx                 context.Context
}

// NewRuleEngine creates a new rule engine instance.
func NewRuleEngine(
	ctx context.Context,
	logger Logger,
	fieldMapper *FieldMapper,
	expressionEvaluator *ExpressionEvaluator,
	functionRegistry *FunctionRegistry,
) *RuleEngine {
	if ctx == nil {
		ctx = context.Background()
	}

	return &RuleEngine{
		fieldMapper:         fieldMapper,
		expressionEvaluator: expressionEvaluator,
		functionRegistry:    functionRegistry,
		logger:              logger,
		ctx:                 ctx,
	}
}

// ExecuteTransformations applies a list of transformation rules to a JSON payload.
// Returns a map of transformed fields or an error if critical transformations fail.
func (re *RuleEngine) ExecuteTransformations(payload []byte, rules []Transformation) (map[string]interface{}, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("payload cannot be empty")
	}

	if len(rules) == 0 {
		// No rules - return empty output
		return make(map[string]interface{}), nil
	}

	// Parse payload into a map for variable access in expressions
	var payloadMap map[string]interface{}
	if err := json.Unmarshal(payload, &payloadMap); err != nil {
		if re.logger != nil {
			re.logger.ErrorContext(re.ctx, "failed to parse JSON payload", "error", err.Error())
		}
		return nil, fmt.Errorf("failed to parse payload: %w", err)
	}

	output := make(map[string]interface{})
	var errors []TransformationError

	// Execute each rule sequentially
	for idx, rule := range rules {
		if err := re.executeRule(payload, &rule, output, payloadMap, idx); err != nil {
			transErr := TransformationError{
				Field:     rule.Target,
				Message:   err.Error(),
				Type:      "transformation_failed",
				Timestamp: time.Now(),
			}
			errors = append(errors, transErr)

			if re.logger != nil {
				re.logger.ErrorContext(re.ctx, "transformation rule failed",
					"rule_index", idx, "target", rule.Target, "error", err.Error())
			}

			// For critical rules (no default value or missing target), stop processing
			// Missing target is always critical
			if rule.Target == "" || (rule.Value == nil && rule.Source == "" && rule.Expression == "") {
				return nil, err
			}
		}
	}

	// If there were transformation errors, log them but still return output
	// unless all transformations failed
	if len(errors) > 0 && re.logger != nil {
		for _, err := range errors {
			re.logger.WarnContext(re.ctx, "transformation had errors", "field", err.Field, "message", err.Message)
		}
	}

	return output, nil
}

// executeRule executes a single transformation rule.
func (re *RuleEngine) executeRule(
	payload []byte,
	rule *Transformation,
	output map[string]interface{},
	payloadMap map[string]interface{},
	ruleIndex int,
) error {
	// Target is required
	if rule.Target == "" {
		return fmt.Errorf("rule target cannot be empty")
	}

	// Check condition if present - if false, skip this rule
	if rule.Condition != "" {
		condition, err := re.expressionEvaluator.EvaluateCondition(rule.Condition, payloadMap)
		if err != nil {
			return fmt.Errorf("condition evaluation failed: %w", err)
		}
		if !condition {
			// Condition is false - skip this rule
			return nil
		}
	}

	var value interface{}
	var err error

	// Determine where to get the value: Source, Expression, or Value (in that order)
	if rule.Source != "" {
		// Extract value from source field
		value = re.fieldMapper.ExtractField(payload, rule.Source)
	} else if rule.Expression != "" {
		// Evaluate expression
		value, err = re.expressionEvaluator.Evaluate(rule.Expression, payloadMap)
		if err != nil {
			return fmt.Errorf("expression evaluation failed: %w", err)
		}
	} else if rule.Function != "" {
		// Call custom function (Phase 3 feature)
		// For now, return nil to indicate not yet implemented
		return fmt.Errorf("function transformations not yet implemented in Phase 2")
	} else if rule.Value != nil {
		// Static value
		value = rule.Value
	} else {
		// No source specified - can't proceed
		return fmt.Errorf("rule must specify source, expression, function, or value")
	}

	// Apply type conversion if specified
	if rule.Type != "" && value != nil {
		value = re.fieldMapper.coerceType(value, rule.Type)
	}

	// Assign to output
	output[rule.Target] = value

	if re.logger != nil {
		re.logger.InfoContext(re.ctx, "rule executed successfully",
			"rule_index", ruleIndex, "target", rule.Target, "type", rule.Type)
	}

	return nil
}
