package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// HealthStatus represents the health check response
type HealthStatus struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Uptime    int64     `json:"uptime_seconds,omitempty"`
}

// HealthServer provides HTTP health endpoints for Kubernetes probes
type HealthServer struct {
	server    *http.Server
	startTime time.Time
	isReady   atomic.Bool
	isHealthy atomic.Bool
	logger    Logger
}

// NewHealthServer creates a new health server on the specified port
func NewHealthServer(port int, logger Logger) *HealthServer {
	if logger == nil {
		return nil
	}

	hs := &HealthServer{
		logger:    logger,
		startTime: time.Now(),
	}

	// Mark as healthy and ready by default
	hs.isHealthy.Store(true)
	hs.isReady.Store(true)

	// Setup routes
	mux := http.NewServeMux()
	mux.HandleFunc("/health", hs.handleHealth)
	mux.HandleFunc("/ready", hs.handleReady)

	// Create HTTP server
	hs.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	return hs
}

// Start starts the health server in a background goroutine
func (hs *HealthServer) Start(ctx context.Context) error {
	if hs == nil || hs.server == nil {
		return fmt.Errorf("health server not initialized")
	}

	go func() {
		if err := hs.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			hs.logger.ErrorContext(ctx, "Health server error", "error", err)
		}
	}()

	hs.logger.InfoContext(ctx, "Health server started", "address", hs.server.Addr)
	return nil
}

// Stop gracefully shuts down the health server
func (hs *HealthServer) Stop(ctx context.Context) error {
	if hs == nil || hs.server == nil {
		return nil
	}

	return hs.server.Shutdown(ctx)
}

// SetReady marks the service as ready for traffic
func (hs *HealthServer) SetReady(ready bool) {
	if hs != nil {
		hs.isReady.Store(ready)
	}
}

// SetHealthy marks the service as healthy
func (hs *HealthServer) SetHealthy(healthy bool) {
	if hs != nil {
		hs.isHealthy.Store(healthy)
	}
}

// handleHealth handles GET /health (liveness probe)
// Returns 200 OK if the process is running and functional
func (hs *HealthServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := "ok"
	code := http.StatusOK

	if !hs.isHealthy.Load() {
		status = "unhealthy"
		code = http.StatusServiceUnavailable
	}

	response := HealthStatus{
		Status:    status,
		Timestamp: time.Now(),
		Uptime:    int64(time.Since(hs.startTime).Seconds()),
	}

	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(response)
}

// handleReady handles GET /ready (readiness probe)
// Returns 200 OK only when the service is ready to accept traffic
func (hs *HealthServer) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := "ready"
	code := http.StatusOK

	if !hs.isReady.Load() {
		status = "not ready"
		code = http.StatusServiceUnavailable
	}

	response := HealthStatus{
		Status:    status,
		Timestamp: time.Now(),
	}

	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(response)
}
