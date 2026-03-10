// Package health provides HTTP health check endpoints for Kubernetes probes
// and Prometheus metrics endpoints for component monitoring.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Status represents the health check response
type Status struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Uptime    int64     `json:"uptime_seconds,omitempty"`
	Component string    `json:"component,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
}

// Server provides HTTP health and metrics endpoints for Kubernetes probes
type Server struct {
	server      *http.Server
	startTime   time.Time
	isReady     atomic.Bool
	isHealthy   atomic.Bool
	logger      *slog.Logger
	componentID string
	nodeID      string
	metricsPath string
	healthPath  string
	readyPath   string
}

// Config holds configuration for the health server
type Config struct {
	// Port to listen on (default: 8080)
	Port int
	// ComponentID is the identifier for this component instance
	ComponentID string
	// NodeID is the pipeline node ID (from orchestrator)
	NodeID string
	// Logger for server operations
	Logger *slog.Logger
	// MetricsPath is the path for Prometheus metrics (default: /metrics)
	MetricsPath string
	// HealthPath is the path for liveness probe (default: /health)
	HealthPath string
	// ReadyPath is the path for readiness probe (default: /ready)
	ReadyPath string
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		Port:        8080,
		MetricsPath: "/metrics",
		HealthPath:  "/health",
		ReadyPath:   "/ready",
	}
}

// NewServer creates a new health server with the given configuration
func NewServer(cfg Config) *Server {
	if cfg.Port == 0 {
		cfg.Port = 8080
	}
	if cfg.MetricsPath == "" {
		cfg.MetricsPath = "/metrics"
	}
	if cfg.HealthPath == "" {
		cfg.HealthPath = "/health"
	}
	if cfg.ReadyPath == "" {
		cfg.ReadyPath = "/ready"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	hs := &Server{
		logger:      cfg.Logger,
		startTime:   time.Now(),
		componentID: cfg.ComponentID,
		nodeID:      cfg.NodeID,
		metricsPath: cfg.MetricsPath,
		healthPath:  cfg.HealthPath,
		readyPath:   cfg.ReadyPath,
	}

	// Mark as healthy and ready by default
	hs.isHealthy.Store(true)
	hs.isReady.Store(true)

	// Setup routes
	mux := http.NewServeMux()
	mux.HandleFunc(cfg.HealthPath, hs.handleHealth)
	mux.HandleFunc(cfg.ReadyPath, hs.handleReady)
	mux.Handle(cfg.MetricsPath, promhttp.Handler())

	// Create HTTP server
	hs.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	return hs
}

// Start starts the health server in a background goroutine
func (s *Server) Start(ctx context.Context) error {
	if s == nil || s.server == nil {
		return fmt.Errorf("health server not initialized")
	}

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.ErrorContext(ctx, "Health server error", "error", err)
		}
	}()

	s.logger.InfoContext(ctx, "Health server started",
		"address", s.server.Addr,
		"health_path", s.healthPath,
		"ready_path", s.readyPath,
		"metrics_path", s.metricsPath)
	return nil
}

// Stop gracefully shuts down the health server
func (s *Server) Stop(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}

	s.logger.InfoContext(ctx, "Stopping health server")
	return s.server.Shutdown(ctx)
}

// SetReady marks the service as ready for traffic
func (s *Server) SetReady(ready bool) {
	if s != nil {
		s.isReady.Store(ready)
	}
}

// SetHealthy marks the service as healthy
func (s *Server) SetHealthy(healthy bool) {
	if s != nil {
		s.isHealthy.Store(healthy)
	}
}

// IsReady returns whether the server is marked as ready
func (s *Server) IsReady() bool {
	if s == nil {
		return false
	}
	return s.isReady.Load()
}

// IsHealthy returns whether the server is marked as healthy
func (s *Server) IsHealthy() bool {
	if s == nil {
		return false
	}
	return s.isHealthy.Load()
}

// handleHealth handles GET /health (liveness probe)
// Returns 200 OK if the process is running and functional
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := "ok"
	code := http.StatusOK

	if !s.isHealthy.Load() {
		status = "unhealthy"
		code = http.StatusServiceUnavailable
	}

	response := Status{
		Status:    status,
		Timestamp: time.Now(),
		Uptime:    int64(time.Since(s.startTime).Seconds()),
		Component: s.componentID,
		NodeID:    s.nodeID,
	}

	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(response)
}

// handleReady handles GET /ready (readiness probe)
// Returns 200 OK only when the service is ready to accept traffic
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := "ready"
	code := http.StatusOK

	if !s.isReady.Load() {
		status = "not ready"
		code = http.StatusServiceUnavailable
	}

	response := Status{
		Status:    status,
		Timestamp: time.Now(),
		Component: s.componentID,
		NodeID:    s.nodeID,
	}

	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(response)
}
