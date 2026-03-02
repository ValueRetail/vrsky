package filter

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// RateLimitEngine provides rate limiting functionality
type RateLimitEngine interface {
	// AddRule adds a new rate limit rule
	AddRule(rule *RateLimitRule) error

	// EvaluateRules evaluates all rate limit rules and returns a decision
	EvaluateRules(ctx context.Context, payload interface{}, metadata map[string]interface{}) (*RateLimitDecision, error)

	// RecordMessageComplete notifies the engine that a message has been published (for concurrent limiting)
	RecordMessageComplete(ruleID string) error

	// GetStats returns statistics for a specific rule
	GetStats(ruleID string) *RateLimitStats

	// Stop gracefully shuts down the rate limit engine and its background workers
	Stop() error

	// GetQueuedMessages retrieves queued messages that are ready for retry
	GetQueuedMessages(ruleID string, maxMessages int) []*QueuedMessage
}

// RateLimitEngineImpl implements the RateLimitEngine interface
type RateLimitEngineImpl struct {
	rules           map[string]*RateLimitRule
	state           map[string]*RateLimitState
	mu              sync.RWMutex
	conditionEngine *ConditionEngine
	metrics         *FilterMetrics
	logger          *slog.Logger
	queueWorkerDone chan struct{}
	queueWorkerCtx  context.Context
	queueWorkerStop context.CancelFunc
}

// RateLimitRule defines a rate limit rule
type RateLimitRule struct {
	ID                    string     // Unique identifier
	Priority              int        // Execution order (lower = first)
	Condition             *Condition // Condition to match
	Strategy              string     // "time_window", "concurrent", or "token_bucket"
	MaxMessagesPerWindow  int        // Strategy: time_window
	WindowDurationSeconds int        // Strategy: time_window
	MaxConcurrent         int        // Strategy: concurrent
	TokenBucketRate       int        // Strategy: token_bucket (tokens per second)
	TokenBucketCapacity   int        // Strategy: token_bucket (max tokens)
	ExceedAction          string     // "queue", "drop", or "reject"
	QueueSize             int        // For "queue" exceed action
}

// RateLimitState tracks runtime state for a rule
type RateLimitState struct {
	// Time-window state
	windowStart        time.Time
	windowMessageCount int
	windowMutex        sync.Mutex

	// Concurrent state
	concurrentCount int
	concurrentMutex sync.Mutex

	// Token bucket state
	tokensAvailable     float64
	lastTokenRefillTime time.Time
	tokenBucketMutex    sync.Mutex

	// Queue state
	queue *RateLimitQueue
}

// RateLimitDecision represents the outcome of rate limit evaluation
type RateLimitDecision struct {
	Action          string // "queue", "drop", "reject", or "" (allowed)
	Allowed         bool   // true if message passed rate limit
	Reason          string // Why accepted/rejected
	Current         int    // Current count/tokens
	Limit           int    // Max allowed
	RuleID          string // Which rule applied
	WindowOrSeconds int    // Time window or token refill window
	StrategyUsed    string // Which strategy applied (for debugging)
}

// RateLimitStats tracks statistics for a rule
type RateLimitStats struct {
	RuleID           string
	Strategy         string
	CurrentValue     int       // Current count for time-window or concurrent
	CurrentTokens    float64   // Current tokens for token bucket
	Limit            int       // Configured limit
	TotalProcessed   int       // Total messages processed by this rule
	TotalRateLimited int       // Total messages rate limited
	TotalQueued      int       // Total messages queued
	TotalDropped     int       // Total messages dropped
	TotalRejected    int       // Total messages rejected
	WindowResetTime  time.Time // Next window reset time (for time-window)
	TokenRefillTime  time.Time // Next token refill time (for token bucket)
}

// NewRateLimitEngine creates a new rate limit engine
func NewRateLimitEngine(
	conditionEngine *ConditionEngine,
	metrics *FilterMetrics,
	logger *slog.Logger,
) *RateLimitEngineImpl {
	if logger == nil {
		logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())

	rle := &RateLimitEngineImpl{
		rules:           make(map[string]*RateLimitRule),
		state:           make(map[string]*RateLimitState),
		conditionEngine: conditionEngine,
		metrics:         metrics,
		logger:          logger,
		queueWorkerDone: make(chan struct{}),
		queueWorkerCtx:  ctx,
		queueWorkerStop: cancel,
	}

	// Start background queue worker
	go rle.queueWorker()

	return rle
}

// AddRule adds a new rate limit rule
func (rle *RateLimitEngineImpl) AddRule(rule *RateLimitRule) error {
	if rule == nil {
		return fmt.Errorf("rule cannot be nil")
	}
	if rule.ID == "" {
		return fmt.Errorf("rule ID cannot be empty")
	}

	// Validate strategy
	if err := validateRateLimitRule(rule); err != nil {
		return err
	}

	rle.mu.Lock()
	defer rle.mu.Unlock()

	if _, exists := rle.rules[rule.ID]; exists {
		return fmt.Errorf("rule with ID %s already exists", rule.ID)
	}

	rle.rules[rule.ID] = rule

	// Initialize state based on strategy
	state := &RateLimitState{
		windowStart:         time.Now(),
		windowMessageCount:  0,
		concurrentCount:     0,
		tokensAvailable:     float64(rule.TokenBucketCapacity),
		lastTokenRefillTime: time.Now(),
	}

	// Create queue if needed
	if rule.ExceedAction == "queue" {
		state.queue = NewRateLimitQueue(rule.QueueSize, rle.logger)
	}

	rle.state[rule.ID] = state

	rle.logger.DebugContext(rle.queueWorkerCtx, "Rate limit rule added",
		"rule_id", rule.ID,
		"strategy", rule.Strategy,
		"priority", rule.Priority,
	)

	return nil
}

// EvaluateRules evaluates all rate limit rules and returns a decision
func (rle *RateLimitEngineImpl) EvaluateRules(
	ctx context.Context,
	payload interface{},
	metadata map[string]interface{},
) (*RateLimitDecision, error) {
	rle.mu.RLock()
	defer rle.mu.RUnlock()

	// Collect matching rules by priority
	var matchingRules []*RateLimitRule
	for _, rule := range rle.rules {
		// Check condition
		if rule.Condition != nil {
			matches, err := rle.conditionEngine.Evaluate(rule.Condition, payload)
			if err != nil {
				rle.logger.WarnContext(ctx, "Error evaluating rate limit condition",
					"rule_id", rule.ID,
					"error", err,
				)
				continue
			}
			if !matches {
				continue
			}
		}

		matchingRules = append(matchingRules, rule)
	}

	// Sort by priority (lower number = higher priority)
	sortRulesByPriority(matchingRules)

	// Evaluate rules in order
	for _, rule := range matchingRules {
		decision := rle.evaluateRule(rule)
		// Return on first matching rule result (allowed or denied)
		return decision, nil
	}

	// No rules matched - allow by default
	return &RateLimitDecision{
		Allowed: true,
		Reason:  "No rate limit rules matched",
	}, nil
}

// evaluateRule checks a single rule against current state
func (rle *RateLimitEngineImpl) evaluateRule(rule *RateLimitRule) *RateLimitDecision {
	state, ok := rle.state[rule.ID]
	if !ok {
		// Rule state not found - shouldn't happen
		return &RateLimitDecision{
			Allowed: true,
			Reason:  "Rule state not found",
			RuleID:  rule.ID,
		}
	}

	switch rule.Strategy {
	case "time_window":
		return rle.checkTimeWindow(rule, state)
	case "concurrent":
		return rle.checkConcurrent(rule, state)
	case "token_bucket":
		return rle.checkTokenBucket(rule, state)
	default:
		return &RateLimitDecision{
			Allowed: true,
			Reason:  "Unknown strategy",
			RuleID:  rule.ID,
		}
	}
}

// checkTimeWindow evaluates time-window rate limiting
func (rle *RateLimitEngineImpl) checkTimeWindow(rule *RateLimitRule, state *RateLimitState) *RateLimitDecision {
	state.windowMutex.Lock()
	defer state.windowMutex.Unlock()

	now := time.Now()
	windowDuration := time.Duration(rule.WindowDurationSeconds) * time.Second

	// Check if current window has expired
	if now.Sub(state.windowStart) >= windowDuration {
		// Start new window
		state.windowStart = now
		state.windowMessageCount = 1
		return &RateLimitDecision{
			Allowed:         true,
			Reason:          fmt.Sprintf("Within limit: 1/%d in new window", rule.MaxMessagesPerWindow),
			RuleID:          rule.ID,
			StrategyUsed:    rule.Strategy,
			Current:         1,
			Limit:           rule.MaxMessagesPerWindow,
			WindowOrSeconds: rule.WindowDurationSeconds,
		}
	}

	// Within current window - check limit
	if state.windowMessageCount < rule.MaxMessagesPerWindow {
		state.windowMessageCount++
		return &RateLimitDecision{
			Allowed:         true,
			Reason:          fmt.Sprintf("Within limit: %d/%d", state.windowMessageCount, rule.MaxMessagesPerWindow),
			RuleID:          rule.ID,
			StrategyUsed:    rule.Strategy,
			Current:         state.windowMessageCount,
			Limit:           rule.MaxMessagesPerWindow,
			WindowOrSeconds: rule.WindowDurationSeconds,
		}
	}

	// Limit exceeded - determine action
	timeUntilReset := windowDuration - now.Sub(state.windowStart)
	return &RateLimitDecision{
		Allowed:         false,
		Action:          rule.ExceedAction,
		Reason:          fmt.Sprintf("Rate limit exceeded: %d/%d in window, reset in %vs", state.windowMessageCount, rule.MaxMessagesPerWindow, timeUntilReset.Seconds()),
		RuleID:          rule.ID,
		StrategyUsed:    rule.Strategy,
		Current:         state.windowMessageCount,
		Limit:           rule.MaxMessagesPerWindow,
		WindowOrSeconds: rule.WindowDurationSeconds,
	}
}

// checkConcurrent evaluates concurrent message rate limiting
func (rle *RateLimitEngineImpl) checkConcurrent(rule *RateLimitRule, state *RateLimitState) *RateLimitDecision {
	state.concurrentMutex.Lock()
	defer state.concurrentMutex.Unlock()

	// Check if limit exceeded
	if state.concurrentCount >= rule.MaxConcurrent {
		return &RateLimitDecision{
			Allowed:      false,
			Action:       rule.ExceedAction,
			Reason:       fmt.Sprintf("Concurrent limit exceeded: %d/%d", state.concurrentCount, rule.MaxConcurrent),
			RuleID:       rule.ID,
			StrategyUsed: rule.Strategy,
			Current:      state.concurrentCount,
			Limit:        rule.MaxConcurrent,
		}
	}

	// Increment concurrent count
	state.concurrentCount++
	return &RateLimitDecision{
		Allowed:      true,
		Reason:       fmt.Sprintf("Within concurrent limit: %d/%d", state.concurrentCount, rule.MaxConcurrent),
		RuleID:       rule.ID,
		StrategyUsed: rule.Strategy,
		Current:      state.concurrentCount,
		Limit:        rule.MaxConcurrent,
	}
}

// checkTokenBucket evaluates token bucket rate limiting
func (rle *RateLimitEngineImpl) checkTokenBucket(rule *RateLimitRule, state *RateLimitState) *RateLimitDecision {
	state.tokenBucketMutex.Lock()
	defer state.tokenBucketMutex.Unlock()

	now := time.Now()
	timeSinceRefill := now.Sub(state.lastTokenRefillTime).Seconds()
	tokensToAdd := float64(rule.TokenBucketRate) * timeSinceRefill

	// Add new tokens
	state.tokensAvailable += tokensToAdd
	if state.tokensAvailable > float64(rule.TokenBucketCapacity) {
		state.tokensAvailable = float64(rule.TokenBucketCapacity)
	}

	state.lastTokenRefillTime = now

	// Check if tokens available
	if state.tokensAvailable >= 1.0 {
		state.tokensAvailable -= 1.0
		return &RateLimitDecision{
			Allowed:         true,
			Reason:          fmt.Sprintf("Token available: %.2f remaining", state.tokensAvailable),
			RuleID:          rule.ID,
			StrategyUsed:    rule.Strategy,
			Current:         int(state.tokensAvailable),
			Limit:           rule.TokenBucketCapacity,
			WindowOrSeconds: rule.TokenBucketRate,
		}
	}

	// No tokens available
	return &RateLimitDecision{
		Allowed:         false,
		Action:          rule.ExceedAction,
		Reason:          fmt.Sprintf("No tokens available: %.2f (need 1.0)", state.tokensAvailable),
		RuleID:          rule.ID,
		StrategyUsed:    rule.Strategy,
		Current:         int(state.tokensAvailable),
		Limit:           rule.TokenBucketCapacity,
		WindowOrSeconds: rule.TokenBucketRate,
	}
}

// RecordMessageComplete notifies that a message has been published (for concurrent limiting)
func (rle *RateLimitEngineImpl) RecordMessageComplete(ruleID string) error {
	rle.mu.RLock()
	rule, exists := rle.rules[ruleID]
	if !exists {
		rle.mu.RUnlock()
		return fmt.Errorf("rule not found: %s", ruleID)
	}

	state, exists := rle.state[ruleID]
	if !exists {
		rle.mu.RUnlock()
		return fmt.Errorf("rule state not found: %s", ruleID)
	}
	rle.mu.RUnlock()

	// Only decrement for concurrent strategy
	if rule.Strategy == "concurrent" {
		state.concurrentMutex.Lock()
		if state.concurrentCount > 0 {
			state.concurrentCount--
		}
		state.concurrentMutex.Unlock()
	}

	return nil
}

// GetStats returns statistics for a rule
func (rle *RateLimitEngineImpl) GetStats(ruleID string) *RateLimitStats {
	rle.mu.RLock()
	rule, exists := rle.rules[ruleID]
	if !exists {
		rle.mu.RUnlock()
		return nil
	}

	state, exists := rle.state[ruleID]
	if !exists {
		rle.mu.RUnlock()
		return nil
	}
	rle.mu.RUnlock()

	stats := &RateLimitStats{
		RuleID:   ruleID,
		Strategy: rule.Strategy,
	}

	switch rule.Strategy {
	case "time_window":
		state.windowMutex.Lock()
		stats.CurrentValue = state.windowMessageCount
		stats.WindowResetTime = state.windowStart.Add(time.Duration(rule.WindowDurationSeconds) * time.Second)
		state.windowMutex.Unlock()
		stats.Limit = rule.MaxMessagesPerWindow

	case "concurrent":
		state.concurrentMutex.Lock()
		stats.CurrentValue = state.concurrentCount
		state.concurrentMutex.Unlock()
		stats.Limit = rule.MaxConcurrent

	case "token_bucket":
		state.tokenBucketMutex.Lock()
		stats.CurrentTokens = state.tokensAvailable
		stats.TokenRefillTime = state.lastTokenRefillTime.Add(time.Second)
		state.tokenBucketMutex.Unlock()
		stats.Limit = rule.TokenBucketCapacity
	}

	return stats
}

// GetQueuedMessages retrieves queued messages for a rule that are ready to retry
func (rle *RateLimitEngineImpl) GetQueuedMessages(ruleID string, maxMessages int) []*QueuedMessage {
	rle.mu.RLock()
	state, exists := rle.state[ruleID]
	rle.mu.RUnlock()

	if !exists || state.queue == nil {
		return nil
	}

	var messages []*QueuedMessage
	for i := 0; i < maxMessages; i++ {
		msg := state.queue.Dequeue()
		if msg == nil {
			break
		}
		messages = append(messages, msg)
	}

	return messages
}

// queueWorker processes queued messages in the background
func (rle *RateLimitEngineImpl) queueWorker() {
	defer close(rle.queueWorkerDone)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-rle.queueWorkerCtx.Done():
			return
		case <-ticker.C:
			// Process queued messages for each rule
			rle.mu.RLock()
			for ruleID, state := range rle.state {
				if state.queue != nil && state.queue.Size() > 0 {
					rle.logger.DebugContext(rle.queueWorkerCtx, "Queue processing",
						"rule_id", ruleID,
						"queue_size", state.queue.Size(),
					)
				}
			}
			rle.mu.RUnlock()
		}
	}
}

// Stop gracefully shuts down the rate limit engine
func (rle *RateLimitEngineImpl) Stop() error {
	rle.queueWorkerStop()

	// Wait for queue worker to finish
	select {
	case <-rle.queueWorkerDone:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("queue worker shutdown timeout")
	}
}

// validateRateLimitRule validates a rate limit rule configuration
func validateRateLimitRule(rule *RateLimitRule) error {
	// Validate strategy
	validStrategies := map[string]bool{
		"time_window":  true,
		"concurrent":   true,
		"token_bucket": true,
	}

	if !validStrategies[rule.Strategy] {
		return fmt.Errorf("invalid strategy '%s': must be one of time_window, concurrent, token_bucket", rule.Strategy)
	}

	// Validate exceed action
	validActions := map[string]bool{
		"queue":  true,
		"drop":   true,
		"reject": true,
	}

	if !validActions[rule.ExceedAction] {
		return fmt.Errorf("invalid exceed_action '%s': must be one of queue, drop, reject", rule.ExceedAction)
	}

	// Validate strategy-specific fields
	strategyCount := 0
	if rule.MaxMessagesPerWindow > 0 {
		strategyCount++
		if rule.WindowDurationSeconds <= 0 {
			return fmt.Errorf("window_duration_seconds must be > 0 for time_window strategy")
		}
	}
	if rule.MaxConcurrent > 0 {
		strategyCount++
	}
	if rule.TokenBucketRate > 0 {
		strategyCount++
		if rule.TokenBucketCapacity <= 0 {
			return fmt.Errorf("token_bucket_capacity must be > 0 for token_bucket strategy")
		}
	}

	// Exactly one strategy must be configured
	if strategyCount == 0 {
		return fmt.Errorf("at least one strategy must be configured (max_messages_per_window, max_concurrent, or token_bucket_rate)")
	}
	if strategyCount > 1 {
		return fmt.Errorf("only one strategy can be configured per rule")
	}

	// Validate queue size if using queue action
	if rule.ExceedAction == "queue" && rule.QueueSize <= 0 {
		return fmt.Errorf("queue_size must be > 0 when exceed_action is queue")
	}

	return nil
}

// sortRulesByPriority sorts rules by priority (lower number = first)
func sortRulesByPriority(rules []*RateLimitRule) {
	for i := 0; i < len(rules); i++ {
		for j := i + 1; j < len(rules); j++ {
			if rules[j].Priority < rules[i].Priority {
				rules[i], rules[j] = rules[j], rules[i]
			}
		}
	}
}
