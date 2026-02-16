package filter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// Filter is the core interface for the filter component
// It accepts or rejects messages based on configurable rules
type Filter interface {
	component.Component

	// ProcessMessage evaluates a message against filter rules
	// Returns Decision indicating ACCEPT or REJECT
	ProcessMessage(ctx context.Context, env *envelope.Envelope) (*Decision, error)
}

// Decision represents the outcome of filter evaluation
type Decision struct {
	Action   Action        // ACCEPT or REJECT
	Message  string        // Reason for decision
	RuleID   string        // ID of rule that triggered decision
	Duration time.Duration // Time taken to evaluate
}

// Action represents the decision made by the filter
type Action string

const (
	ActionAccept Action = "ACCEPT"
	ActionReject Action = "REJECT"
)

// FilterImpl implements the Filter interface
type FilterImpl struct {
	// Configuration
	id     string
	config *Config
	rules  []*Rule

	// Connections
	natsConn           *nats.Conn
	natsInputSubject   string
	natsOutputTopic    string
	natsRejectionTopic string

	// Runtime
	logger          *slog.Logger
	metricsRegistry prometheus.Registerer
	metrics         *FilterMetrics
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.RWMutex
	closed          bool
	closedOnce      sync.Once
	wg              sync.WaitGroup
	health          component.HealthStatus

	// Retry and backoff
	backoffConfig BackoffConfig
	maxRetries    int

	// Validators and schema handler
	schemaValidator *SchemaValidator
	conditionEngine *ConditionEngine
}

// NewFilter creates a new filter instance
func NewFilter(
	id string,
	config *Config,
	natsConn *nats.Conn,
	logger *slog.Logger,
	metricsRegistry prometheus.Registerer,
) (*FilterImpl, error) {
	if id == "" {
		return nil, fmt.Errorf("filter id cannot be empty")
	}
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if natsConn == nil {
		return nil, fmt.Errorf("nats connection cannot be nil")
	}

	if logger == nil {
		logger = slog.Default()
	}
	if metricsRegistry == nil {
		metricsRegistry = prometheus.DefaultRegisterer
	}

	// Create schema validator
	schemaValidator, err := NewSchemaValidator()
	if err != nil {
		return nil, fmt.Errorf("create schema validator: %w", err)
	}

	// Create condition engine
	conditionEngine := NewConditionEngine()

	// Parse rules from config
	rules, err := parseRules(config.Rules)
	if err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}

	f := &FilterImpl{
		id:                 id,
		config:             config,
		rules:              rules,
		natsConn:           natsConn,
		natsInputSubject:   config.InputTopic,
		natsOutputTopic:    config.OutputTopic,
		natsRejectionTopic: config.RejectionTopic,
		logger:             logger,
		metricsRegistry:    metricsRegistry,
		health:             component.HealthStopped,
		backoffConfig:      DefaultBackoffConfig(),
		maxRetries:         3,
		schemaValidator:    schemaValidator,
		conditionEngine:    conditionEngine,
	}

	// Register metrics
	f.metrics = NewFilterMetrics(id, metricsRegistry)

	return f, nil
}

// Name returns the filter's human-readable name
func (f *FilterImpl) Name() string {
	return fmt.Sprintf("Filter/%s", f.id)
}

// Type returns the component type
func (f *FilterImpl) Type() component.ComponentType {
	return component.TypeFilter
}

// Version returns the component version
func (f *FilterImpl) Version() string {
	return "1.0.0"
}

// Start initializes and starts the filter
func (f *FilterImpl) Start(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.closed {
		return fmt.Errorf("filter already started")
	}

	f.ctx, f.cancel = context.WithCancel(ctx)
	f.closed = false
	f.health = component.HealthHealthy

	f.logger.InfoContext(f.ctx, "Starting filter",
		"filter_id", f.id,
		"input_topic", f.natsInputSubject,
		"output_topic", f.natsOutputTopic,
		"rejection_topic", f.natsRejectionTopic,
		"rules_count", len(f.rules),
	)

	// Subscribe to input topic
	f.wg.Add(1)
	go f.consumeMessages()

	return nil
}

// Stop gracefully shuts down the filter
func (f *FilterImpl) Stop(ctx context.Context) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	f.health = component.HealthStopped
	f.cancel()
	f.mu.Unlock()

	// Wait for message processing with timeout
	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		f.logger.InfoContext(ctx, "Filter stopped", "filter_id", f.id)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("filter stop timeout")
	}
}

// Health returns the current health status
func (f *FilterImpl) Health() component.HealthStatus {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.health
}

// ProcessMessage evaluates a message against filter rules
func (f *FilterImpl) ProcessMessage(ctx context.Context, env *envelope.Envelope) (*Decision, error) {
	start := time.Now()

	// Parse message payload
	var payload interface{}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		f.metrics.RecordFailure()
		f.logger.WarnContext(ctx, "Failed to parse message payload",
			"envelope_id", env.ID,
			"error", err,
		)
		return &Decision{
			Action:   ActionReject,
			Message:  fmt.Sprintf("Invalid JSON payload: %v", err),
			Duration: time.Since(start),
		}, nil
	}

	// Evaluate each rule in order
	for _, rule := range f.rules {
		matches, err := f.evaluateRule(ctx, rule, payload)
		if err != nil {
			f.logger.WarnContext(ctx, "Error evaluating rule",
				"rule_id", rule.ID,
				"error", err,
			)
			f.metrics.RecordFailure()
			continue
		}

		if matches {
			f.metrics.RecordAccepted()
			return &Decision{
				Action:   ActionAccept,
				Message:  fmt.Sprintf("Rule '%s' accepted message", rule.Name),
				RuleID:   rule.ID,
				Duration: time.Since(start),
			}, nil
		}
	}

	// No rules matched - reject by default
	f.metrics.RecordRejected()
	return &Decision{
		Action:   ActionReject,
		Message:  "No matching rules found",
		Duration: time.Since(start),
	}, nil
}

// evaluateRule checks if a message matches the rule conditions
func (f *FilterImpl) evaluateRule(ctx context.Context, rule *Rule, payload interface{}) (bool, error) {
	// Validate against schema if present
	if rule.SchemaID != "" {
		if err := f.schemaValidator.Validate(rule.SchemaID, payload); err != nil {
			f.logger.WarnContext(ctx, "Schema validation failed",
				"rule_id", rule.ID,
				"schema_id", rule.SchemaID,
				"error", err,
			)
			return false, nil
		}
	}

	// Evaluate conditions
	if rule.Condition != nil {
		result, err := f.conditionEngine.Evaluate(rule.Condition, payload)
		if err != nil {
			return false, fmt.Errorf("evaluate condition: %w", err)
		}
		return result, nil
	}

	// No conditions = accept
	return true, nil
}

// consumeMessages reads from the input topic and processes messages
func (f *FilterImpl) consumeMessages() {
	defer f.wg.Done()

	sub, err := f.natsConn.Subscribe(f.natsInputSubject, func(msg *nats.Msg) {
		// Parse envelope
		env, err := envelope.Unmarshal(msg.Data)
		if err != nil {
			f.logger.WarnContext(f.ctx, "Failed to unmarshal envelope",
				"error", err,
			)
			f.metrics.RecordFailure()
			return
		}

		f.metrics.RecordReceived()

		// Process message
		decision, err := f.ProcessMessage(f.ctx, env)
		if err != nil {
			f.logger.WarnContext(f.ctx, "Error processing message",
				"envelope_id", env.ID,
				"error", err,
			)
			f.metrics.RecordFailure()
			return
		}

		// Route based on decision
		var outputTopic string
		if decision.Action == ActionAccept {
			outputTopic = f.natsOutputTopic
		} else {
			outputTopic = f.natsRejectionTopic
		}

		// Publish to appropriate topic
		env.StepHistory = append(env.StepHistory, fmt.Sprintf("%s:%s", f.id, decision.Action))
		data, err := envelope.Marshal(env)
		if err != nil {
			f.logger.WarnContext(f.ctx, "Failed to marshal envelope",
				"envelope_id", env.ID,
				"error", err,
			)
			f.metrics.RecordFailure()
			return
		}

		if err := f.natsConn.Publish(outputTopic, data); err != nil {
			f.logger.WarnContext(f.ctx, "Failed to publish message",
				"envelope_id", env.ID,
				"output_topic", outputTopic,
				"error", err,
			)
			f.metrics.RecordFailure()
			return
		}

		f.logger.DebugContext(f.ctx, "Message processed",
			"envelope_id", env.ID,
			"action", decision.Action,
			"rule_id", decision.RuleID,
			"duration_ms", decision.Duration.Milliseconds(),
		)
	})

	if err != nil {
		f.logger.ErrorContext(f.ctx, "Failed to subscribe to input topic",
			"topic", f.natsInputSubject,
			"error", err,
		)
		f.mu.Lock()
		f.health = component.HealthUnhealthy
		f.mu.Unlock()
		return
	}

	<-f.ctx.Done()
	if err := sub.Unsubscribe(); err != nil {
		f.logger.WarnContext(f.ctx, "Failed to unsubscribe", "error", err)
	}
}

// parseRules converts raw rule configs to Rule structs
func parseRules(rawRules []interface{}) ([]*Rule, error) {
	rules := make([]*Rule, 0)

	for i, rawRule := range rawRules {
		ruleMap, ok := rawRule.(map[interface{}]interface{})
		if !ok {
			return nil, fmt.Errorf("rule %d is not a map", i)
		}

		rule := &Rule{
			ID:       fmt.Sprintf("rule_%d", i),
			Name:     getString(ruleMap, "name", ""),
			SchemaID: getString(ruleMap, "schema_id", ""),
		}

		// Parse condition if present
		if condRaw, ok := ruleMap["condition"]; ok {
			condMap, ok := condRaw.(map[interface{}]interface{})
			if !ok {
				return nil, fmt.Errorf("rule %d condition is not a map", i)
			}
			rule.Condition = parseCondition(condMap)
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

// parseCondition converts raw condition map to Condition struct
func parseCondition(raw map[interface{}]interface{}) *Condition {
	return &Condition{
		Operator: getString(raw, "operator", ""),
		Field:    getString(raw, "field", ""),
		Value:    raw["value"],
	}
}

// getString safely extracts string from map
func getString(m map[interface{}]interface{}, key string, defaultVal string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultVal
}
