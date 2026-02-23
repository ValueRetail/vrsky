package converter

import (
	"context"
	"fmt"
)

// Function is a callable function that can be registered with the registry.
// Phase 2F includes stub implementations; Phase 3 will implement real functionality.
type Function func(ctx context.Context, args ...interface{}) (interface{}, error)

// FunctionRegistry manages custom functions available in expressions.
// This allows Phase 3 to register built-in functions without modifying the evaluator.
type FunctionRegistry struct {
	functions     map[string]Function
	logger        Logger
	ctx           context.Context
	lookupBackend LookupBackend
}

// NewFunctionRegistry creates a new function registry with stubs for Phase 3 functions.
func NewFunctionRegistry(ctx context.Context, logger Logger) *FunctionRegistry {
	if ctx == nil {
		ctx = context.Background()
	}

	fr := &FunctionRegistry{
		functions: make(map[string]Function),
		logger:    logger,
		ctx:       ctx,
	}

	// Register Phase 2F stub functions (Phase 3 will implement real versions)
	fr.registerStubs()

	return fr
}

// registerStubs registers placeholder functions for Phase 3 implementation.
func (fr *FunctionRegistry) registerStubs() {
	// Aggregation functions
	fr.Register("sum", sumFunc)
	fr.Register("avg", avgFunc)
	fr.Register("count", countFunc)
	fr.Register("max", maxFunc)
	fr.Register("min", minFunc)

	// String functions
	fr.Register("concat", concatFunc)
	fr.Register("uppercase", uppercaseFunc)
	fr.Register("lowercase", lowercaseFunc)
	fr.Register("trim", trimFunc)
	fr.Register("split", splitFunc)
	fr.Register("replace", replaceFunc)

	// Math functions
	fr.Register("multiply", multiplyFunc)
	fr.Register("divide", divideFunc)

	// Type conversion functions
	fr.Register("as_string", asStringFunc)
	fr.Register("as_number", asNumberFunc)

	// Date/Time functions
	fr.Register("now", nowFunc)
	fr.Register("date_format", dateFormatFunc)
	fr.Register("date_add", dateAddFunc)

	// Lookup functions (mock implementations - Phase 3)
	mockBackend := NewMockLookupBackend()
	fr.Register("lookup", newLookupFunc(mockBackend))
	fr.Register("http_lookup", newHTTPLookupFunc(mockBackend))

	// Legacy lookup functions (for backward compatibility)
	fr.Register("lookup_customer_account", stubLookupCustomerAccount)
	fr.Register("get_product_info", stubGetProductInfo)
	fr.Register("get_inventory_level", stubGetInventoryLevel)
	fr.Register("get_pricing", stubGetPricing)
	fr.Register("lookup_tax_rate", stubLookupTaxRate)
}

// Register registers a custom function with the registry.
func (fr *FunctionRegistry) Register(name string, fn Function) error {
	if name == "" {
		return fmt.Errorf("function name cannot be empty")
	}
	if fn == nil {
		return fmt.Errorf("function cannot be nil")
	}
	fr.functions[name] = fn
	if fr.logger != nil {
		fr.logger.InfoContext(fr.ctx, "registered function", "name", name)
	}
	return nil
}

// Call invokes a registered function.
func (fr *FunctionRegistry) Call(name string, args ...interface{}) (interface{}, error) {
	fn, exists := fr.functions[name]
	if !exists {
		return nil, fmt.Errorf("function not found: %s", name)
	}
	return fn(fr.ctx, args...)
}

// Exists checks if a function is registered.
func (fr *FunctionRegistry) Exists(name string) bool {
	_, exists := fr.functions[name]
	return exists
}

// SetLookupBackend sets the lookup backend for database and HTTP lookup functions.
func (fr *FunctionRegistry) SetLookupBackend(backend LookupBackend) {
	if backend != nil {
		fr.lookupBackend = backend
		// Re-register lookup functions with new backend
		fr.Register("lookup", newLookupFunc(backend))
		fr.Register("http_lookup", newHTTPLookupFunc(backend))
	}
}

// =============================================================================
// Phase 2F Stub Functions (to be implemented in Phase 3)
// =============================================================================

// Lookup functions (stubs)
func stubLookupCustomerAccount(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("lookup_customer_account requires at least 1 argument")
	}
	// Phase 3: Implement database lookup
	return map[string]interface{}{
		"id":      "STUB_CUSTOMER_ID",
		"name":    "Stub Customer",
		"account": "STUB_ACCOUNT",
	}, nil
}

func stubGetProductInfo(ctx context.Context, args ...interface{}) (interface{}, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("get_product_info requires at least 1 argument")
	}
	// Phase 3: Implement product database lookup
	return map[string]interface{}{
		"sku":   "STUB_SKU",
		"name":  "Stub Product",
		"price": 0.0,
	}, nil
}

func stubGetInventoryLevel(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement inventory system lookup
	return float64(0), nil
}

func stubGetPricing(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement pricing engine integration
	return map[string]interface{}{
		"basePrice":     0.0,
		"discountPrice": 0.0,
		"currencyCode":  "USD",
		"effectiveDate": "",
	}, nil
}

func stubLookupTaxRate(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement tax rate lookup
	return 0.0, nil
}

// Aggregation functions (stubs)
func stubSum(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement array summation
	return float64(0), nil
}

func stubAvg(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement array average
	return float64(0), nil
}

func stubCount(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement array count
	return float64(0), nil
}

func stubMax(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement array maximum
	return float64(0), nil
}

func stubMin(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement array minimum
	return float64(0), nil
}

// String functions (stubs)
func stubConcat(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement string concatenation
	return "", nil
}

func stubUppercase(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement uppercase conversion
	return "", nil
}

func stubLowercase(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement lowercase conversion
	return "", nil
}

func stubTrim(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement string trimming
	return "", nil
}

func stubSplit(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement string splitting
	return []interface{}{}, nil
}

func stubReplace(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement string replacement
	return "", nil
}

// Date/Time functions (stubs)
func stubNow(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Return current timestamp
	return "", nil
}

func stubDateFormat(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement date formatting
	return "", nil
}

func stubDateAdd(ctx context.Context, args ...interface{}) (interface{}, error) {
	// Phase 3: Implement date arithmetic
	return "", nil
}
