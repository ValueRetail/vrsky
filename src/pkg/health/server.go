// Package health provides HTTP health check endpoints for Kubernetes probes
// and Prometheus metrics endpoints for component monitoring.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// readinessCheckTimeout bounds each upstream readiness check so a hung
// dependency can't stall the /readyz response (and the K8s probe).
const readinessCheckTimeout = 2 * time.Second

// ReadinessCheck verifies one upstream dependency (e.g. NATS connected, DB
// reachable). It should return quickly and respect ctx; a non-nil error marks
// the service not-ready.
type ReadinessCheck func(ctx context.Context) error

// Status represents the health check response
type Status struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Uptime    int64     `json:"uptime_seconds,omitempty"`
	Component string    `json:"component,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
	// Checks reports per-dependency readiness status ("ok" or "error: …"); only
	// populated on the readiness endpoint when checks are registered.
	Checks map[string]string `json:"checks,omitempty"`
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

	mu     sync.RWMutex
	checks []namedCheck // upstream readiness checks, run on every /readyz request
}

type namedCheck struct {
	name string
	fn   ReadinessCheck
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

	// Setup routes. /healthz + /readyz are the canonical Kubernetes probe paths;
	// the configured /health + /ready remain as backward-compatible aliases (so
	// existing Dockerfile/compose/k8s probes keep working). Registering the same
	// path twice would panic, so only add an alias when it differs.
	mux := http.NewServeMux()
	mux.HandleFunc(cfg.HealthPath, hs.handleHealth)
	if cfg.HealthPath != "/healthz" {
		mux.HandleFunc("/healthz", hs.handleHealth)
	}
	mux.HandleFunc(cfg.ReadyPath, hs.handleReady)
	if cfg.ReadyPath != "/readyz" {
		mux.HandleFunc("/readyz", hs.handleReady)
	}
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

// AddReadinessCheck registers an upstream dependency check run on every
// readiness probe (e.g. "nats", "database"). Safe to call before or after
// Start. Checks run in addition to the SetReady gate.
func (s *Server) AddReadinessCheck(name string, fn ReadinessCheck) {
	if s == nil || fn == nil {
		return
	}
	s.mu.Lock()
	s.checks = append(s.checks, namedCheck{name: name, fn: fn})
	s.mu.Unlock()
}

// runReadinessChecks runs every registered check (each bounded by
// readinessCheckTimeout) and returns the per-check results plus whether all
// passed.
func (s *Server) runReadinessChecks(ctx context.Context) (map[string]string, bool) {
	s.mu.RLock()
	checks := make([]namedCheck, len(s.checks))
	copy(checks, s.checks)
	s.mu.RUnlock()
	if len(checks) == 0 {
		return nil, true
	}
	results := make(map[string]string, len(checks))
	ok := true
	for _, c := range checks {
		cctx, cancel := context.WithTimeout(ctx, readinessCheckTimeout)
		err := c.fn(cctx)
		cancel()
		if err != nil {
			results[c.name] = "error: " + err.Error()
			ok = false
		} else {
			results[c.name] = "ok"
		}
	}
	return results, ok
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

// handleReady handles GET /ready and /readyz (readiness probe). Returns 200 OK
// only when the service is marked ready (the SetReady gate — flipped false on
// shutdown to drain) AND every registered upstream check passes.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := "ready"
	code := http.StatusOK

	if !s.isReady.Load() {
		status = "not ready"
		code = http.StatusServiceUnavailable
	}

	// Upstream dependency checks (NATS connected, DB reachable, …). Run even
	// when already not-ready so the response names every failing dependency.
	checks, ok := s.runReadinessChecks(r.Context())
	if !ok {
		status = "not ready"
		code = http.StatusServiceUnavailable
	}

	response := Status{
		Status:    status,
		Timestamp: time.Now(),
		Component: s.componentID,
		NodeID:    s.nodeID,
		Checks:    checks,
	}

	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(response)
}
