package converter

import (
	"context"
	"testing"
)

// TestNewMockLookupBackend tests mock backend initialization
func TestNewMockLookupBackend(t *testing.T) {
	backend := NewMockLookupBackend()

	if backend == nil {
		t.Fatal("expected mock backend, got nil")
	}

	// Verify tables are initialized
	if len(backend.mockData) == 0 {
		t.Error("expected mock data to be initialized")
	}

	expectedTables := []string{"customers", "products", "inventory", "tax_rates"}
	for _, table := range expectedTables {
		if _, exists := backend.mockData[table]; !exists {
			t.Errorf("expected table %q to be initialized", table)
		}
	}
}

// TestMockLookupBackend_Lookup tests database lookup functionality
func TestMockLookupBackend_Lookup(t *testing.T) {
	backend := NewMockLookupBackend()
	ctx := context.Background()

	tests := []struct {
		name    string
		table   string
		field   string
		value   interface{}
		want    map[string]interface{}
		wantNil bool
	}{
		{
			name:    "lookup customer by id",
			table:   "customers",
			field:   "id",
			value:   "CUST001",
			wantNil: false,
			want: map[string]interface{}{
				"id":      "CUST001",
				"name":    "Alice Smith",
				"email":   "alice@example.com",
				"account": "ACC001",
				"country": "US",
				"tier":    "premium",
			},
		},
		{
			name:    "lookup customer by email",
			table:   "customers",
			field:   "email",
			value:   "bob@example.com",
			wantNil: false,
			want: map[string]interface{}{
				"id":      "CUST002",
				"name":    "Bob Johnson",
				"email":   "bob@example.com",
				"account": "ACC002",
				"country": "CA",
				"tier":    "standard",
			},
		},
		{
			name:    "lookup product by sku",
			table:   "products",
			field:   "sku",
			value:   "SKU-001",
			wantNil: false,
			want: map[string]interface{}{
				"sku":      "SKU-001",
				"name":     "Laptop Pro",
				"price":    1299.99,
				"category": "electronics",
			},
		},
		{
			name:    "lookup inventory by sku",
			table:   "inventory",
			field:   "sku",
			value:   "SKU-002",
			wantNil: false,
			want: map[string]interface{}{
				"sku":     "SKU-002",
				"level":   float64(200),
				"reorder": float64(50),
			},
		},
		{
			name:    "lookup tax rate by country",
			table:   "tax_rates",
			field:   "country",
			value:   "US",
			wantNil: false,
			want: map[string]interface{}{
				"country": "US",
				"rate":    0.08,
			},
		},
		{
			name:    "customer not found",
			table:   "customers",
			field:   "id",
			value:   "NONEXISTENT",
			wantNil: true,
		},
		{
			name:    "table not found",
			table:   "nonexistent_table",
			field:   "id",
			value:   "some_value",
			wantNil: true,
		},
		{
			name:    "field not found",
			table:   "customers",
			field:   "nonexistent_field",
			value:   "CUST001",
			wantNil: true,
		},
		{
			name:    "empty table",
			table:   "",
			field:   "id",
			value:   "value",
			wantNil: true,
		},
		{
			name:    "empty field",
			table:   "customers",
			field:   "",
			value:   "CUST001",
			wantNil: true,
		},
		{
			name:    "nil value",
			table:   "customers",
			field:   "id",
			value:   nil,
			wantNil: true,
		},
		{
			name:    "numeric value converted to string",
			table:   "customers",
			field:   "id",
			value:   int64(123), // Will be converted to "123"
			wantNil: true,       // Won't match string "CUST001"
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := backend.Lookup(ctx, tt.table, tt.field, tt.value)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
			} else {
				if got == nil {
					t.Fatal("expected non-nil result")
				}

				// Verify key fields
				for key, expectedVal := range tt.want {
					if actualVal, exists := got[key]; !exists {
						t.Errorf("expected key %q not found in result", key)
					} else if actualVal != expectedVal {
						t.Errorf("for key %q: expected %v, got %v", key, expectedVal, actualVal)
					}
				}
			}
		})
	}
}

// TestMockLookupBackend_HTTPLookup tests HTTP lookup functionality
func TestMockLookupBackend_HTTPLookup(t *testing.T) {
	backend := NewMockLookupBackend()
	ctx := context.Background()

	tests := []struct {
		name    string
		url     string
		params  map[string]interface{}
		want    map[string]interface{}
		wantNil bool
	}{
		{
			name: "currency exchange lookup USD to EUR",
			url:  "https://api.example.com/exchange",
			params: map[string]interface{}{
				"from": "USD",
				"to":   "EUR",
			},
			wantNil: false,
			want: map[string]interface{}{
				"from": "USD",
				"to":   "EUR",
				"rate": 0.92,
				"date": "2026-02-23",
			},
		},
		{
			name:    "empty url",
			url:     "",
			params:  map[string]interface{}{},
			wantNil: true,
		},
		{
			name:    "unknown API endpoint",
			url:     "https://api.example.com/unknown",
			params:  map[string]interface{}{},
			wantNil: true,
		},
		{
			name:    "missing params",
			url:     "https://api.example.com/exchange",
			params:  map[string]interface{}{},
			wantNil: true,
		},
		{
			name:    "nil params",
			url:     "https://api.example.com/exchange",
			params:  nil,
			wantNil: true,
		},
		{
			name: "wrong param types",
			url:  "https://api.example.com/exchange",
			params: map[string]interface{}{
				"from": 123, // Should be string
				"to":   456,
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := backend.HTTPLookup(ctx, tt.url, tt.params)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
			} else {
				if got == nil {
					t.Fatal("expected non-nil result")
				}

				// Verify key fields
				for key, expectedVal := range tt.want {
					if actualVal, exists := got[key]; !exists {
						t.Errorf("expected key %q not found in result", key)
					} else if actualVal != expectedVal {
						t.Errorf("for key %q: expected %v, got %v", key, expectedVal, actualVal)
					}
				}
			}
		})
	}
}

// TestNewLookupFunc tests the lookup function wrapper
func TestNewLookupFunc(t *testing.T) {
	backend := NewMockLookupBackend()
	lookupFn := newLookupFunc(backend)

	tests := []struct {
		name    string
		args    []interface{}
		want    map[string]interface{}
		wantNil bool
		wantErr bool
	}{
		{
			name:    "valid lookup",
			args:    []interface{}{"customers", "id", "CUST001"},
			wantNil: false,
			wantErr: false,
		},
		{
			name:    "lookup not found",
			args:    []interface{}{"customers", "id", "NONEXISTENT"},
			wantNil: true, // When not found, Lookup returns nil
			wantErr: false,
		},
		{
			name:    "missing arguments",
			args:    []interface{}{"customers", "id"},
			wantNil: true,
			wantErr: true,
		},
		{
			name:    "invalid table type",
			args:    []interface{}{123, "id", "value"},
			wantNil: true, // Type assertion fails, returns nil
			wantErr: false,
		},
		{
			name:    "invalid field type",
			args:    []interface{}{"customers", 123, "value"},
			wantNil: true, // Type assertion fails, returns nil
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			got, err := lookupFn(ctx, tt.args...)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			isEmpty := got == nil
			if !isEmpty {
				if gotMap, ok := got.(map[string]interface{}); ok {
					isEmpty = len(gotMap) == 0
				}
			}

			if tt.wantNil && !isEmpty {
				t.Errorf("expected nil or empty, got %v", got)
			}
			if !tt.wantNil && isEmpty {
				t.Error("expected non-empty result")
			}
		})
	}
}

// TestNewHTTPLookupFunc tests the HTTP lookup function wrapper
func TestNewHTTPLookupFunc(t *testing.T) {
	backend := NewMockLookupBackend()
	httpLookupFn := newHTTPLookupFunc(backend)

	tests := []struct {
		name    string
		args    []interface{}
		want    map[string]interface{}
		wantNil bool
		wantErr bool
	}{
		{
			name:    "valid http lookup",
			args:    []interface{}{"https://api.example.com/exchange", map[string]interface{}{"from": "USD", "to": "EUR"}},
			wantNil: false,
			wantErr: false,
		},
		{
			name:    "http lookup not found",
			args:    []interface{}{"https://api.example.com/unknown"},
			wantNil: true,
			wantErr: false,
		},
		{
			name:    "missing url",
			args:    []interface{}{},
			wantNil: true,
			wantErr: true,
		},
		{
			name:    "invalid url type",
			args:    []interface{}{123},
			wantNil: true,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			got, err := httpLookupFn(ctx, tt.args...)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			isEmpty := got == nil
			if !isEmpty {
				if gotMap, ok := got.(map[string]interface{}); ok {
					isEmpty = len(gotMap) == 0
				}
			}

			if tt.wantNil && !isEmpty {
				t.Errorf("expected nil or empty, got %v", got)
			}
			if !tt.wantNil && isEmpty {
				t.Error("expected non-empty result")
			}
		})
	}
}

// TestLookupBackendNilHandling tests graceful nil handling
func TestLookupBackendNilHandling(t *testing.T) {
	t.Run("nil backend in lookup function", func(t *testing.T) {
		lookupFn := newLookupFunc(nil)
		ctx := context.Background()
		result, err := lookupFn(ctx, "customers", "id", "CUST001")

		if err != nil {
			t.Errorf("unexpected error with nil backend: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil result with nil backend, got %v", result)
		}
	})

	t.Run("nil backend in http lookup function", func(t *testing.T) {
		httpLookupFn := newHTTPLookupFunc(nil)
		ctx := context.Background()
		result, err := httpLookupFn(ctx, "https://api.example.com")

		if err != nil {
			t.Errorf("unexpected error with nil backend: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil result with nil backend, got %v", result)
		}
	})
}

// TestLookupFunctionRegistryIntegration tests lookup functions in registry
func TestLookupFunctionRegistryIntegration(t *testing.T) {
	logger := NewTestLogger()
	ctx := context.Background()
	registry := NewFunctionRegistry(ctx, logger)

	tests := []struct {
		name     string
		funcName string
		args     []interface{}
		wantNil  bool
		wantErr  bool
	}{
		{
			name:     "call lookup function",
			funcName: "lookup",
			args:     []interface{}{"customers", "id", "CUST001"},
			wantNil:  false,
			wantErr:  false,
		},
		{
			name:     "call http_lookup function",
			funcName: "http_lookup",
			args:     []interface{}{"https://api.example.com/exchange", map[string]interface{}{"from": "USD", "to": "EUR"}},
			wantNil:  false,
			wantErr:  false,
		},
		{
			name:     "lookup with missing table",
			funcName: "lookup",
			args:     []interface{}{"nonexistent", "id", "value"},
			wantNil:  true,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := registry.Call(tt.funcName, tt.args...)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			isEmpty := result == nil
			if !isEmpty {
				if resultMap, ok := result.(map[string]interface{}); ok {
					isEmpty = len(resultMap) == 0
				}
			}

			if tt.wantNil && !isEmpty {
				t.Errorf("expected nil or empty, got %v", result)
			}
			if !tt.wantNil && isEmpty {
				t.Error("expected non-empty result")
			}
		})
	}
}

// TestSetLookupBackend tests the SetLookupBackend method
func TestSetLookupBackend(t *testing.T) {
	logger := NewTestLogger()
	ctx := context.Background()
	registry := NewFunctionRegistry(ctx, logger)

	// Create a new mock backend
	newBackend := NewMockLookupBackend()

	// Set the new backend
	registry.SetLookupBackend(newBackend)

	// Verify lookup still works
	result, err := registry.Call("lookup", "customers", "id", "CUST001")
	if err != nil {
		t.Fatalf("unexpected error after SetLookupBackend: %v", err)
	}
	if result == nil {
		t.Error("expected non-nil result after SetLookupBackend")
	}
}
