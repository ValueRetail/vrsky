package io

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaesslerAG/jsonpath"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
)

// ============================================================================
// CONFIG STRUCTS
// ============================================================================

// APIInputConfig holds the configuration for an API consumer
type APIInputConfig struct {
	ID           string            `json:"id"`
	BaseURL      string            `json:"baseUrl"`
	Endpoints    []string          `json:"endpoints"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	Body         string            `json:"body"`
	Auth         *APIAuth          `json:"auth"`
	Pagination   *APIPagination    `json:"pagination"`
	DataPath     string            `json:"dataPath"`
	PollInterval string            `json:"pollInterval"`
	Timeout      string            `json:"timeout"`
	RateLimit    *APIRateLimit     `json:"rateLimit"`
}

// APIAuth configures authentication for the API consumer
type APIAuth struct {
	Type     string `json:"type"`     // "bearer", "apikey", "basic", "none"
	Token    string `json:"token"`    // Bearer token
	APIKey   string `json:"apiKey"`   // API key value
	KeyName  string `json:"keyName"`  // API key header name (default: X-API-Key)
	Username string `json:"username"` // Basic auth username
	Password string `json:"password"` // Basic auth password
}

// APIPagination configures pagination strategy
type APIPagination struct {
	Strategy     string `json:"strategy"`     // "offset", "cursor", "link", "auto", "none"
	PageSize     int    `json:"pageSize"`     // Number of records per page
	OffsetParam  string `json:"offsetParam"`  // Query param for offset (default: offset)
	LimitParam   string `json:"limitParam"`   // Query param for limit (default: limit)
	CursorParam  string `json:"cursorParam"`  // Query param for cursor (default: cursor)
	CursorPath   string `json:"cursorPath"`   // JSONPath to extract next cursor from response
	NextLinkPath string `json:"nextLinkPath"` // JSONPath to extract next link from response
}

// APIRateLimit configures per-consumer rate limiting
type APIRateLimit struct {
	RequestsPerSecond float64 `json:"requestsPerSecond"`
}

// apiInputState holds persistent state for pagination and error tracking
type apiInputState struct {
	ConsumerID     string    `json:"consumerId"`
	Offset         int       `json:"offset"`
	Cursor         string    `json:"cursor"`
	NextLink       string    `json:"nextLink"`
	LastPoll       time.Time `json:"lastPoll"`
	FailureCount   int       `json:"failureCount"`
	IsExhausted    bool      `json:"isExhausted"`
	PaginationType string    `json:"paginationType"` // "offset", "cursor", "link", "none"
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// ============================================================================
// STATE STORE INTERFACE
// ============================================================================

// StateStore defines the interface for persisting consumer state
type StateStore interface {
	// Load retrieves state for a consumer, returns nil if not found
	Load(ctx context.Context, consumerID string) (*apiInputState, error)
	// Save persists state for a consumer (upsert semantics)
	Save(ctx context.Context, consumerID string, state *apiInputState) error
}

// InMemoryStateStore is a simple in-memory implementation for testing
type InMemoryStateStore struct {
	mu    sync.RWMutex
	state map[string]*apiInputState
}

// NewInMemoryStateStore creates a new in-memory state store
func NewInMemoryStateStore() *InMemoryStateStore {
	return &InMemoryStateStore{
		state: make(map[string]*apiInputState),
	}
}

// Load retrieves state from memory
func (s *InMemoryStateStore) Load(_ context.Context, consumerID string) (*apiInputState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.state[consumerID]
	if !ok {
		return nil, nil
	}
	// Return a copy to prevent external mutation
	stateCopy := *state
	return &stateCopy, nil
}

// Save persists state to memory
func (s *InMemoryStateStore) Save(_ context.Context, consumerID string, state *apiInputState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state.UpdatedAt = time.Now()
	// Store a copy
	stateCopy := *state
	s.state[consumerID] = &stateCopy
	return nil
}

// ============================================================================
// TOKEN BUCKET RATE LIMITER
// ============================================================================

type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

func newTokenBucket(requestsPerSecond float64) *tokenBucket {
	if requestsPerSecond <= 0 {
		requestsPerSecond = 100 // default: 100 req/s
	}
	return &tokenBucket{
		tokens:     requestsPerSecond, // start with full bucket
		maxTokens:  requestsPerSecond,
		refillRate: requestsPerSecond,
		lastRefill: time.Now(),
	}
}

func (tb *tokenBucket) acquire() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	// Refill tokens based on time elapsed
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now

	// Try to acquire a token
	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}
	return false
}

// ============================================================================
// API INPUT CONSUMER
// ============================================================================

// APIInput is a consumer that polls external REST APIs at configurable intervals
type APIInput struct {
	// Configuration
	config       *APIInputConfig
	pollInterval time.Duration
	timeout      time.Duration
	logger       *slog.Logger

	// Runtime
	ctx         context.Context
	cancel      context.CancelFunc
	ticker      *time.Ticker
	messages    chan *envelope.Envelope
	httpClient  *http.Client
	stateStore  StateStore
	rateLimiter *tokenBucket

	// State management
	state      *apiInputState
	mu         sync.Mutex
	closed     bool
	closedOnce sync.Once
}

// NewAPIConsumer creates a new API consumer from JSON configuration
func NewAPIConsumer(configJSON json.RawMessage, stateStore StateStore, logger *slog.Logger) (*APIInput, error) {
	if logger == nil {
		logger = slog.Default()
	}

	var config APIInputConfig
	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, fmt.Errorf("parse api config: %w", err)
	}

	// Validate required fields
	if config.BaseURL == "" {
		return nil, fmt.Errorf("baseUrl is required")
	}
	if len(config.Endpoints) == 0 {
		return nil, fmt.Errorf("at least one endpoint is required")
	}

	// Validate URL format
	if _, err := url.Parse(config.BaseURL); err != nil {
		return nil, fmt.Errorf("invalid baseUrl: %w", err)
	}

	// Generate ID if not provided
	if config.ID == "" {
		config.ID = uuid.New().String()
	}

	// Set default method
	if config.Method == "" {
		config.Method = http.MethodGet
	}

	// Parse poll interval (default: 5s)
	pollInterval := 5 * time.Second
	if config.PollInterval != "" {
		parsed, err := time.ParseDuration(config.PollInterval)
		if err != nil {
			logger.Warn("invalid poll interval, using default",
				"value", config.PollInterval,
				"error", err,
				"default", pollInterval)
		} else if parsed > 0 {
			pollInterval = parsed
		}
	}

	// Parse timeout (default: 30s)
	timeout := 30 * time.Second
	if config.Timeout != "" {
		parsed, err := time.ParseDuration(config.Timeout)
		if err != nil {
			logger.Warn("invalid timeout, using default",
				"value", config.Timeout,
				"error", err,
				"default", timeout)
		} else if parsed > 0 {
			timeout = parsed
		}
	}

	// Set pagination defaults
	if config.Pagination == nil {
		config.Pagination = &APIPagination{
			Strategy: "auto",
			PageSize: 100,
		}
	}
	if config.Pagination.PageSize <= 0 {
		config.Pagination.PageSize = 100
	}
	if config.Pagination.OffsetParam == "" {
		config.Pagination.OffsetParam = "offset"
	}
	if config.Pagination.LimitParam == "" {
		config.Pagination.LimitParam = "limit"
	}
	if config.Pagination.CursorParam == "" {
		config.Pagination.CursorParam = "cursor"
	}

	// Set rate limit defaults
	var rateLimit float64 = 100 // default: 100 req/s
	if config.RateLimit != nil && config.RateLimit.RequestsPerSecond > 0 {
		rateLimit = config.RateLimit.RequestsPerSecond
	}

	// Use in-memory state store if none provided
	if stateStore == nil {
		stateStore = NewInMemoryStateStore()
	}

	// Create HTTP client with timeout and connection pooling
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	return &APIInput{
		config:       &config,
		pollInterval: pollInterval,
		timeout:      timeout,
		logger:       logger,
		messages:     make(chan *envelope.Envelope, 100),
		httpClient:   httpClient,
		stateStore:   stateStore,
		rateLimiter:  newTokenBucket(rateLimit),
		state: &apiInputState{
			ConsumerID: config.ID,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
	}, nil
}

// ============================================================================
// COMPONENT INTERFACE IMPLEMENTATION
// ============================================================================

// Start initializes the API consumer and begins polling
func (a *APIInput) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("consumer already closed")
	}
	a.mu.Unlock()

	a.ctx, a.cancel = context.WithCancel(ctx)

	// Load persisted state
	if err := a.loadState(); err != nil {
		a.logger.Warn("failed to load state, starting fresh",
			"consumer_id", a.config.ID,
			"error", err)
	}

	// Launch polling goroutine
	go a.pollLoop()

	a.logger.Info("API consumer started",
		"consumer_id", a.config.ID,
		"base_url", a.config.BaseURL,
		"endpoints", len(a.config.Endpoints),
		"poll_interval", a.pollInterval,
		"pagination", a.config.Pagination.Strategy)

	return nil
}

// Read retrieves the next message from the consumer
func (a *APIInput) Read(ctx context.Context) (*envelope.Envelope, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.ctx.Done():
		return nil, fmt.Errorf("consumer closed")
	case env, ok := <-a.messages:
		if !ok {
			return nil, fmt.Errorf("messages channel closed")
		}
		return env, nil
	}
}

// Close gracefully shuts down the API consumer
func (a *APIInput) Close() error {
	var closeErr error

	a.closedOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()

		// Cancel context to stop polling
		if a.cancel != nil {
			a.cancel()
		}

		// Stop ticker
		if a.ticker != nil {
			a.ticker.Stop()
		}

		// Save final state
		if err := a.saveState(); err != nil {
			a.logger.Error("failed to save final state",
				"consumer_id", a.config.ID,
				"error", err)
			closeErr = err
		}

		// Close messages channel
		close(a.messages)

		a.logger.Info("API consumer closed",
			"consumer_id", a.config.ID)
	})

	return closeErr
}

// ============================================================================
// MAIN POLLING LOOP
// ============================================================================

func (a *APIInput) pollLoop() {
	// Initial poll immediately
	a.poll()

	a.ticker = time.NewTicker(a.pollInterval)
	defer a.ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.ticker.C:
			a.poll()
		}
	}
}

func (a *APIInput) poll() {
	for _, endpoint := range a.config.Endpoints {
		if a.ctx.Err() != nil {
			return
		}

		if err := a.fetchEndpoint(endpoint); err != nil {
			a.logger.Error("failed to fetch endpoint",
				"consumer_id", a.config.ID,
				"endpoint", endpoint,
				"error", err)

			a.mu.Lock()
			a.state.FailureCount++
			a.mu.Unlock()
		} else {
			a.mu.Lock()
			a.state.FailureCount = 0
			a.state.LastPoll = time.Now()
			a.mu.Unlock()
		}
	}

	// Save state after each poll cycle
	if err := a.saveState(); err != nil {
		a.logger.Error("failed to save state",
			"consumer_id", a.config.ID,
			"error", err)
	}
}

func (a *APIInput) fetchEndpoint(endpoint string) error {
	// Check rate limit
	if !a.rateLimiter.acquire() {
		a.logger.Debug("rate limited, skipping poll",
			"consumer_id", a.config.ID,
			"endpoint", endpoint)
		return nil
	}

	// Build and send request with retry
	var lastErr error
	backoffSchedule := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	}

	for attempt := 0; attempt <= len(backoffSchedule); attempt++ {
		if a.ctx.Err() != nil {
			return a.ctx.Err()
		}

		req, err := a.buildRequest(endpoint)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}

		resp, err := a.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < len(backoffSchedule) {
				a.logger.Warn("request failed, retrying",
					"consumer_id", a.config.ID,
					"endpoint", endpoint,
					"attempt", attempt+1,
					"backoff", backoffSchedule[attempt],
					"error", err)
				time.Sleep(backoffSchedule[attempt])
				continue
			}
			return fmt.Errorf("request failed after %d retries: %w", attempt, lastErr)
		}

		// Handle response
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			lastErr = err
			if attempt < len(backoffSchedule) {
				time.Sleep(backoffSchedule[attempt])
				continue
			}
			return fmt.Errorf("read response: %w", lastErr)
		}

		// Handle HTTP status codes
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			// 4xx: Alert but don't retry
			a.logger.Warn("4xx error from API, skipping",
				"consumer_id", a.config.ID,
				"endpoint", endpoint,
				"status", resp.StatusCode,
				"body", truncateString(string(body), 200))
			return nil // Don't retry, continue to next poll
		}

		if resp.StatusCode >= 500 {
			// 5xx: Retry with backoff
			lastErr = fmt.Errorf("server error: %d", resp.StatusCode)
			if attempt < len(backoffSchedule) {
				a.logger.Warn("5xx error, retrying",
					"consumer_id", a.config.ID,
					"endpoint", endpoint,
					"status", resp.StatusCode,
					"attempt", attempt+1,
					"backoff", backoffSchedule[attempt])
				time.Sleep(backoffSchedule[attempt])
				continue
			}
			return fmt.Errorf("server error after %d retries: %d", attempt, resp.StatusCode)
		}

		// Success - process response
		if err := a.processResponse(body, endpoint, resp.Header); err != nil {
			return fmt.Errorf("process response: %w", err)
		}

		return nil
	}

	return lastErr
}

// ============================================================================
// REQUEST BUILDING
// ============================================================================

func (a *APIInput) buildRequest(endpoint string) (*http.Request, error) {
	// Build URL - ensure proper joining of base URL and endpoint
	baseURL := strings.TrimSuffix(a.config.BaseURL, "/")
	endpointPath := endpoint
	if !strings.HasPrefix(endpointPath, "/") {
		endpointPath = "/" + endpointPath
	}
	fullURL := baseURL + endpointPath

	// Parse URL to add pagination params
	parsedURL, err := url.Parse(fullURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	// Add pagination parameters
	a.mu.Lock()
	query := parsedURL.Query()
	a.addPaginationParams(query)
	a.mu.Unlock()
	parsedURL.RawQuery = query.Encode()

	// Use nextLink directly if link-based pagination
	a.mu.Lock()
	if a.state.PaginationType == "link" && a.state.NextLink != "" {
		parsedURL, err = url.Parse(a.state.NextLink)
		if err != nil {
			a.mu.Unlock()
			return nil, fmt.Errorf("parse next link: %w", err)
		}
	}
	a.mu.Unlock()

	// Create request
	var bodyReader io.Reader
	if a.config.Body != "" {
		bodyReader = strings.NewReader(a.config.Body)
	}

	req, err := http.NewRequestWithContext(a.ctx, a.config.Method, parsedURL.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Add default headers
	req.Header.Set("Accept", "application/json")
	if a.config.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Add custom headers
	for key, value := range a.config.Headers {
		req.Header.Set(key, value)
	}

	// Add authentication
	a.addAuth(req)

	return req, nil
}

func (a *APIInput) addPaginationParams(query url.Values) {
	if a.config.Pagination == nil || a.config.Pagination.Strategy == "none" {
		return
	}

	strategy := a.state.PaginationType
	if strategy == "" {
		strategy = a.config.Pagination.Strategy
	}

	switch strategy {
	case "offset":
		// Offset-based pagination
		query.Set(a.config.Pagination.OffsetParam, strconv.Itoa(a.state.Offset))
		query.Set(a.config.Pagination.LimitParam, strconv.Itoa(a.config.Pagination.PageSize))

	case "auto":
		// Auto mode: only add pagination params on subsequent polls (after first detection)
		// On first poll, don't add any params to let detectPaginationType work
		if a.state.PaginationType != "" {
			// Pagination type has been detected, use it
			switch a.state.PaginationType {
			case "offset":
				query.Set(a.config.Pagination.OffsetParam, strconv.Itoa(a.state.Offset))
				query.Set(a.config.Pagination.LimitParam, strconv.Itoa(a.config.Pagination.PageSize))
			case "cursor":
				if a.state.Cursor != "" {
					query.Set(a.config.Pagination.CursorParam, a.state.Cursor)
				}
				query.Set(a.config.Pagination.LimitParam, strconv.Itoa(a.config.Pagination.PageSize))
			}
		}

	case "cursor":
		// Cursor-based pagination
		if a.state.Cursor != "" {
			query.Set(a.config.Pagination.CursorParam, a.state.Cursor)
		}
		query.Set(a.config.Pagination.LimitParam, strconv.Itoa(a.config.Pagination.PageSize))

	case "link":
		// Link-based pagination - URL is replaced entirely, no query params needed

	case "none":
		// No pagination - don't add any params
	}
}

func (a *APIInput) addAuth(req *http.Request) {
	if a.config.Auth == nil || a.config.Auth.Type == "" || a.config.Auth.Type == "none" {
		return
	}

	switch strings.ToLower(a.config.Auth.Type) {
	case "bearer":
		if a.config.Auth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+a.config.Auth.Token)
		}

	case "apikey":
		keyName := a.config.Auth.KeyName
		if keyName == "" {
			keyName = "X-API-Key"
		}
		if a.config.Auth.APIKey != "" {
			req.Header.Set(keyName, a.config.Auth.APIKey)
		}

	case "basic":
		if a.config.Auth.Username != "" {
			credentials := a.config.Auth.Username + ":" + a.config.Auth.Password
			encoded := base64.StdEncoding.EncodeToString([]byte(credentials))
			req.Header.Set("Authorization", "Basic "+encoded)
		}
	}
}

// ============================================================================
// RESPONSE PROCESSING
// ============================================================================

func (a *APIInput) processResponse(body []byte, endpoint string, headers http.Header) error {
	// Extract records using JSONPath if configured
	records, err := a.extractRecords(body)
	if err != nil {
		return fmt.Errorf("extract records: %w", err)
	}

	// Update pagination state
	a.updatePaginationState(body, headers, len(records))

	// Create envelopes for each record
	for _, record := range records {
		env, err := a.createEnvelope(record, endpoint)
		if err != nil {
			a.logger.Error("failed to create envelope",
				"consumer_id", a.config.ID,
				"endpoint", endpoint,
				"error", err)
			continue
		}

		// Check if closed before sending
		a.mu.Lock()
		closed := a.closed
		a.mu.Unlock()
		if closed {
			return fmt.Errorf("consumer closed")
		}

		// Send to channel (non-blocking)
		select {
		case a.messages <- env:
			a.logger.Debug("record queued",
				"consumer_id", a.config.ID,
				"envelope_id", env.ID)
		case <-a.ctx.Done():
			return a.ctx.Err()
		default:
			a.logger.Warn("message channel full, dropping record",
				"consumer_id", a.config.ID,
				"envelope_id", env.ID)
		}
	}

	a.logger.Info("poll completed",
		"consumer_id", a.config.ID,
		"endpoint", endpoint,
		"records", len(records),
		"exhausted", a.state.IsExhausted)

	return nil
}

func (a *APIInput) extractRecords(body []byte) ([]json.RawMessage, error) {
	// If no data path, treat entire response as single record or array
	if a.config.DataPath == "" {
		return a.parseAsRecords(body)
	}

	// Parse JSON to interface for JSONPath
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("unmarshal json: %w", err)
	}

	// Apply JSONPath
	result, err := jsonpath.Get(a.config.DataPath, data)
	if err != nil {
		// JSONPath not found, return empty
		a.logger.Debug("jsonpath extraction returned no results",
			"consumer_id", a.config.ID,
			"path", a.config.DataPath,
			"error", err)
		return nil, nil
	}

	// Convert result to records
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal jsonpath result: %w", err)
	}

	return a.parseAsRecords(resultJSON)
}

func (a *APIInput) parseAsRecords(data []byte) ([]json.RawMessage, error) {
	// Try to parse as array first
	var array []json.RawMessage
	if err := json.Unmarshal(data, &array); err == nil {
		return array, nil
	}

	// Try as single object
	var obj json.RawMessage
	if err := json.Unmarshal(data, &obj); err == nil {
		return []json.RawMessage{obj}, nil
	}

	return nil, fmt.Errorf("data is neither array nor object")
}

func (a *APIInput) updatePaginationState(body []byte, headers http.Header, recordCount int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Check if exhausted (empty response or no more records)
	if recordCount == 0 {
		a.state.IsExhausted = true
		return
	}

	// Reset exhausted if we got records
	a.state.IsExhausted = false

	// Determine pagination type if auto
	if a.state.PaginationType == "" && a.config.Pagination.Strategy == "auto" {
		a.state.PaginationType = a.detectPaginationType(body, headers)
	} else if a.state.PaginationType == "" {
		a.state.PaginationType = a.config.Pagination.Strategy
	}

	// Update state based on pagination type
	switch a.state.PaginationType {
	case "offset":
		a.state.Offset += recordCount

	case "cursor":
		cursor := a.extractCursor(body)
		if cursor == "" {
			a.state.IsExhausted = true
		} else {
			a.state.Cursor = cursor
		}

	case "link":
		nextLink := a.extractNextLink(body, headers)
		if nextLink == "" {
			a.state.IsExhausted = true
		} else {
			a.state.NextLink = nextLink
		}

	default:
		// No pagination, mark as exhausted after first poll
		a.state.IsExhausted = true
	}

	// Check if we got fewer records than page size (likely last page)
	if recordCount < a.config.Pagination.PageSize {
		a.state.IsExhausted = true
	}
}

func (a *APIInput) detectPaginationType(body []byte, headers http.Header) string {
	// Check for Link header (RFC 5988)
	if linkHeader := headers.Get("Link"); linkHeader != "" {
		if strings.Contains(linkHeader, `rel="next"`) || strings.Contains(linkHeader, `rel=next`) {
			return "link"
		}
	}

	// Parse body to check for pagination hints
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "offset" // Default to offset if can't parse
	}

	// Check for cursor patterns
	cursorPaths := []string{
		"nextCursor", "next_cursor", "cursor",
		"pagination.nextCursor", "pagination.cursor",
		"meta.nextCursor", "meta.cursor",
	}
	for _, path := range cursorPaths {
		if val, err := jsonpath.Get("$."+path, data); err == nil && val != nil && val != "" {
			return "cursor"
		}
	}

	// Check for link patterns in body
	linkPaths := []string{
		"links.next", "pagination.next", "next",
		"_links.next.href", "paging.next",
	}
	for _, path := range linkPaths {
		if val, err := jsonpath.Get("$."+path, data); err == nil && val != nil && val != "" {
			return "link"
		}
	}

	// Default to offset pagination
	return "offset"
}

func (a *APIInput) extractCursor(body []byte) string {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}

	// Try configured cursor path first
	if a.config.Pagination.CursorPath != "" {
		if val, err := jsonpath.Get(a.config.Pagination.CursorPath, data); err == nil {
			if str, ok := val.(string); ok {
				return str
			}
		}
	}

	// Try common cursor paths
	cursorPaths := []string{
		"$.nextCursor", "$.next_cursor", "$.cursor",
		"$.pagination.nextCursor", "$.pagination.cursor",
		"$.meta.nextCursor", "$.meta.cursor",
	}
	for _, path := range cursorPaths {
		if val, err := jsonpath.Get(path, data); err == nil {
			if str, ok := val.(string); ok && str != "" {
				return str
			}
		}
	}

	return ""
}

func (a *APIInput) extractNextLink(body []byte, headers http.Header) string {
	// Check Link header first (RFC 5988)
	if linkHeader := headers.Get("Link"); linkHeader != "" {
		// Parse: <https://api.example.com?page=2>; rel="next"
		parts := strings.Split(linkHeader, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if strings.Contains(part, `rel="next"`) || strings.Contains(part, `rel=next`) {
				// Extract URL from <...>
				start := strings.Index(part, "<")
				end := strings.Index(part, ">")
				if start >= 0 && end > start {
					return part[start+1 : end]
				}
			}
		}
	}

	// Try to extract from body
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}

	// Try configured next link path first
	if a.config.Pagination.NextLinkPath != "" {
		if val, err := jsonpath.Get(a.config.Pagination.NextLinkPath, data); err == nil {
			if str, ok := val.(string); ok {
				return str
			}
		}
	}

	// Try common link paths
	linkPaths := []string{
		"$.links.next", "$.pagination.next", "$.next",
		"$._links.next.href", "$.paging.next",
	}
	for _, path := range linkPaths {
		if val, err := jsonpath.Get(path, data); err == nil {
			if str, ok := val.(string); ok && str != "" {
				return str
			}
		}
	}

	return ""
}

// ============================================================================
// ENVELOPE CREATION
// ============================================================================

func (a *APIInput) createEnvelope(record json.RawMessage, endpoint string) (*envelope.Envelope, error) {
	env := envelope.New()
	env.ID = uuid.New().String()
	env.Source = "api-consumer"
	env.ContentType = "application/json"
	env.Payload = record
	env.PayloadSize = int64(len(record))

	// Add step history
	env.StepHistory = append(env.StepHistory,
		fmt.Sprintf("api-consumer:%s%s", a.config.BaseURL, endpoint))

	// Add metadata
	if env.Metadata == nil {
		env.Metadata = make(map[string]interface{})
	}
	env.Metadata["consumer_id"] = a.config.ID
	env.Metadata["endpoint"] = endpoint
	env.Metadata["pagination_type"] = a.state.PaginationType

	// Handle large payloads (>256KB)
	const maxInlinePayload = 256 * 1024 // 256KB
	if env.PayloadSize > maxInlinePayload {
		// For now, log warning - MinIO integration to be added
		a.logger.Warn("large payload detected, should be stored in object storage",
			"consumer_id", a.config.ID,
			"envelope_id", env.ID,
			"size", env.PayloadSize)
		// TODO: Store in MinIO and set env.PayloadRef
		// For now, keep inline (consumer should handle this externally if needed)
	}

	return env, nil
}

// ============================================================================
// STATE PERSISTENCE
// ============================================================================

func (a *APIInput) loadState() error {
	state, err := a.stateStore.Load(a.ctx, a.config.ID)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if state != nil {
		a.mu.Lock()
		a.state = state
		a.mu.Unlock()
		a.logger.Info("state loaded",
			"consumer_id", a.config.ID,
			"offset", state.Offset,
			"cursor", truncateString(state.Cursor, 20),
			"pagination_type", state.PaginationType)
	}

	return nil
}

func (a *APIInput) saveState() error {
	a.mu.Lock()
	stateCopy := *a.state
	a.mu.Unlock()

	if err := a.stateStore.Save(a.ctx, a.config.ID, &stateCopy); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	return nil
}

// ============================================================================
// UTILITY FUNCTIONS
// ============================================================================

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
