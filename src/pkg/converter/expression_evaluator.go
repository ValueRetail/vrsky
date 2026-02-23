package converter

import (
	"context"
	"fmt"
	"sync"

	exprpkg "github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// ExpressionEvaluator evaluates expressions against a variable context.
// Uses expr-lang/expr for safe, efficient expression evaluation.
// Thread-safe with mutex protection for the compiled expression cache.
type ExpressionEvaluator struct {
	functionRegistry *FunctionRegistry
	logger           Logger
	ctx              context.Context
	mu               sync.RWMutex
	compiled         map[string]*vm.Program
}

// NewExpressionEvaluator creates a new expression evaluator.
func NewExpressionEvaluator(ctx context.Context, logger Logger, functionRegistry *FunctionRegistry) *ExpressionEvaluator {
	if ctx == nil {
		ctx = context.Background()
	}

	return &ExpressionEvaluator{
		functionRegistry: functionRegistry,
		logger:           logger,
		ctx:              ctx,
		compiled:         make(map[string]*vm.Program),
	}
}

// Evaluate evaluates an expression against the provided variables.
// Variables should be a map of field names to values extracted from the JSON payload.
// Returns the result or an error if evaluation fails.
func (ee *ExpressionEvaluator) Evaluate(expression string, variables map[string]interface{}) (interface{}, error) {
	if expression == "" {
		return nil, fmt.Errorf("expression cannot be empty")
	}

	if variables == nil {
		variables = make(map[string]interface{})
	}

	// Try to compile and cache the expression
	program, err := ee.getCompiledProgram(expression, variables)
	if err != nil {
		if ee.logger != nil {
			ee.logger.ErrorContext(ee.ctx, "failed to compile expression", "expression", expression, "error", err.Error())
		}
		return nil, fmt.Errorf("failed to compile expression: %w", err)
	}

	// Run the compiled program
	result, err := exprpkg.Run(program, variables)
	if err != nil {
		if ee.logger != nil {
			ee.logger.ErrorContext(ee.ctx, "failed to evaluate expression", "expression", expression, "error", err.Error())
		}
		return nil, fmt.Errorf("failed to evaluate expression: %w", err)
	}

	return result, nil
}

// getCompiledProgram retrieves or compiles an expression program.
// Caches compiled programs to avoid recompilation.
// Thread-safe with read lock for cache hits and write lock for misses.
func (ee *ExpressionEvaluator) getCompiledProgram(expression string, variables map[string]interface{}) (*vm.Program, error) {
	// Check cache first with read lock
	ee.mu.RLock()
	if program, exists := ee.compiled[expression]; exists {
		ee.mu.RUnlock()
		return program, nil
	}
	ee.mu.RUnlock()

	// Compile the expression with the environment
	program, err := exprpkg.Compile(
		expression,
		exprpkg.Env(variables),
	)
	if err != nil {
		return nil, err
	}

	// Cache the compiled program with write lock
	ee.mu.Lock()
	defer ee.mu.Unlock()
	ee.compiled[expression] = program

	return program, nil
}

// EvaluateCondition evaluates a condition expression.
// Returns a boolean result or an error.
func (ee *ExpressionEvaluator) EvaluateCondition(expression string, variables map[string]interface{}) (bool, error) {
	if expression == "" {
		// Empty condition is always true (no filtering)
		return true, nil
	}

	result, err := ee.Evaluate(expression, variables)
	if err != nil {
		return false, err
	}

	// Convert result to boolean
	switch v := result.(type) {
	case bool:
		return v, nil
	case nil:
		return false, nil
	case string:
		return v != "", nil
	case float64:
		return v != 0, nil
	default:
		// Non-nil values are truthy
		return true, nil
	}
}

// ClearCache clears the compiled expression cache.
// Use when you want to free memory or reset state.
// Thread-safe operation.
func (ee *ExpressionEvaluator) ClearCache() {
	ee.mu.Lock()
	defer ee.mu.Unlock()
	ee.compiled = make(map[string]*vm.Program)
}

// SupportedOperators returns documentation for supported operators.
// Useful for API documentation or user guidance.
func (ee *ExpressionEvaluator) SupportedOperators() map[string]string {
	return map[string]string{
		"Arithmetic": "    + (addition), - (subtraction), * (multiplication), / (division), % (modulo)",
		"Comparison": "    == (equal), != (not equal), < (less), <= (less/equal), > (greater), >= (greater/equal)",
		"Logical":    "    && (and), || (or), ! (not)",
		"Array":      "    [index] (indexing), . (field access), #() (filtering), all(), any(), map(), filter()",
		"String":     "    + (concatenation), contains(), startsWith(), endsWith(), matches()",
		"Type":       "    Automatic type coercion, null handling",
	}
}
