package converter

import (
	"context"
	"fmt"
	"sync"
)

// Function is a callable function that can be registered with the registry.
// Phase 2F includes stub implementations; Phase 3 will implement real functionality.
type Function func(ctx context.Context, args ...interface{}) (interface{}, error)

// FunctionRegistry manages custom functions available in expressions.
// This allows Phase 3 to register built-in functions without modifying the evaluator.
// Phase 3.5 Iteration 3 adds WASM plugin support.
type FunctionRegistry struct {
	functions     map[string]Function
	logger        Logger
	ctx           context.Context
	lookupBackend LookupBackend
	wasmLoader    *WASMFunctionLoader
	wasmFunctions map[string]*WASMModuleFunction
	mu            sync.RWMutex
}

// WASMModuleFunction maps a function name to its WASM module and export
type WASMModuleFunction struct {
	ModuleName string
	ExportName string
}

// NewFunctionRegistry creates a new function registry with stubs for Phase 3 functions.
func NewFunctionRegistry(ctx context.Context, logger Logger) *FunctionRegistry {
	if ctx == nil {
		ctx = context.Background()
	}

	fr := &FunctionRegistry{
		functions:     make(map[string]Function),
		logger:        logger,
		ctx:           ctx,
		wasmLoader:    NewWASMFunctionLoader(ctx, logger),
		wasmFunctions: make(map[string]*WASMModuleFunction),
	}

	// Register Phase 2F stub functions (Phase 3 will implement real versions)
	fr.registerStubs()

	return fr
}

// registerStubs registers placeholder functions for Phase 3 implementation.
func (fr *FunctionRegistry) registerStubs() {
	// Aggregation functions
	_ = fr.Register("sum", sumFunc)
	_ = fr.Register("avg", avgFunc)
	_ = fr.Register("count", countFunc)
	_ = fr.Register("max", maxFunc)
	_ = fr.Register("min", minFunc)

	// String functions
	_ = fr.Register("concat", concatFunc)
	_ = fr.Register("uppercase", uppercaseFunc)
	_ = fr.Register("lowercase", lowercaseFunc)
	_ = fr.Register("trim", trimFunc)
	_ = fr.Register("split", splitFunc)
	_ = fr.Register("replace", replaceFunc)

	// Math functions
	_ = fr.Register("multiply", multiplyFunc)
	_ = fr.Register("divide", divideFunc)

	// Type conversion functions
	_ = fr.Register("as_string", asStringFunc)
	_ = fr.Register("as_number", asNumberFunc)

	// Date/Time functions
	_ = fr.Register("now", nowFunc)
	_ = fr.Register("date_format", dateFormatFunc)
	_ = fr.Register("date_add", dateAddFunc)

	// Lookup functions (mock implementations - Phase 3)
	mockBackend := NewMockLookupBackend()
	_ = fr.Register("lookup", newLookupFunc(mockBackend))
	_ = fr.Register("http_lookup", newHTTPLookupFunc(mockBackend))

	// Legacy lookup functions (for backward compatibility)
	_ = fr.Register("lookup_customer_account", stubLookupCustomerAccount)
	_ = fr.Register("get_product_info", stubGetProductInfo)
	_ = fr.Register("get_inventory_level", stubGetInventoryLevel)
	_ = fr.Register("get_pricing", stubGetPricing)
	_ = fr.Register("lookup_tax_rate", stubLookupTaxRate)
}

// Register registers a custom function with the registry.
func (fr *FunctionRegistry) Register(name string, fn Function) error {
	if name == "" {
		return fmt.Errorf("function name cannot be empty")
	}
	if fn == nil {
		return fmt.Errorf("function cannot be nil")
	}

	fr.mu.Lock()
	defer fr.mu.Unlock()

	fr.functions[name] = fn
	if fr.logger != nil {
		fr.logger.InfoContext(fr.ctx, "registered function", "name", name)
	}
	return nil
}

// Call invokes a registered function.
// Phase 3.5 Iteration 3: Checks WASM functions first, then built-in functions
func (fr *FunctionRegistry) Call(name string, args ...interface{}) (interface{}, error) {
	// First, check for a matching WASM function and retrieve the module under lock
	// to prevent race conditions where wasmLoader could be modified after lock release.
	fr.mu.RLock()

	wasmFunc, isWASM := fr.wasmFunctions[name]
	var module *WASMModule
	useWASM := isWASM && fr.wasmLoader != nil
	if useWASM {
		// Retrieve the module while holding the lock to ensure wasmLoader state is consistent
		module = fr.wasmLoader.GetModule(wasmFunc.ModuleName)
	}
	fr.mu.RUnlock()

	// Check WASM functions first using the module retrieved under lock.
	// The module reference is safe to use even after lock release because it's a snapshot
	if useWASM && module != nil {
		result := module.Call(fr.ctx, wasmFunc.ExportName, args...)
		if result != nil {
			return result, nil
		}
		// Graceful degradation: WASM returned nil, continue to built-in
	}

	// Check built-in functions
	fr.mu.RLock()
	fn, exists := fr.functions[name]
	fr.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("function not found: %s", name)
	}
	return fn(fr.ctx, args...)
}

// InitializeWASM initializes the WASM plugin loader with a base directory
// All .wasm files in the directory will be loaded and made available
func (fr *FunctionRegistry) InitializeWASM(pluginDir string) error {
	if pluginDir == "" {
		return nil // WASM support is optional
	}

	if fr.wasmLoader == nil {
		fr.wasmLoader = NewWASMFunctionLoader(fr.ctx, fr.logger)
	}

	// Note: In a production system, we'd scan the directory for .wasm files
	// For now, this is a placeholder for future directory scanning
	if fr.logger != nil {
		fr.logger.InfoContext(fr.ctx, "WASM plugin directory initialized", "path", pluginDir)
	}

	return nil
}

// RegisterWASM registers a WASM module and maps a function name to its export
// functionName: name to use in expressions (e.g., "calculate_discount")
// modulePath: path to .wasm binary file
// exportName: name of exported function in WASM module (e.g., "calculate_discount")
func (fr *FunctionRegistry) RegisterWASM(functionName, modulePath, exportName string) error {
	if functionName == "" {
		return fmt.Errorf("function name cannot be empty")
	}
	if modulePath == "" {
		return fmt.Errorf("module path cannot be empty")
	}
	if exportName == "" {
		return fmt.Errorf("export name cannot be empty")
	}

	fr.mu.Lock()
	defer fr.mu.Unlock()

	if fr.wasmLoader == nil {
		return fmt.Errorf("WASM loader not initialized")
	}

	// Use module path as module name (unique identifier)
	moduleName := modulePath

	// Try to load module if not already loaded
	module := fr.wasmLoader.GetModule(moduleName)
	if module == nil {
		err := fr.wasmLoader.LoadFromFile(fr.ctx, moduleName, modulePath)
		if err != nil {
			if fr.logger != nil {
				fr.logger.ErrorContext(fr.ctx, "failed to load WASM module", "function", functionName, "module", moduleName, "error", err.Error())
			}
			return fmt.Errorf("failed to load WASM module: %w", err)
		}
	}

	// Register function mapping
	fr.wasmFunctions[functionName] = &WASMModuleFunction{
		ModuleName: moduleName,
		ExportName: exportName,
	}

	if fr.logger != nil {
		fr.logger.InfoContext(fr.ctx, "WASM function registered", "function", functionName, "module", moduleName, "export", exportName)
	}

	return nil
}

// UnregisterWASM unregisters a WASM function
func (fr *FunctionRegistry) UnregisterWASM(functionName string) error {
	fr.mu.Lock()
	defer fr.mu.Unlock()

	if _, exists := fr.wasmFunctions[functionName]; !exists {
		return fmt.Errorf("WASM function not registered: %s", functionName)
	}

	delete(fr.wasmFunctions, functionName)

	if fr.logger != nil {
		fr.logger.InfoContext(fr.ctx, "WASM function unregistered", "function", functionName)
	}

	return nil
}

// CloseWASM shuts down the WASM plugin system and releases all resources
func (fr *FunctionRegistry) CloseWASM() error {
	fr.mu.Lock()
	defer fr.mu.Unlock()

	if fr.wasmLoader != nil {
		err := fr.wasmLoader.UnloadAll()
		if err != nil {
			if fr.logger != nil {
				fr.logger.ErrorContext(fr.ctx, "error closing WASM loader", "error", err.Error())
			}
			return err
		}
		fr.wasmFunctions = make(map[string]*WASMModuleFunction)
	}

	return nil
}

// Exists checks if a function is registered (built-in or WASM).
func (fr *FunctionRegistry) Exists(name string) bool {
	fr.mu.RLock()
	defer fr.mu.RUnlock()

	// Check WASM functions first
	if _, exists := fr.wasmFunctions[name]; exists {
		return true
	}

	// Check built-in functions
	_, exists := fr.functions[name]
	return exists
}

// SetLookupBackend sets the lookup backend for database and HTTP lookup functions.
func (fr *FunctionRegistry) SetLookupBackend(backend LookupBackend) {
	if backend != nil {
		fr.mu.Lock()
		fr.lookupBackend = backend
		fr.mu.Unlock()

		// Re-register lookup functions with new backend
		_ = fr.Register("lookup", newLookupFunc(backend))
		_ = fr.Register("http_lookup", newHTTPLookupFunc(backend))
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
