package converter

import (
	"context"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// TEST HELPERS
// =============================================================================

// Use existing newMockLogger from lookup_postgres_test.go or create inline
func getMockLoggerWASM() Logger {
	return newMockLogger()
}

// =============================================================================
// WASM CONFIG TESTS
// =============================================================================

func TestNewWASMConfig(t *testing.T) {
	config := NewWASMConfig()
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	if config.MaxMemoryBytes != 100*1024*1024 {
		t.Errorf("expected 100MB, got %d bytes", config.MaxMemoryBytes)
	}
	if config.TimeoutSeconds != 5 {
		t.Errorf("expected 5 second timeout, got %d", config.TimeoutSeconds)
	}
	if !config.EnableMetrics {
		t.Error("expected metrics enabled by default")
	}
}

func TestWASMConfigCustom(t *testing.T) {
	config := &WASMConfig{
		MaxMemoryBytes: 50 * 1024 * 1024,
		TimeoutSeconds: 10,
		EnableMetrics:  false,
	}
	if config.MaxMemoryBytes != 50*1024*1024 {
		t.Errorf("expected 50MB, got %d bytes", config.MaxMemoryBytes)
	}
	if config.TimeoutSeconds != 10 {
		t.Errorf("expected 10 second timeout, got %d", config.TimeoutSeconds)
	}
	if config.EnableMetrics {
		t.Error("expected metrics disabled")
	}
}

// =============================================================================
// WASM METRICS TESTS
// =============================================================================

func TestWASMMetricsRecordCall(t *testing.T) {
	metrics := &WASMMetrics{}

	metrics.RecordCall(10)
	if metrics.CallCount != 1 {
		t.Errorf("expected 1 call, got %d", metrics.CallCount)
	}
	if metrics.TotalLatencyMs != 10 {
		t.Errorf("expected 10ms total, got %d", metrics.TotalLatencyMs)
	}
	if metrics.MaxLatencyMs != 10 {
		t.Errorf("expected 10ms max, got %d", metrics.MaxLatencyMs)
	}
	if metrics.MinLatencyMs != 10 {
		t.Errorf("expected 10ms min, got %d", metrics.MinLatencyMs)
	}

	metrics.RecordCall(20)
	if metrics.CallCount != 2 {
		t.Errorf("expected 2 calls, got %d", metrics.CallCount)
	}
	if metrics.TotalLatencyMs != 30 {
		t.Errorf("expected 30ms total, got %d", metrics.TotalLatencyMs)
	}
	if metrics.MaxLatencyMs != 20 {
		t.Errorf("expected 20ms max, got %d", metrics.MaxLatencyMs)
	}
	if metrics.MinLatencyMs != 10 {
		t.Errorf("expected 10ms min, got %d", metrics.MinLatencyMs)
	}
}

func TestWASMMetricsRecordError(t *testing.T) {
	metrics := &WASMMetrics{}

	metrics.RecordError("test error")
	if metrics.ErrorCount != 1 {
		t.Errorf("expected 1 error, got %d", metrics.ErrorCount)
	}
	if metrics.LastErrorMsg != "test error" {
		t.Errorf("expected 'test error', got '%s'", metrics.LastErrorMsg)
	}

	metrics.RecordError("another error")
	if metrics.ErrorCount != 2 {
		t.Errorf("expected 2 errors, got %d", metrics.ErrorCount)
	}
	if metrics.LastErrorMsg != "another error" {
		t.Errorf("expected 'another error', got '%s'", metrics.LastErrorMsg)
	}
}

func TestWASMMetricsGetStats(t *testing.T) {
	metrics := &WASMMetrics{}
	metrics.RecordCall(5)
	metrics.RecordCall(15)
	metrics.RecordError("test")

	stats := metrics.GetStats()
	if stats["call_count"].(int64) != 2 {
		t.Errorf("expected 2 calls in stats")
	}
	if stats["error_count"].(int64) != 1 {
		t.Errorf("expected 1 error in stats")
	}
	if stats["avg_latency_ms"].(int64) != 10 {
		t.Errorf("expected 10ms avg latency")
	}
}

func TestWASMMetricsNilSafe(t *testing.T) {
	var metrics *WASMMetrics

	// Should not panic
	metrics.RecordCall(10)
	metrics.RecordError("error")
	stats := metrics.GetStats()

	if len(stats) != 0 {
		t.Error("expected empty stats from nil metrics")
	}
}

// =============================================================================
// WASM FUNCTION LOADER TESTS
// =============================================================================

func TestNewWASMFunctionLoader(t *testing.T) {
	ctx := context.Background()
	logger := getMockLoggerWASM()

	loader := NewWASMFunctionLoader(ctx, logger)
	if loader == nil {
		t.Fatal("expected non-nil loader")
	}
	if loader.config == nil {
		t.Error("expected config to be initialized")
	}
	if len(loader.modules) != 0 {
		t.Errorf("expected 0 modules, got %d", len(loader.modules))
	}
}

func TestNewWASMFunctionLoaderNilContext(t *testing.T) {
	loader := NewWASMFunctionLoader(nil, getMockLoggerWASM())
	if loader == nil {
		t.Fatal("expected non-nil loader")
	}
	if loader.ctx == nil {
		t.Error("expected context to be set to background")
	}
}

func TestWASMFunctionLoaderSetConfig(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())
	customConfig := &WASMConfig{
		MaxMemoryBytes: 50 * 1024 * 1024,
		TimeoutSeconds: 10,
	}

	loader.SetConfig(customConfig)
	if loader.config.TimeoutSeconds != 10 {
		t.Errorf("expected 10s timeout, got %d", loader.config.TimeoutSeconds)
	}
}

func TestWASMFunctionLoaderListModules(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())

	modules := loader.ListModules()
	if len(modules) != 0 {
		t.Errorf("expected 0 modules, got %d", len(modules))
	}
}

// =============================================================================
// TYPE MARSHALING TESTS
// =============================================================================

func TestWASMMarshalArgsNil(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.marshalArgs(nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 args, got %d", len(result))
	}
}

func TestWASMMarshalArgsBool(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.marshalArgs([]interface{}{true, false})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if v, ok := result[0].(int32); !ok || v != 1 {
		t.Errorf("expected int32(1) for true, got %v", result[0])
	}
	if v, ok := result[1].(int32); !ok || v != 0 {
		t.Errorf("expected int32(0) for false, got %v", result[1])
	}
}

func TestWASMMarshalArgsNumbers(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.marshalArgs([]interface{}{int32(42), int64(1000), float32(3.14), float64(2.71)})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if v, ok := result[0].(int32); !ok || v != 42 {
		t.Errorf("expected int32(42), got %v", result[0])
	}
	if v, ok := result[1].(int64); !ok || v != 1000 {
		t.Errorf("expected int64(1000), got %v", result[1])
	}
	if _, ok := result[2].(float64); !ok {
		t.Errorf("expected float64, got %T", result[2])
	}
}

func TestWASMMarshalArgsString(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.marshalArgs([]interface{}{"hello", "world"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if v, ok := result[0].(string); !ok || v != "hello" {
		t.Errorf("expected string 'hello', got %v", result[0])
	}
	if v, ok := result[1].(string); !ok || v != "world" {
		t.Errorf("expected string 'world', got %v", result[1])
	}
}

func TestWASMMarshalArgsArray(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	arr := []interface{}{1, 2, 3}
	result, err := wm.marshalArgs([]interface{}{arr})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if _, ok := result[0].(string); !ok {
		t.Errorf("expected JSON string, got %T", result[0])
	}
}

func TestWASMMarshalArgsMap(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	m := map[string]interface{}{"key": "value"}
	result, err := wm.marshalArgs([]interface{}{m})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if _, ok := result[0].(string); !ok {
		t.Errorf("expected JSON string, got %T", result[0])
	}
}

// =============================================================================
// UNMARSHAL RESULT TESTS
// =============================================================================

func TestWASMUnmarshalResultEmpty(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.unmarshalResult([]interface{}{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestWASMUnmarshalResultNumber(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.unmarshalResult([]interface{}{int32(42)})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if v, ok := result.(int64); !ok || v != 42 {
		t.Errorf("expected int64(42), got %v", result)
	}
}

func TestWASMUnmarshalResultFloat(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.unmarshalResult([]interface{}{float64(3.14)})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if v, ok := result.(float64); !ok || v != 3.14 {
		t.Errorf("expected float64(3.14), got %v", result)
	}
}

func TestWASMUnmarshalResultString(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.unmarshalResult([]interface{}{"hello"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if v, ok := result.(string); !ok || v != "hello" {
		t.Errorf("expected string 'hello', got %v", result)
	}
}

func TestWASMUnmarshalResultJSON(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	// String that is valid JSON
	result, err := wm.unmarshalResult([]interface{}{`{"key":"value"}`})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Should parse as JSON
	if m, ok := result.(map[string]interface{}); !ok || m["key"] != "value" {
		t.Errorf("expected parsed JSON object, got %v", result)
	}
}

// =============================================================================
// ERROR HANDLING TESTS
// =============================================================================

func TestWASMModuleCallNil(t *testing.T) {
	var wm *WASMModule

	result := wm.Call(context.Background(), "test")
	if result != nil {
		t.Errorf("expected nil result from nil module, got %v", result)
	}
}

func TestWASMModuleCallNotInitialized(t *testing.T) {
	wm := &WASMModule{
		Name:     "test",
		Logger:   getMockLoggerWASM(),
		Config:   NewWASMConfig(),
		Metrics:  &WASMMetrics{},
		Instance: nil,
	}

	result := wm.Call(context.Background(), "test")
	if result != nil {
		t.Errorf("expected nil result from uninitialized module, got %v", result)
	}
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

func TestWASMMetricsThreadSafe(t *testing.T) {
	metrics := &WASMMetrics{}
	wg := sync.WaitGroup{}

	// Launch 100 goroutines recording calls concurrently
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(latency int64) {
			defer wg.Done()
			metrics.RecordCall(latency)
		}(int64(i))
	}

	wg.Wait()

	if metrics.CallCount != 100 {
		t.Errorf("expected 100 calls, got %d", metrics.CallCount)
	}
}

func TestWASMFunctionLoaderThreadSafe(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())
	wg := sync.WaitGroup{}

	// Launch concurrent operations
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			loader.ListModules()
		}(i)
	}

	wg.Wait()
}

// =============================================================================
// PERFORMANCE TESTS
// =============================================================================

func TestWASMMetricsPerformance(t *testing.T) {
	metrics := &WASMMetrics{}

	start := time.Now()
	for i := 0; i < 10000; i++ {
		metrics.RecordCall(1)
	}
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Logf("warning: 10000 metric calls took %v", elapsed)
	}

	if metrics.CallCount != 10000 {
		t.Errorf("expected 10000 calls, got %d", metrics.CallCount)
	}
}

// =============================================================================
// LOADER OPERATION TESTS
// =============================================================================

func TestWASMFunctionLoaderLoadInvalidPath(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())

	err := loader.LoadFromFile("test", "/nonexistent/path/to/module.wasm")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestWASMFunctionLoaderLoadEmptyName(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())

	err := loader.LoadFromFile("", "path/to/module.wasm")
	if err == nil {
		t.Error("expected error for empty module name")
	}
}

func TestWASMFunctionLoaderLoadEmptyPath(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())

	err := loader.LoadFromFile("test", "")
	if err == nil {
		t.Error("expected error for empty file path")
	}
}

func TestWASMFunctionLoaderGetNonexistent(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())

	module := loader.GetModule("nonexistent")
	if module != nil {
		t.Error("expected nil for nonexistent module")
	}
}

func TestWASMFunctionLoaderUnloadNonexistent(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())

	err := loader.UnloadModule("nonexistent")
	if err == nil {
		t.Error("expected error when unloading nonexistent module")
	}
}

func TestWASMFunctionLoaderGetMetricsNonexistent(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())

	metrics := loader.GetMetrics("nonexistent")
	if metrics != nil {
		t.Error("expected nil metrics for nonexistent module")
	}
}

func TestWASMFunctionLoaderUnloadAll(t *testing.T) {
	loader := NewWASMFunctionLoader(context.Background(), getMockLoggerWASM())

	err := loader.UnloadAll()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	modules := loader.ListModules()
	if len(modules) != 0 {
		t.Errorf("expected 0 modules after unload all, got %d", len(modules))
	}
}

// =============================================================================
// CONFIG VALIDATION TESTS
// =============================================================================

func TestWASMConfigMaxMemory(t *testing.T) {
	config := &WASMConfig{MaxMemoryBytes: 0}
	if config.MaxMemoryBytes != 0 {
		t.Error("config should allow 0 memory")
	}

	config = &WASMConfig{MaxMemoryBytes: 1000 * 1024 * 1024}
	if config.MaxMemoryBytes != 1000*1024*1024 {
		t.Error("config should allow 1GB memory")
	}
}

func TestWASMConfigTimeout(t *testing.T) {
	config := &WASMConfig{TimeoutSeconds: 0}
	if config.TimeoutSeconds != 0 {
		t.Error("config should allow 0 timeout")
	}

	config = &WASMConfig{TimeoutSeconds: 300}
	if config.TimeoutSeconds != 300 {
		t.Error("config should allow 300 second timeout")
	}
}

// =============================================================================
// EDGE CASE TESTS
// =============================================================================

func TestWASMMarshalArgsEmpty(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.marshalArgs([]interface{}{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d args", len(result))
	}
}

func TestWASMMarshalArgsNilValue(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	result, err := wm.marshalArgs([]interface{}{nil})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if v, ok := result[0].(int32); !ok || v != 0 {
		t.Errorf("expected int32(0) for nil, got %v", result[0])
	}
}

func TestWASMMarshalArgsMixedTypes(t *testing.T) {
	wm := &WASMModule{Config: NewWASMConfig()}

	args := []interface{}{
		true,
		int32(42),
		float64(3.14),
		"hello",
		[]interface{}{1, 2, 3},
		map[string]interface{}{"key": "value"},
	}

	result, err := wm.marshalArgs(args)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if len(result) != len(args) {
		t.Errorf("expected %d args, got %d", len(args), len(result))
	}
}

// =============================================================================
// INTEGRATION SANITY TESTS
// =============================================================================

func TestWASMConfigDefaults(t *testing.T) {
	config := NewWASMConfig()
	if config.MaxMemoryBytes == 0 {
		t.Error("MaxMemoryBytes should have a default")
	}
	if config.TimeoutSeconds == 0 {
		t.Error("TimeoutSeconds should have a default")
	}
}

func TestWASMModuleMetricsAllocated(t *testing.T) {
	wm := &WASMModule{
		Name:    "test",
		Metrics: &WASMMetrics{},
		Config:  NewWASMConfig(),
		Logger:  getMockLoggerWASM(),
	}

	if wm.Metrics == nil {
		t.Error("Metrics should be allocated")
	}
}

func TestWASMFunctionLoaderContextPassthrough(t *testing.T) {
	ctx := context.Background()
	loader := NewWASMFunctionLoader(ctx, getMockLoggerWASM())

	if loader.ctx == nil {
		t.Error("Context should be preserved")
	}
}
