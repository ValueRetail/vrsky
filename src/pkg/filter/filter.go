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
	ActionQueue  Action = "QUEUE"
	ActionDrop   Action = "DROP"
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
	wg              sync.WaitGroup
	health          component.HealthStatus

	// Retry and backoff
	backoffConfig BackoffConfig
	maxRetries    int

	// Validators and schema handler
	schemaValidator *SchemaValidator
	conditionEngine *ConditionEngine

	// Priority 2: Routing and Transformations (optional)
	routingEngine        RoutingEngine
	transformationEngine TransformationEngine

	// Priority 3: Rate limiting (optional)
	rateLimitEngine    RateLimitEngine
	rateLimitRules     []*RateLimitRule
	pendingRateLimitID string // Track which rate limit rule for concurrent tracking
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

	// Initialize routing engine (optional, Priority 2)
	var routingEngine RoutingEngine
	if len(config.RoutingRules) > 0 {
		var err error
		routingEngine, err = NewRoutingEngine(config.RoutingRules, conditionEngine)
		if err != nil {
			return nil, fmt.Errorf("initialize routing engine: %w", err)
		}
	}

	// Initialize transformation engine (optional, Priority 2)
	transformationEngine := NewTransformationEngine(conditionEngine)

	// Initialize rate limit engine (optional, Priority 3)
	var rateLimitEngine RateLimitEngine
	var rateLimitRules []*RateLimitRule
	if len(config.RateLimitRules) > 0 {
		var err error
		rateLimitRules, err = parseRateLimitRules(config.RateLimitRules)
		if err != nil {
			return nil, fmt.Errorf("parse rate limit rules: %w", err)
		}

		// Create registry for this filter's rate limit metrics
		registry := metricsRegistry
		rateLimitEngine = NewRateLimitEngine(conditionEngine, nil, logger) // metrics will be passed in filter
		for _, rule := range rateLimitRules {
			if err := rateLimitEngine.AddRule(rule); err != nil {
				return nil, fmt.Errorf("add rate limit rule %s: %w", rule.ID, err)
			}
		}
		_ = registry // Suppress unused warning
	}

	f := &FilterImpl{
		id:                   id,
		config:               config,
		rules:                rules,
		natsConn:             natsConn,
		natsInputSubject:     config.InputTopic,
		natsOutputTopic:      config.OutputTopic,
		natsRejectionTopic:   config.RejectionTopic,
		logger:               logger,
		metricsRegistry:      metricsRegistry,
		health:               component.HealthStopped,
		backoffConfig:        DefaultBackoffConfig(),
		maxRetries:           3,
		schemaValidator:      schemaValidator,
		conditionEngine:      conditionEngine,
		routingEngine:        routingEngine,
		transformationEngine: transformationEngine,
		rateLimitEngine:      rateLimitEngine,
		rateLimitRules:       rateLimitRules,
		closed:               true, // Initialize as closed (not yet started)
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

	// Stop rate limit engine if running
	if f.rateLimitEngine != nil {
		if err := f.rateLimitEngine.Stop(); err != nil {
			f.logger.WarnContext(ctx, "Error stopping rate limit engine", "error", err)
		}
	}

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

// getRateLimitRuleState safely retrieves the rule state from the rate limit engine
func (f *FilterImpl) getRateLimitRuleState(ruleID string) (*RateLimitState, bool) {
	if f.rateLimitEngine == nil {
		return nil, false
	}

	// Cast to get access to the state map
	if impl, ok := f.rateLimitEngine.(*RateLimitEngineImpl); ok {
		impl.mu.RLock()
		defer impl.mu.RUnlock()

		state, ok := impl.state[ruleID]
		return state, ok
	}

	return nil, false
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

			// Priority 2: Apply conditional routing if configured
			if f.routingEngine != nil {
				// Parse payload for routing evaluation
				var payload interface{}
				if err := json.Unmarshal(env.Payload, &payload); err != nil {
					f.logger.WarnContext(f.ctx, "Failed to parse payload for routing",
						"envelope_id", env.ID,
						"error", err,
					)
					f.metrics.RecordRoutingFailure()
					outputTopic = f.natsRejectionTopic
				} else {
					// Evaluate routing rules
					routingDecision, err := f.routingEngine.EvaluateRules(payload, env.Metadata)
					if err != nil {
						f.logger.WarnContext(f.ctx, "Routing evaluation failed",
							"envelope_id", env.ID,
							"error", err,
						)
						f.metrics.RecordRoutingFailure()
						outputTopic = f.natsRejectionTopic
					} else {
						outputTopic = routingDecision.OutputTopic

						// Apply transformations (Priority 2)
						if len(routingDecision.Transformations) > 0 {
							if err := f.transformationEngine.ApplyTransformations(env, routingDecision.Transformations, payload); err != nil {
								f.logger.WarnContext(f.ctx, "Transformation failed, sending to DLQ",
									"envelope_id", env.ID,
									"routing_rule", routingDecision.RuleID,
									"error", err,
								)
								f.metrics.RecordTransformationFailure()
								outputTopic = f.natsRejectionTopic
							}
						}

						// Apply routing metadata
						if err := ApplyRoutingToEnvelope(env, routingDecision, f.id); err != nil {
							f.logger.WarnContext(f.ctx, "Failed to apply routing metadata",
								"envelope_id", env.ID,
								"error", err,
							)
						}
					}
				}
			}
		} else {
			outputTopic = f.natsRejectionTopic
		}

		// Priority 3: Apply rate limiting if configured
		f.pendingRateLimitID = "" // Reset
		if decision.Action == ActionAccept && f.rateLimitEngine != nil {
			// Parse payload for rate limit evaluation
			var payload interface{}
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				f.logger.WarnContext(f.ctx, "Failed to parse payload for rate limiting",
					"envelope_id", env.ID,
					"error", err,
				)
				f.metrics.RecordFailure()
				return
			}

			// Evaluate rate limit rules
			rateLimitDecision, err := f.rateLimitEngine.EvaluateRules(f.ctx, payload, env.Metadata)
			if err != nil {
				f.logger.WarnContext(f.ctx, "Rate limit evaluation failed",
					"envelope_id", env.ID,
					"error", err,
				)
				f.metrics.RecordFailure()
				outputTopic = f.natsRejectionTopic
			} else if !rateLimitDecision.Allowed {
				// Rate limited - handle based on exceed action
				switch rateLimitDecision.Action {
				case "queue":
					f.logger.DebugContext(f.ctx, "Message queued by rate limiter",
						"envelope_id", env.ID,
						"rule_id", rateLimitDecision.RuleID,
						"current", rateLimitDecision.Current,
						"limit", rateLimitDecision.Limit,
					)
					f.metrics.RecordRateLimitQueue()

					// Queue message for later retry by background worker
					if ruleState, ok := f.getRateLimitRuleState(rateLimitDecision.RuleID); ok && ruleState.queue != nil {
						queuedMsg := &QueuedMessage{
							Envelope:  env,
							RuleID:    rateLimitDecision.RuleID,
							Timestamp: time.Now(),
						}

						if err := ruleState.queue.Enqueue(queuedMsg); err != nil {
							f.logger.WarnContext(f.ctx, "Failed to queue message",
								"envelope_id", env.ID,
								"rule_id", rateLimitDecision.RuleID,
								"error", err,
							)
							f.metrics.RecordFailure()
						}
					}
					return

				case "drop":
					f.logger.DebugContext(f.ctx, "Message dropped by rate limiter",
						"envelope_id", env.ID,
						"rule_id", rateLimitDecision.RuleID,
						"current", rateLimitDecision.Current,
						"limit", rateLimitDecision.Limit,
					)
					f.metrics.RecordRateLimitDrop()
					// Drop silently - return without publishing
					return

				case "reject":
					f.logger.DebugContext(f.ctx, "Message rejected by rate limiter",
						"envelope_id", env.ID,
						"rule_id", rateLimitDecision.RuleID,
						"current", rateLimitDecision.Current,
						"limit", rateLimitDecision.Limit,
					)
					f.metrics.RecordRateLimitReject()
					// Send to rejection topic
					outputTopic = f.natsRejectionTopic
				}
			} else {
				// Rate limit allowed - track for concurrent limiting
				f.pendingRateLimitID = rateLimitDecision.RuleID
			}
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

		// Notify rate limiter that message was published (for concurrent tracking)
		if f.pendingRateLimitID != "" {
			if err := f.rateLimitEngine.RecordMessageComplete(f.pendingRateLimitID); err != nil {
				f.logger.DebugContext(f.ctx, "Failed to record message complete in rate limiter",
					"rule_id", f.pendingRateLimitID,
					"error", err,
				)
			}
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

// getString safely extracts string from map
func getString(m map[interface{}]interface{}, key string, defaultVal string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultVal
}

// parseRateLimitRules converts raw rate limit rule configs to RateLimitRule structs
func parseRateLimitRules(rawRules []interface{}) ([]*RateLimitRule, error) {
	rules := make([]*RateLimitRule, 0)

	for i, rawRule := range rawRules {
		ruleMap, ok := rawRule.(map[interface{}]interface{})
		if !ok {
			return nil, fmt.Errorf("rate limit rule %d is not a map", i)
		}

		rule := &RateLimitRule{
			ID:           getString(ruleMap, "id", fmt.Sprintf("rl_rule_%d", i)),
			Priority:     getIntKey(ruleMap, "priority", 100),
			Strategy:     getString(ruleMap, "strategy", ""),
			ExceedAction: getString(ruleMap, "exceed_action", "reject"),
			QueueSize:    getIntKey(ruleMap, "queue_size", 0),
		}

		// Parse strategy-specific fields
		if maxPerWindow, ok := ruleMap["max_messages_per_window"]; ok {
			rule.MaxMessagesPerWindow = getIntValue(maxPerWindow)
		}
		if windowDur, ok := ruleMap["window_duration_seconds"]; ok {
			rule.WindowDurationSeconds = getIntValue(windowDur)
		}
		if maxConcurrent, ok := ruleMap["max_concurrent"]; ok {
			rule.MaxConcurrent = getIntValue(maxConcurrent)
		}
		if tbRate, ok := ruleMap["token_bucket_rate"]; ok {
			rule.TokenBucketRate = getIntValue(tbRate)
		}
		if tbCap, ok := ruleMap["token_bucket_capacity"]; ok {
			rule.TokenBucketCapacity = getIntValue(tbCap)
		}

		// Parse condition if present
		if condRaw, ok := ruleMap["condition"]; ok {
			if condMap, ok := condRaw.(map[interface{}]interface{}); ok {
				rule.Condition = parseCondition(condMap)
			}
		}

		// Validate rule
		if err := validateRateLimitRule(rule); err != nil {
			return nil, fmt.Errorf("rate limit rule %s: %w", rule.ID, err)
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

// getIntKey safely extracts int from map (by key name)
func getIntKey(m map[interface{}]interface{}, key string, defaultVal int) int {
	if v, ok := m[key]; ok {
		return getIntValue(v)
	}
	return defaultVal
}

// parseCondition converts a YAML map to a Condition struct
func parseCondition(condMap map[interface{}]interface{}) *Condition {
	if condMap == nil {
		return nil
	}

	condition := &Condition{}

	// Parse operator
	if op, ok := condMap["operator"]; ok {
		if s, ok := op.(string); ok {
			condition.Operator = s
		}
	}

	// Parse field
	if field, ok := condMap["field"]; ok {
		if s, ok := field.(string); ok {
			condition.Field = s
		}
	}

	// Parse value (can be any type)
	if value, ok := condMap["value"]; ok {
		condition.Value = value
	}

	return condition
}

// getIntValue converts various types to int
func getIntValue(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case string:
		var i int
		_, _ = fmt.Sscanf(val, "%d", &i)
		return i
	default:
		return 0
	}
}
