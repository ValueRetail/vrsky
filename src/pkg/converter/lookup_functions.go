package converter

import (
	"context"
	"fmt"
)

// LookupBackend defines the interface for pluggable lookup implementations.
// Phase 3 uses mock implementations; Phase 3.5+ will use real PostgreSQL/HTTP backends.
type LookupBackend interface {
	// Lookup retrieves a value from a table by field and value.
	// Table: table name (e.g., "customers", "products")
	// Field: field name to search by (e.g., "id", "email")
	// Value: value to search for (e.g., "123", "john@example.com")
	// Returns map[string]interface{} with matched row, or nil if not found
	Lookup(ctx context.Context, table, field string, value interface{}) (map[string]interface{}, error)

	// HTTPLookup performs an HTTP request to an external API.
	// URL: endpoint to call
	// Params: query parameters as map[string]interface{}
	// Returns response body as map[string]interface{}, or nil if not found
	HTTPLookup(ctx context.Context, url string, params map[string]interface{}) (map[string]interface{}, error)
}

// MockLookupBackend provides hardcoded mock data for testing.
// Phase 3 uses this; Phase 3.5 will replace with real PostgreSQL backend.
type MockLookupBackend struct {
	// mockData structure: table -> field -> value -> row_data
	mockData map[string]map[string]map[string]map[string]interface{}
}

// NewMockLookupBackend creates a new mock lookup backend with sample data.
func NewMockLookupBackend() *MockLookupBackend {
	backend := &MockLookupBackend{
		mockData: make(map[string]map[string]map[string]map[string]interface{}),
	}

	// Initialize mock tables with sample data
	backend.initMockCustomers()
	backend.initMockProducts()
	backend.initMockInventory()
	backend.initMockTaxRates()

	return backend
}

// initMockCustomers initializes sample customer data
func (m *MockLookupBackend) initMockCustomers() {
	m.mockData["customers"] = make(map[string]map[string]map[string]interface{})

	// Index by ID
	m.mockData["customers"]["id"] = make(map[string]map[string]interface{})
	m.mockData["customers"]["id"]["CUST001"] = map[string]interface{}{
		"id":      "CUST001",
		"name":    "Alice Smith",
		"email":   "alice@example.com",
		"account": "ACC001",
		"country": "US",
		"tier":    "premium",
	}
	m.mockData["customers"]["id"]["CUST002"] = map[string]interface{}{
		"id":      "CUST002",
		"name":    "Bob Johnson",
		"email":   "bob@example.com",
		"account": "ACC002",
		"country": "CA",
		"tier":    "standard",
	}

	// Index by email
	m.mockData["customers"]["email"] = make(map[string]map[string]interface{})
	m.mockData["customers"]["email"]["alice@example.com"] = map[string]interface{}{
		"id":      "CUST001",
		"name":    "Alice Smith",
		"email":   "alice@example.com",
		"account": "ACC001",
		"country": "US",
		"tier":    "premium",
	}
	m.mockData["customers"]["email"]["bob@example.com"] = map[string]interface{}{
		"id":      "CUST002",
		"name":    "Bob Johnson",
		"email":   "bob@example.com",
		"account": "ACC002",
		"country": "CA",
		"tier":    "standard",
	}
}

// initMockProducts initializes sample product data
func (m *MockLookupBackend) initMockProducts() {
	m.mockData["products"] = make(map[string]map[string]map[string]interface{})

	// Index by SKU
	m.mockData["products"]["sku"] = make(map[string]map[string]interface{})
	m.mockData["products"]["sku"]["SKU-001"] = map[string]interface{}{
		"sku":      "SKU-001",
		"name":     "Laptop Pro",
		"price":    1299.99,
		"category": "electronics",
	}
	m.mockData["products"]["sku"]["SKU-002"] = map[string]interface{}{
		"sku":      "SKU-002",
		"name":     "USB Cable",
		"price":    9.99,
		"category": "accessories",
	}

	// Index by ID
	m.mockData["products"]["id"] = make(map[string]map[string]interface{})
	m.mockData["products"]["id"]["PROD001"] = map[string]interface{}{
		"sku":      "SKU-001",
		"name":     "Laptop Pro",
		"price":    1299.99,
		"category": "electronics",
	}
	m.mockData["products"]["id"]["PROD002"] = map[string]interface{}{
		"sku":      "SKU-002",
		"name":     "USB Cable",
		"price":    9.99,
		"category": "accessories",
	}
}

// initMockInventory initializes sample inventory data
func (m *MockLookupBackend) initMockInventory() {
	m.mockData["inventory"] = make(map[string]map[string]map[string]interface{})

	// Index by SKU
	m.mockData["inventory"]["sku"] = make(map[string]map[string]interface{})
	m.mockData["inventory"]["sku"]["SKU-001"] = map[string]interface{}{
		"sku":     "SKU-001",
		"level":   float64(45),
		"reorder": float64(20),
	}
	m.mockData["inventory"]["sku"]["SKU-002"] = map[string]interface{}{
		"sku":     "SKU-002",
		"level":   float64(200),
		"reorder": float64(50),
	}
}

// initMockTaxRates initializes sample tax rate data
func (m *MockLookupBackend) initMockTaxRates() {
	m.mockData["tax_rates"] = make(map[string]map[string]map[string]interface{})

	// Index by country
	m.mockData["tax_rates"]["country"] = make(map[string]map[string]interface{})
	m.mockData["tax_rates"]["country"]["US"] = map[string]interface{}{
		"country": "US",
		"rate":    0.08,
	}
	m.mockData["tax_rates"]["country"]["CA"] = map[string]interface{}{
		"country": "CA",
		"rate":    0.13,
	}
}

// Lookup retrieves a value from the mock lookup tables.
func (m *MockLookupBackend) Lookup(ctx context.Context, table, field string, value interface{}) (map[string]interface{}, error) {
	if table == "" || field == "" || value == nil {
		return nil, nil
	}

	// Get table data
	tableData, exists := m.mockData[table]
	if !exists {
		// Table doesn't exist in mock data - return nil (graceful degradation)
		return nil, nil
	}

	// Get field index
	fieldIndex, exists := tableData[field]
	if !exists {
		// Field doesn't exist - return nil
		return nil, nil
	}

	// Convert value to string for lookup
	valueStr := fmt.Sprintf("%v", value)

	// Get row data
	rowData, exists := fieldIndex[valueStr]
	if !exists {
		// Row not found - return nil
		return nil, nil
	}

	return rowData, nil
}

// HTTPLookup simulates an HTTP lookup by returning mock API responses.
func (m *MockLookupBackend) HTTPLookup(ctx context.Context, url string, params map[string]interface{}) (map[string]interface{}, error) {
	if url == "" {
		return nil, nil
	}

	// Mock API responses based on URL pattern
	// In Phase 3.5, this will make real HTTP requests

	// Example: currency exchange rate API
	if params != nil {
		if from, ok := params["from"].(string); ok {
			if to, ok := params["to"].(string); ok {
				if from == "USD" && to == "EUR" {
					return map[string]interface{}{
						"from": "USD",
						"to":   "EUR",
						"rate": 0.92,
						"date": "2026-02-23",
					}, nil
				}
			}
		}
	}

	// Return nil if no matching API pattern found
	return nil, nil
}

// newLookupFunc creates a lookup function that uses the provided backend.
func newLookupFunc(backend LookupBackend) Function {
	return func(ctx context.Context, args ...interface{}) (interface{}, error) {
		if len(args) < 3 {
			return nil, fmt.Errorf("lookup requires 3 arguments (table, field, value)")
		}

		table, ok := args[0].(string)
		if !ok {
			return nil, nil
		}

		field, ok := args[1].(string)
		if !ok {
			return nil, nil
		}

		value := args[2]

		if backend == nil {
			return nil, nil
		}

		result, err := backend.Lookup(ctx, table, field, value)
		if err != nil {
			// Graceful degradation: log error but return nil
			return nil, nil
		}

		return result, nil
	}
}

// newHTTPLookupFunc creates an HTTP lookup function that uses the provided backend.
func newHTTPLookupFunc(backend LookupBackend) Function {
	return func(ctx context.Context, args ...interface{}) (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("http_lookup requires at least 1 argument (url)")
		}

		url, ok := args[0].(string)
		if !ok {
			return nil, nil
		}

		// Extract query params from remaining args (optional)
		var params map[string]interface{}
		if len(args) > 1 {
			if p, ok := args[1].(map[string]interface{}); ok {
				params = p
			}
		}

		if backend == nil {
			return nil, nil
		}

		result, err := backend.HTTPLookup(ctx, url, params)
		if err != nil {
			// Graceful degradation: log error but return nil
			return nil, nil
		}

		return result, nil
	}
}
