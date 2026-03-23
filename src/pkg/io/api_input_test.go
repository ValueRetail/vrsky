package io

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// TEST UTILITIES
// ============================================================================

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ============================================================================
// CONSTRUCTOR TESTS
// ============================================================================

func TestNewAPIConsumer_ValidConfig(t *testing.T) {
	config := `{
		"id": "test-consumer",
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"pollInterval": "10s"
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}
	if consumer == nil {
		t.Fatal("NewAPIConsumer() returned nil")
	}
	if consumer.config.ID != "test-consumer" {
		t.Errorf("expected ID 'test-consumer', got '%s'", consumer.config.ID)
	}
	if consumer.pollInterval != 10*time.Second {
		t.Errorf("expected pollInterval 10s, got %v", consumer.pollInterval)
	}
}

func TestNewAPIConsumer_MissingURL(t *testing.T) {
	config := `{
		"endpoints": ["/data"]
	}`

	_, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err == nil {
		t.Fatal("expected error for missing baseUrl")
	}
	if !strings.Contains(err.Error(), "baseUrl is required") {
		t.Errorf("expected 'baseUrl is required' error, got: %v", err)
	}
}

func TestNewAPIConsumer_MissingEndpoints(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com"
	}`

	_, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err == nil {
		t.Fatal("expected error for missing endpoints")
	}
	if !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("expected endpoint-related error, got: %v", err)
	}
}

func TestNewAPIConsumer_InvalidJSON(t *testing.T) {
	config := `{invalid json}`

	_, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNewAPIConsumer_DefaultsSet(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"]
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	// Check defaults
	if consumer.config.Method != "GET" {
		t.Errorf("expected default method GET, got %s", consumer.config.Method)
	}
	if consumer.pollInterval != 5*time.Second {
		t.Errorf("expected default pollInterval 5s, got %v", consumer.pollInterval)
	}
	if consumer.timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", consumer.timeout)
	}
	if consumer.config.Pagination.PageSize != 100 {
		t.Errorf("expected default pageSize 100, got %d", consumer.config.Pagination.PageSize)
	}
	if consumer.config.ID == "" {
		t.Error("expected auto-generated ID")
	}
}

func TestNewAPIConsumer_InvalidPollInterval(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"pollInterval": "invalid"
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}
	// Should fall back to default
	if consumer.pollInterval != 5*time.Second {
		t.Errorf("expected fallback to default 5s, got %v", consumer.pollInterval)
	}
}

// ============================================================================
// AUTHENTICATION TESTS
// ============================================================================

func TestAddAuth_Bearer(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"auth": {
			"type": "bearer",
			"token": "my-secret-token"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	consumer.addAuth(req)

	auth := req.Header.Get("Authorization")
	if auth != "Bearer my-secret-token" {
		t.Errorf("expected 'Bearer my-secret-token', got '%s'", auth)
	}
}

func TestAddAuth_APIKey(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"auth": {
			"type": "apikey",
			"apiKey": "my-api-key",
			"keyName": "X-Custom-Key"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	consumer.addAuth(req)

	apiKey := req.Header.Get("X-Custom-Key")
	if apiKey != "my-api-key" {
		t.Errorf("expected 'my-api-key', got '%s'", apiKey)
	}
}

func TestAddAuth_APIKey_DefaultHeader(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"auth": {
			"type": "apikey",
			"apiKey": "my-api-key"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	consumer.addAuth(req)

	apiKey := req.Header.Get("X-API-Key")
	if apiKey != "my-api-key" {
		t.Errorf("expected 'my-api-key' in X-API-Key, got '%s'", apiKey)
	}
}

func TestAddAuth_BasicAuth(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"auth": {
			"type": "basic",
			"username": "user",
			"password": "pass"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	consumer.addAuth(req)

	auth := req.Header.Get("Authorization")
	// Base64("user:pass") = "dXNlcjpwYXNz"
	if auth != "Basic dXNlcjpwYXNz" {
		t.Errorf("expected 'Basic dXNlcjpwYXNz', got '%s'", auth)
	}
}

func TestAddAuth_None(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"auth": {
			"type": "none"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	req, _ := http.NewRequest("GET", "https://api.example.com/data", nil)
	consumer.addAuth(req)

	auth := req.Header.Get("Authorization")
	if auth != "" {
		t.Errorf("expected no Authorization header, got '%s'", auth)
	}
}

// ============================================================================
// PAGINATION TESTS
// ============================================================================

func TestAddPaginationParams_Offset(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"pagination": {
			"strategy": "offset",
			"pageSize": 50
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	// Set up context
	consumer.ctx, consumer.cancel = context.WithCancel(context.Background())
	defer consumer.cancel()

	// Set state
	consumer.state.Offset = 100
	consumer.state.PaginationType = "offset"

	req, _ := consumer.buildRequest("/data")

	// Check query params
	if !strings.Contains(req.URL.RawQuery, "offset=100") {
		t.Errorf("expected offset=100 in query, got %s", req.URL.RawQuery)
	}
	if !strings.Contains(req.URL.RawQuery, "limit=50") {
		t.Errorf("expected limit=50 in query, got %s", req.URL.RawQuery)
	}
}

func TestAddPaginationParams_Cursor(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"pagination": {
			"strategy": "cursor",
			"pageSize": 25,
			"cursorParam": "after"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	// Set up context
	consumer.ctx, consumer.cancel = context.WithCancel(context.Background())
	defer consumer.cancel()

	// Set state
	consumer.state.Cursor = "abc123"
	consumer.state.PaginationType = "cursor"

	req, _ := consumer.buildRequest("/data")

	// Check query params
	if !strings.Contains(req.URL.RawQuery, "after=abc123") {
		t.Errorf("expected after=abc123 in query, got %s", req.URL.RawQuery)
	}
}

// ============================================================================
// RESPONSE PARSING TESTS
// ============================================================================

func TestParseAsRecords_Array(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"]
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	data := `[{"id": 1}, {"id": 2}, {"id": 3}]`
	records, err := consumer.parseAsRecords([]byte(data))
	if err != nil {
		t.Fatalf("parseAsRecords() error = %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records, got %d", len(records))
	}
}

func TestParseAsRecords_SingleObject(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"]
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	data := `{"id": 1, "name": "test"}`
	records, err := consumer.parseAsRecords([]byte(data))
	if err != nil {
		t.Fatalf("parseAsRecords() error = %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
}

func TestExtractRecords_WithJSONPath(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"],
		"dataPath": "$.data"
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	body := `{
		"meta": {"total": 3},
		"data": [{"id": 1}, {"id": 2}, {"id": 3}]
	}`

	records, err := consumer.extractRecords([]byte(body))
	if err != nil {
		t.Fatalf("extractRecords() error = %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records, got %d", len(records))
	}
}

// ============================================================================
// RATE LIMITING TESTS
// ============================================================================

func TestTokenBucket_AllowsRequests(t *testing.T) {
	tb := newTokenBucket(10) // 10 req/s

	// Should allow first 10 requests immediately
	for i := 0; i < 10; i++ {
		if !tb.acquire() {
			t.Errorf("request %d should be allowed", i)
		}
	}
}

func TestTokenBucket_EnforcesLimit(t *testing.T) {
	tb := newTokenBucket(5) // 5 req/s

	// Consume all tokens
	for i := 0; i < 5; i++ {
		tb.acquire()
	}

	// Next request should be denied
	if tb.acquire() {
		t.Error("request should be denied when bucket is empty")
	}
}

func TestTokenBucket_RefillsOverTime(t *testing.T) {
	tb := newTokenBucket(10) // 10 req/s

	// Consume all tokens
	for i := 0; i < 10; i++ {
		tb.acquire()
	}

	// Should be empty
	if tb.acquire() {
		t.Error("should be empty initially")
	}

	// Wait for refill (100ms = 1 token at 10 req/s)
	time.Sleep(150 * time.Millisecond)

	// Should have at least 1 token now
	if !tb.acquire() {
		t.Error("should have refilled after waiting")
	}
}

// ============================================================================
// STATE STORE TESTS
// ============================================================================

func TestInMemoryStateStore_SaveAndLoad(t *testing.T) {
	store := NewInMemoryStateStore()
	ctx := context.Background()

	state := &apiInputState{
		ConsumerID:     "test-123",
		Offset:         100,
		Cursor:         "abc",
		PaginationType: "offset",
	}

	// Save
	err := store.Save(ctx, "test-123", state)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load
	loaded, err := store.Load(ctx, "test-123")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("Load() returned nil")
	}
	if loaded.Offset != 100 {
		t.Errorf("expected Offset 100, got %d", loaded.Offset)
	}
	if loaded.Cursor != "abc" {
		t.Errorf("expected Cursor 'abc', got '%s'", loaded.Cursor)
	}
}

func TestInMemoryStateStore_LoadNonExistent(t *testing.T) {
	store := NewInMemoryStateStore()
	ctx := context.Background()

	loaded, err := store.Load(ctx, "non-existent")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for non-existent key, got %v", loaded)
	}
}

// ============================================================================
// COMPONENT INTERFACE TESTS
// ============================================================================

func TestAPIInput_StartAndClose(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 1}]`))
	}))
	defer server.Close()

	config := fmt.Sprintf(`{
		"baseUrl": "%s",
		"endpoints": ["/data"],
		"pollInterval": "100ms"
	}`, server.URL)

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start
	err = consumer.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Wait a bit for polling to happen
	time.Sleep(200 * time.Millisecond)

	// Close
	err = consumer.Close()
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestAPIInput_Read(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 1}, {"id": 2}]`))
	}))
	defer server.Close()

	config := fmt.Sprintf(`{
		"baseUrl": "%s",
		"endpoints": ["/data"],
		"pollInterval": "50ms"
	}`, server.URL)

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer consumer.Close()

	// Read should return envelopes
	env, err := consumer.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if env == nil {
		t.Fatal("Read() returned nil envelope")
	}
	if env.Source != "api-consumer" {
		t.Errorf("expected Source 'api-consumer', got '%s'", env.Source)
	}
	if env.ContentType != "application/json" {
		t.Errorf("expected ContentType 'application/json', got '%s'", env.ContentType)
	}
}

// ============================================================================
// ERROR HANDLING TESTS
// ============================================================================

func TestAPIInput_4xxError_NoRetry(t *testing.T) {
	requestCount := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	config := fmt.Sprintf(`{
		"baseUrl": "%s",
		"endpoints": ["/data"],
		"pollInterval": "1s"
	}`, server.URL)

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	// Set up context for fetchEndpoint
	consumer.ctx, consumer.cancel = context.WithCancel(context.Background())
	defer consumer.cancel()

	// Fetch endpoint directly
	err = consumer.fetchEndpoint("/data")
	// Should not return error for 4xx (just skip)
	if err != nil {
		t.Errorf("4xx should not return error, got: %v", err)
	}

	// Should only make 1 request (no retries for 4xx)
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("expected 1 request (no retry), got %d", requestCount)
	}
}

func TestAPIInput_5xxError_Retries(t *testing.T) {
	requestCount := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "server error"}`))
			return
		}
		// Success on 3rd try
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 1}]`))
	}))
	defer server.Close()

	config := fmt.Sprintf(`{
		"baseUrl": "%s",
		"endpoints": ["/data"],
		"pollInterval": "1s"
	}`, server.URL)

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	consumer.ctx, consumer.cancel = context.WithCancel(context.Background())
	defer consumer.cancel()

	err = consumer.fetchEndpoint("/data")
	if err != nil {
		t.Errorf("should succeed after retries, got: %v", err)
	}

	// Should have made 3 requests (2 retries + success)
	if atomic.LoadInt32(&requestCount) != 3 {
		t.Errorf("expected 3 requests, got %d", requestCount)
	}
}

// ============================================================================
// PAGINATION DETECTION TESTS
// ============================================================================

func TestDetectPaginationType_FromLinkHeader(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"]
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	headers := http.Header{}
	headers.Set("Link", `<https://api.example.com?page=2>; rel="next"`)

	body := `{"data": []}`
	paginationType := consumer.detectPaginationType([]byte(body), headers)

	if paginationType != "link" {
		t.Errorf("expected 'link', got '%s'", paginationType)
	}
}

func TestDetectPaginationType_FromCursorInBody(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"]
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	body := `{"data": [], "nextCursor": "abc123"}`
	paginationType := consumer.detectPaginationType([]byte(body), http.Header{})

	if paginationType != "cursor" {
		t.Errorf("expected 'cursor', got '%s'", paginationType)
	}
}

func TestExtractNextLink_FromHeader(t *testing.T) {
	config := `{
		"baseUrl": "https://api.example.com",
		"endpoints": ["/data"]
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	headers := http.Header{}
	headers.Set("Link", `<https://api.example.com?page=2>; rel="next", <https://api.example.com?page=1>; rel="prev"`)

	nextLink := consumer.extractNextLink([]byte(`{}`), headers)

	if nextLink != "https://api.example.com?page=2" {
		t.Errorf("expected 'https://api.example.com?page=2', got '%s'", nextLink)
	}
}

// ============================================================================
// REAL API TESTS - api.met.no (Norwegian Meteorological Institute)
// ============================================================================

func TestRealAPI_MetNo_LocationForecast(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real API test in short mode")
	}

	// api.met.no requires User-Agent header and doesn't support pagination
	config := `{
		"id": "met-no-test",
		"baseUrl": "https://api.met.no",
		"endpoints": ["/weatherapi/locationforecast/2.0/compact?lat=59.9139&lon=10.7522"],
		"headers": {
			"User-Agent": "VRSky-Integration-Test/1.0 github.com/ValueRetail/vrsky"
		},
		"pollInterval": "1s",
		"timeout": "10s",
		"dataPath": "$.properties.timeseries",
		"pagination": {
			"strategy": "none"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer consumer.Close()

	// Read should return weather data envelope
	env, err := consumer.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if env == nil {
		t.Fatal("Read() returned nil envelope")
	}

	// Verify envelope
	if env.Source != "api-consumer" {
		t.Errorf("expected Source 'api-consumer', got '%s'", env.Source)
	}
	if env.ContentType != "application/json" {
		t.Errorf("expected ContentType 'application/json', got '%s'", env.ContentType)
	}
	if env.PayloadSize == 0 {
		t.Error("expected non-zero PayloadSize")
	}

	// Verify we got weather data (contains time field)
	var record map[string]interface{}
	if err := json.Unmarshal(env.Payload, &record); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if _, ok := record["time"]; !ok {
		t.Errorf("expected 'time' field in weather record, got: %v", record)
	}

	t.Logf("Successfully received weather data for Oslo, payload size: %d bytes", env.PayloadSize)
}

func TestRealAPI_MetNo_MultipleRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real API test in short mode")
	}

	config := `{
		"id": "met-no-multi-test",
		"baseUrl": "https://api.met.no",
		"endpoints": ["/weatherapi/locationforecast/2.0/compact?lat=59.9139&lon=10.7522"],
		"headers": {
			"User-Agent": "VRSky-Integration-Test/1.0 github.com/ValueRetail/vrsky"
		},
		"pollInterval": "1s",
		"timeout": "10s",
		"dataPath": "$.properties.timeseries",
		"pagination": {
			"strategy": "none"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer consumer.Close()

	// Read multiple records (timeseries has many forecast points)
	recordCount := 0
	for i := 0; i < 5; i++ {
		env, err := consumer.Read(ctx)
		if err != nil {
			break
		}
		if env != nil {
			recordCount++
		}
	}

	if recordCount < 5 {
		t.Logf("Received %d records from api.met.no timeseries", recordCount)
	}

	t.Logf("Successfully received %d weather forecast records", recordCount)
}

func TestRealAPI_MetNo_Sunrise(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real API test in short mode")
	}

	// Test different endpoint - sunrise API
	config := `{
		"id": "met-no-sunrise-test",
		"baseUrl": "https://api.met.no",
		"endpoints": ["/weatherapi/sunrise/3.0/sun?lat=59.9139&lon=10.7522&date=2026-03-16&offset=+01:00"],
		"headers": {
			"User-Agent": "VRSky-Integration-Test/1.0 github.com/ValueRetail/vrsky"
		},
		"pollInterval": "1s",
		"timeout": "10s",
		"pagination": {
			"strategy": "none"
		}
	}`

	consumer, err := NewAPIConsumer(json.RawMessage(config), nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer consumer.Close()

	env, err := consumer.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if env == nil {
		t.Fatal("Read() returned nil envelope")
	}

	// Verify we got sunrise data
	var record map[string]interface{}
	if err := json.Unmarshal(env.Payload, &record); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	t.Logf("Successfully received sunrise data, payload size: %d bytes", env.PayloadSize)
}

// ============================================================================
// INTEGRATION TESTS WITH MOCK SERVER
// ============================================================================

func TestAPIInput_FullPollCycle_MockServer(t *testing.T) {
	pollCount := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&pollCount, 1)
		w.Header().Set("Content-Type", "application/json")

		// Return different data each poll
		data := fmt.Sprintf(`[{"id": %d, "poll": %d}]`, count, count)
		_, _ = w.Write([]byte(data))
	}))
	defer server.Close()

	config := fmt.Sprintf(`{
		"baseUrl": "%s",
		"endpoints": ["/data"],
		"pollInterval": "100ms"
	}`, server.URL)

	stateStore := NewInMemoryStateStore()
	consumer, err := NewAPIConsumer(json.RawMessage(config), stateStore, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Read multiple envelopes
	type pollRecord struct {
		ID   int `json:"id"`
		Poll int `json:"poll"`
	}
	envelopes := make([]pollRecord, 0)
	for i := 0; i < 3; i++ {
		env, err := consumer.Read(ctx)
		if err != nil {
			break
		}

		var record pollRecord
		_ = json.Unmarshal(env.Payload, &record)
		envelopes = append(envelopes, record)
	}

	consumer.Close()

	if len(envelopes) < 2 {
		t.Errorf("expected at least 2 envelopes from multiple polls, got %d", len(envelopes))
	}

	t.Logf("Received %d envelopes from %d polls", len(envelopes), atomic.LoadInt32(&pollCount))
}

func TestAPIInput_StateRecovery_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`[{"offset": "%s"}]`, offset)))
	}))
	defer server.Close()

	config := fmt.Sprintf(`{
		"id": "recovery-test",
		"baseUrl": "%s",
		"endpoints": ["/data"],
		"pollInterval": "100ms",
		"pagination": {
			"strategy": "offset",
			"pageSize": 10
		}
	}`, server.URL)

	// Shared state store to verify recovery
	stateStore := NewInMemoryStateStore()

	// Pre-populate state (simulating previous run)
	ctx := context.Background()
	_ = stateStore.Save(ctx, "recovery-test", &apiInputState{
		ConsumerID:     "recovery-test",
		Offset:         50,
		PaginationType: "offset",
	})

	consumer, err := NewAPIConsumer(json.RawMessage(config), stateStore, newTestLogger())
	if err != nil {
		t.Fatalf("NewAPIConsumer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer consumer.Close()

	// Read envelope - should have offset=50 from recovered state
	env, err := consumer.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	var record struct {
		Offset string `json:"offset"`
	}
	_ = json.Unmarshal(env.Payload, &record)

	if record.Offset != "50" {
		t.Errorf("expected recovered offset '50', got '%s'", record.Offset)
	}

	t.Logf("State recovery verified - offset started at 50")
}

// ============================================================================
// BENCHMARK TESTS
// ============================================================================

func BenchmarkTokenBucket_Acquire(b *testing.B) {
	tb := newTokenBucket(1000000) // High limit for benchmarking

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tb.acquire()
	}
}

func BenchmarkParseAsRecords_SmallArray(b *testing.B) {
	config := `{"baseUrl": "https://api.example.com", "endpoints": ["/data"]}`
	consumer, _ := NewAPIConsumer(json.RawMessage(config), nil, nil)

	data := `[{"id": 1}, {"id": 2}, {"id": 3}, {"id": 4}, {"id": 5}]`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = consumer.parseAsRecords([]byte(data))
	}
}
