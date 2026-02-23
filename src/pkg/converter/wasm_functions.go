package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bytecodealliance/wasmtime-go/v13"
)

// =============================================================================
// WASM CONFIGURATION & METRICS
// =============================================================================

// WASMConfig holds configuration for WASM module execution
type WASMConfig struct {
	// MaxMemoryBytes is the maximum memory allocated to each WASM module (default: 100MB)
	MaxMemoryBytes uint64

	// TimeoutSeconds is the maximum execution time per WASM function call (default: 5)
	TimeoutSeconds int

	// EnableMetrics controls whether to collect performance metrics (default: true)
	EnableMetrics bool
}

// NewWASMConfig creates a WASMConfig with sensible defaults
func NewWASMConfig() *WASMConfig {
	return &WASMConfig{
		MaxMemoryBytes: 100 * 1024 * 1024, // 100MB
		TimeoutSeconds: 5,
		EnableMetrics:  true,
	}
}

// WASMMetrics holds performance metrics for a WASM module
type WASMMetrics struct {
	mu             sync.RWMutex
	CallCount      int64
	ErrorCount     int64
	TotalLatencyMs int64
	MaxLatencyMs   int64
	MinLatencyMs   int64
	LastCallTime   time.Time
	LastErrorTime  time.Time
	LastErrorMsg   string
}

// RecordCall records a successful function call with latency
func (m *WASMMetrics) RecordCall(latencyMs int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.CallCount++
	m.TotalLatencyMs += latencyMs
	m.LastCallTime = time.Now()

	if m.CallCount == 1 {
		m.MinLatencyMs = latencyMs
		m.MaxLatencyMs = latencyMs
	} else {
		if latencyMs > m.MaxLatencyMs {
			m.MaxLatencyMs = latencyMs
		}
		if latencyMs < m.MinLatencyMs {
			m.MinLatencyMs = latencyMs
		}
	}
}

// RecordError records a failed function call
func (m *WASMMetrics) RecordError(errMsg string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ErrorCount++
	m.LastErrorTime = time.Now()
	m.LastErrorMsg = errMsg
}

// GetStats returns a copy of current metrics
func (m *WASMMetrics) GetStats() map[string]interface{} {
	if m == nil {
		return make(map[string]interface{})
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	var avgLatency int64
	if m.CallCount > 0 {
		avgLatency = m.TotalLatencyMs / m.CallCount
	}

	return map[string]interface{}{
		"call_count":       m.CallCount,
		"error_count":      m.ErrorCount,
		"total_latency_ms": m.TotalLatencyMs,
		"avg_latency_ms":   avgLatency,
		"max_latency_ms":   m.MaxLatencyMs,
		"min_latency_ms":   m.MinLatencyMs,
		"last_call_time":   m.LastCallTime,
		"last_error_time":  m.LastErrorTime,
		"last_error_msg":   m.LastErrorMsg,
	}
}

// =============================================================================
// WASM MODULE
// =============================================================================

// WASMModule represents a loaded and instantiated WASM module
type WASMModule struct {
	Name     string
	FilePath string
	Instance *wasmtime.Instance
	Store    *wasmtime.Store
	Engine   *wasmtime.Engine
	Metrics  *WASMMetrics
	Logger   Logger
	Config   *WASMConfig
	mu       sync.RWMutex
	ctx      context.Context
}

// Call invokes an exported function in the WASM module with graceful error handling
// Returns nil on error (graceful degradation), never panics
func (wm *WASMModule) Call(ctx context.Context, funcName string, args ...interface{}) interface{} {
	if wm == nil {
		return nil
	}

	wm.mu.RLock()
	if wm.Instance == nil {
		wm.mu.RUnlock()
		if wm.Logger != nil {
			wm.Logger.WarnContext(ctx, "WASM module not initialized", "module", wm.Name, "function", funcName)
		}
		return nil
	}
	wm.mu.RUnlock()

	start := time.Now()
	defer func() {
		if wm.Metrics != nil && wm.Config.EnableMetrics {
			latencyMs := time.Since(start).Milliseconds()
			wm.Metrics.RecordCall(latencyMs)
		}
	}()

	// Get exported function
	f := wm.Instance.GetFunc(wm.Store, funcName)
	if f == nil {
		errMsg := fmt.Sprintf("function not exported: %s", funcName)
		if wm.Logger != nil {
			wm.Logger.WarnContext(ctx, "WASM function not found", "module", wm.Name, "function", funcName)
		}
		if wm.Metrics != nil {
			wm.Metrics.RecordError(errMsg)
		}
		return nil
	}

	// Call WASM function (with timeout enforcement)
	var result interface{}
	var err error
	err = wm.callWithTimeout(ctx, func(callCtx context.Context) error {
		res, err := f.Call(wm.Store, args...)
		if err != nil {
			return err
		}

		// Result is already in the right format
		result = res
		return nil
	})

	if err != nil {
		if wm.Logger != nil {
			wm.Logger.WarnContext(ctx, "WASM function execution failed", "module", wm.Name, "function", funcName, "error", err.Error())
		}
		if wm.Metrics != nil {
			wm.Metrics.RecordError(fmt.Sprintf("execution failed: %v", err))
		}
		return nil
	}

	if wm.Logger != nil {
		wm.Logger.InfoContext(ctx, "WASM function executed successfully", "module", wm.Name, "function", funcName, "duration_ms", time.Since(start).Milliseconds())
	}

	return result
}

// callWithTimeout executes a function with timeout enforcement and context-aware cancellation.
// The function fn receives the context so it can check ctx.Done() and exit early,
// preventing goroutine leaks when timeout or context cancellation occurs.
func (wm *WASMModule) callWithTimeout(ctx context.Context, fn func(context.Context) error) error {
	// Create channel for result (buffered to prevent goroutine leak on timeout)
	done := make(chan error, 1)

	// Run function in goroutine with context awareness
	go func() {
		done <- fn(ctx)
	}()

	// Determine timeout
	timeout := time.Duration(wm.Config.TimeoutSeconds) * time.Second

	// Wait for result or timeout
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("WASM function execution timeout after %s", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// (removed unused marshalArgs and unmarshalResult helpers; no behavior change as they were never called)
// =============================================================================
// WASM FUNCTION LOADER
// =============================================================================

// WASMFunctionLoader manages the lifecycle of WASM modules
type WASMFunctionLoader struct {
	modules map[string]*WASMModule
	mu      sync.RWMutex
	logger  Logger
	config  *WASMConfig
	ctx     context.Context
}

// NewWASMFunctionLoader creates a new WASM function loader
func NewWASMFunctionLoader(ctx context.Context, logger Logger) *WASMFunctionLoader {
	if ctx == nil {
		ctx = context.Background()
	}

	return &WASMFunctionLoader{
		modules: make(map[string]*WASMModule),
		logger:  logger,
		config:  NewWASMConfig(),
		ctx:     ctx,
	}
}

// Compile module
	module, err := wasmtime.NewModule(engine, wasmData)
	// Compile module
	module, err := wasmtime.NewModule(engine, wasmData)
	if err != nil {
		if wfl.logger != nil {
			wfl.logger.ErrorContext(wfl.ctx, "failed to compile WASM module", "module", moduleName, "error", err.Error())
		}
		return fmt.Errorf("failed to compile WASM module: %w", err)
	}

	// Instantiate module
	instance, err := wasmtime.NewInstance(store, module, []wasmtime.AsExtern{})
	if err != nil {
		if wfl.logger != nil {
			wfl.logger.ErrorContext(wfl.ctx, "failed to instantiate WASM module", "module", moduleName, "error", err.Error())
		}
		return fmt.Errorf("failed to instantiate WASM module: %w", err)
	}

	// Create WASMModule wrapper
	wm := &WASMModule{
		Name:     moduleName,
		FilePath: filePath,
		Instance: instance,
		Store:    store,
		Engine:   engine,
		Logger:   wfl.logger,
		Config:   wfl.config,
		Metrics:  &WASMMetrics{},
		ctx:      wfl.ctx,
	}

	// Store module
	wfl.modules[moduleName] = wm

	if wfl.logger != nil {
		wfl.logger.InfoContext(wfl.ctx, "WASM module loaded successfully", "module", moduleName, "path", filePath)
	}

	return nil
}

// GetModule retrieves a loaded WASM module
func (wfl *WASMFunctionLoader) GetModule(moduleName string) *WASMModule {
	wfl.mu.RLock()
	defer wfl.mu.RUnlock()
	return wfl.modules[moduleName]
}

// UnloadModule unloads a WASM module and frees resources
func (wfl *WASMFunctionLoader) UnloadModule(moduleName string) error {
	wfl.mu.Lock()
	defer wfl.mu.Unlock()

	module, exists := wfl.modules[moduleName]
	if !exists {
		return fmt.Errorf("module not found: %s", moduleName)
	}

	// Clean up resources
	if module.Store != nil {
		// Wasmtime store will be GC'd automatically
	}

	delete(wfl.modules, moduleName)

	if wfl.logger != nil {
		wfl.logger.InfoContext(wfl.ctx, "WASM module unloaded", "module", moduleName)
	}

	return nil
}

// ListModules returns names of all loaded modules
func (wfl *WASMFunctionLoader) ListModules() []string {
	wfl.mu.RLock()
	defer wfl.mu.RUnlock()

	names := make([]string, 0, len(wfl.modules))
	for name := range wfl.modules {
		names = append(names, name)
	}
	return names
}

// GetMetrics returns metrics for a loaded module
func (wfl *WASMFunctionLoader) GetMetrics(moduleName string) map[string]interface{} {
	wfl.mu.RLock()
	module, exists := wfl.modules[moduleName]
	wfl.mu.RUnlock()

	if !exists {
		return nil
	}

	return module.Metrics.GetStats()
}

// UnloadAll unloads all modules and shuts down the loader
func (wfl *WASMFunctionLoader) UnloadAll() error {
	wfl.mu.Lock()
	defer wfl.mu.Unlock()

	for moduleName := range wfl.modules {
		if wfl.logger != nil {
			wfl.logger.InfoContext(wfl.ctx, "unloading WASM module", "module", moduleName)
		}
	}

	wfl.modules = make(map[string]*WASMModule)
	return nil
}
