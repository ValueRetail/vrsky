package managementapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// TestGenerator manages test data generation for a connection
type TestGenerator struct {
	mu              sync.RWMutex
	connectionID    string
	tenantID        string
	isRunning       bool
	messageCount    int64
	errorCount      int64
	startTime       *time.Time
	stopTime        *time.Time
	lastMessageTime *time.Time
	ratePerSecond   int
	cancel          context.CancelFunc
	done            chan struct{}
}

// TestGeneratorRegistry manages active test generators per connection
type TestGeneratorRegistry struct {
	mu         sync.RWMutex
	generators map[string]*TestGenerator // key: "{tenantID}:{connectionID}"
}

// TestDataPayload represents a test data message
type TestDataPayload struct {
	ID        string            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	Data      interface{}       `json:"data"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// TestGeneratorStatus represents the status of a test generator
type TestGeneratorStatus struct {
	ConnectionID  string     `json:"connection_id"`
	IsRunning     bool       `json:"is_running"`
	MessageCount  int64      `json:"message_count"`
	ErrorCount    int64      `json:"error_count"`
	RatePerSecond int        `json:"rate_per_second"`
	StartTime     *time.Time `json:"start_time"`
	StopTime      *time.Time `json:"stop_time"`
	LastMessage   *time.Time `json:"last_message_time"`
	Uptime        string     `json:"uptime"`
}

// TestMessageRequest represents a single test message request
type TestMessageRequest struct {
	Payload  interface{}       `json:"payload"` // JSON payload for the test message
	Metadata map[string]string `json:"metadata,omitempty"`
}

// TestGeneratorRequest represents a test generator control request
type TestGeneratorRequest struct {
	RatePerSecond int `json:"rate_per_second"` // Messages per second (1-1000)
}

// NewTestGenerator creates a new test generator
func NewTestGenerator(connID, tenantID string) *TestGenerator {
	return &TestGenerator{
		connectionID: connID,
		tenantID:     tenantID,
		done:         make(chan struct{}),
	}
}

// NewTestGeneratorRegistry creates a new test generator registry
func NewTestGeneratorRegistry() *TestGeneratorRegistry {
	return &TestGeneratorRegistry{
		generators: make(map[string]*TestGenerator),
	}
}

// GetOrCreate gets an existing generator or creates a new one
func (r *TestGeneratorRegistry) GetOrCreate(tenantID, connID string) *TestGenerator {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := fmt.Sprintf("%s:%s", tenantID, connID)
	if gen, exists := r.generators[key]; exists {
		return gen
	}

	gen := NewTestGenerator(connID, tenantID)
	r.generators[key] = gen
	return gen
}

// Get retrieves an existing generator
func (r *TestGeneratorRegistry) Get(tenantID, connID string) *TestGenerator {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", tenantID, connID)
	return r.generators[key]
}

// Remove removes a generator from the registry
func (r *TestGeneratorRegistry) Remove(tenantID, connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := fmt.Sprintf("%s:%s", tenantID, connID)
	delete(r.generators, key)
}

// SendSingleTestMessage sends a single test message to the pipeline
func (h *Handler) SendSingleTestMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID
	connID := r.PathValue("id")
	if connID == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Verify connection exists and belongs to tenant
	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		return
	}

	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Parse request body
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)
	var req TestMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err == io.EOF {
			_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "request body is empty", nil)
		} else {
			_ = writeError(w, http.StatusBadRequest, "InvalidJSON", fmt.Sprintf("failed to parse request: %v", err), nil)
		}
		return
	}

	// Validate payload
	if req.Payload == nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "payload is required", nil)
		return
	}

	// Create test message
	testMsg := TestDataPayload{
		ID:        fmt.Sprintf("test-%d", time.Now().UnixNano()),
		Timestamp: time.Now().UTC(),
		Data:      req.Payload,
		Metadata:  req.Metadata,
	}

	// Publish test message via NATS if publisher is available
	if h.publisher != nil {
		if err := h.publisher.PublishTestMessage(ctx, connID, tenantID, testMsg); err != nil {
			_ = writeError(w, http.StatusInternalServerError, "PublishError", "failed to publish test message", nil)
			return
		}
	}

	_ = writeJSON(w, http.StatusOK, SuccessResponse{
		Data: map[string]interface{}{
			"message_id": testMsg.ID,
			"sent_at":    testMsg.Timestamp,
		},
	})
}

// StartAutoGenerator starts automatic test message generation
// POST /api/v1/connections/{id}/auto-generator/start
func (h *Handler) StartAutoGenerator(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID
	connID := r.PathValue("id")
	if connID == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Verify connection exists
	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		return
	}

	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Parse request body
	var req TestGeneratorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.RatePerSecond = 10 // Default rate
	}

	// Validate rate
	if req.RatePerSecond < 1 || req.RatePerSecond > 1000 {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "rate_per_second must be between 1 and 1000", nil)
		return
	}

	// Get or create test generator for this connection
	gen := h.generatorRegistry.GetOrCreate(tenantID, connID)

	// Check if already running
	if gen.Status().IsRunning {
		_ = writeError(w, http.StatusConflict, "AlreadyRunning", "test generator is already running for this connection", nil)
		return
	}

	// Start the generator
	if err := gen.Start(ctx, req.RatePerSecond, h.publisher); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "StartError", fmt.Sprintf("failed to start generator: %v", err), nil)
		return
	}

	// Return status
	status := gen.Status()
	_ = writeJSON(w, http.StatusOK, SuccessResponse{
		Data: status,
	})
}

// StopAutoGenerator stops automatic test message generation
// POST /api/v1/connections/{id}/auto-generator/stop
func (h *Handler) StopAutoGenerator(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID
	connID := r.PathValue("id")
	if connID == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Verify connection exists
	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		return
	}

	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Get test generator for this connection
	gen := h.generatorRegistry.Get(tenantID, connID)
	if gen == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "no test generator running for this connection", nil)
		return
	}

	// Stop the generator
	if err := gen.Stop(); err != nil {
		_ = writeError(w, http.StatusConflict, "NotRunning", fmt.Sprintf("generator not running: %v", err), nil)
		return
	}

	// Return status
	status := gen.Status()
	_ = writeJSON(w, http.StatusOK, SuccessResponse{
		Data: status,
	})
}

// GetAutoGeneratorStatus retrieves the status of the test generator
// GET /api/v1/connections/{id}/auto-generator/status
func (h *Handler) GetAutoGeneratorStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get tenant ID
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	// Extract connection ID
	connID := r.PathValue("id")
	if connID == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "connection ID is required", nil)
		return
	}

	// Verify connection exists
	conn, err := h.repo.GetConnection(ctx, connID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "connection not found", nil)
		return
	}

	if conn.TenantID != tenantID {
		_ = writeError(w, http.StatusForbidden, "Forbidden", "not authorized to access this connection", nil)
		return
	}

	// Get test generator for this connection
	gen := h.generatorRegistry.Get(tenantID, connID)
	if gen == nil {
		// Return default status if no generator exists
		status := TestGeneratorStatus{
			ConnectionID:  connID,
			IsRunning:     false,
			MessageCount:  0,
			ErrorCount:    0,
			RatePerSecond: 0,
		}
		_ = writeJSON(w, http.StatusOK, SuccessResponse{
			Data: status,
		})
		return
	}

	// Return actual generator status
	status := gen.Status()
	_ = writeJSON(w, http.StatusOK, SuccessResponse{
		Data: status,
	})
}

// GenerateTestPayload generates a random test payload
func GenerateTestPayload() interface{} {
	payloadTypes := []string{"json", "string", "number"}
	payloadType := payloadTypes[rand.Intn(len(payloadTypes))]

	switch payloadType {
	case "json":
		return map[string]interface{}{
			"user_id":   rand.Intn(10000),
			"action":    "test_action",
			"timestamp": time.Now().Unix(),
		}
	case "string":
		return fmt.Sprintf("Test message %d", rand.Intn(1000000))
	case "number":
		return rand.Float64() * 100
	default:
		return "test"
	}
}

// Status returns the current status of the test generator
func (tg *TestGenerator) Status() TestGeneratorStatus {
	tg.mu.RLock()
	defer tg.mu.RUnlock()

	uptime := ""
	if tg.startTime != nil && tg.isRunning {
		uptime = time.Since(*tg.startTime).String()
	}

	return TestGeneratorStatus{
		ConnectionID:  tg.connectionID,
		IsRunning:     tg.isRunning,
		MessageCount:  tg.messageCount,
		ErrorCount:    tg.errorCount,
		RatePerSecond: tg.ratePerSecond,
		StartTime:     tg.startTime,
		StopTime:      tg.stopTime,
		LastMessage:   tg.lastMessageTime,
		Uptime:        uptime,
	}
}

// Start begins generating test messages
func (tg *TestGenerator) Start(ctx context.Context, ratePerSecond int, publisher *NATSPublisher) error {
	// Validate input parameters before acquiring lock
	if ratePerSecond < 1 || ratePerSecond > 1000 {
		return fmt.Errorf("invalid rate: must be between 1 and 1000")
	}

	tg.mu.Lock()

	if tg.isRunning {
		tg.mu.Unlock()
		return fmt.Errorf("test generator is already running")
	}

	tg.isRunning = true
	tg.ratePerSecond = ratePerSecond
	tg.messageCount = 0
	tg.errorCount = 0
	now := time.Now().UTC()
	tg.startTime = &now
	tg.stopTime = nil

	// Create context and cancel function while holding the lock
	generatorCtx, cancel := context.WithCancel(ctx)
	tg.cancel = cancel

	tg.mu.Unlock()

	go tg.generationLoop(generatorCtx, publisher)

	return nil
}

// Stop stops the test generator
func (tg *TestGenerator) Stop() error {
	tg.mu.Lock()

	if !tg.isRunning {
		tg.mu.Unlock()
		return fmt.Errorf("test generator is not running")
	}

	tg.isRunning = false
	now := time.Now().UTC()
	tg.stopTime = &now

	// Capture the cancel function while holding the lock
	cancel := tg.cancel
	tg.cancel = nil

	tg.mu.Unlock()

	// Call cancel after releasing the lock
	if cancel != nil {
		cancel()
	}

	return nil
}

// generationLoop runs the test message generation loop
func (tg *TestGenerator) generationLoop(ctx context.Context, publisher *NATSPublisher) {
	interval := time.Second / time.Duration(tg.ratePerSecond)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Generate and publish test message
			payload := GenerateTestPayload()
			testMsg := TestDataPayload{
				ID:        fmt.Sprintf("test-%d", time.Now().UnixNano()),
				Timestamp: time.Now().UTC(),
				Data:      payload,
			}

			if publisher != nil {
				if err := publisher.PublishTestMessage(ctx, tg.connectionID, tg.tenantID, testMsg); err != nil {
					tg.mu.Lock()
					tg.errorCount++
					tg.mu.Unlock()
				} else {
					tg.mu.Lock()
					tg.messageCount++
					now := time.Now().UTC()
					tg.lastMessageTime = &now
					tg.mu.Unlock()
				}
			}
		}
	}
}
