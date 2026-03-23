package health

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewServer(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want struct {
			port        int
			healthPath  string
			readyPath   string
			metricsPath string
		}
	}{
		{
			name: "default values",
			cfg:  Config{},
			want: struct {
				port        int
				healthPath  string
				readyPath   string
				metricsPath string
			}{8080, "/health", "/ready", "/metrics"},
		},
		{
			name: "custom values",
			cfg: Config{
				Port:        9090,
				HealthPath:  "/healthz",
				ReadyPath:   "/readyz",
				MetricsPath: "/prometheus",
			},
			want: struct {
				port        int
				healthPath  string
				readyPath   string
				metricsPath string
			}{9090, "/healthz", "/readyz", "/prometheus"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewServer(tt.cfg)
			if s.healthPath != tt.want.healthPath {
				t.Errorf("healthPath = %v, want %v", s.healthPath, tt.want.healthPath)
			}
			if s.readyPath != tt.want.readyPath {
				t.Errorf("readyPath = %v, want %v", s.readyPath, tt.want.readyPath)
			}
			if s.metricsPath != tt.want.metricsPath {
				t.Errorf("metricsPath = %v, want %v", s.metricsPath, tt.want.metricsPath)
			}
		})
	}
}

func TestServer_handleHealth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer(Config{
		Logger:      logger,
		ComponentID: "test-component",
		NodeID:      "node-123",
	})

	tests := []struct {
		name       string
		healthy    bool
		wantStatus int
		wantBody   string
	}{
		{
			name:       "healthy",
			healthy:    true,
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:       "unhealthy",
			healthy:    false,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "unhealthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.SetHealthy(tt.healthy)

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()

			s.handleHealth(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status code = %v, want %v", resp.StatusCode, tt.wantStatus)
			}

			var status Status
			if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if status.Status != tt.wantBody {
				t.Errorf("status = %v, want %v", status.Status, tt.wantBody)
			}

			if status.Component != "test-component" {
				t.Errorf("component = %v, want test-component", status.Component)
			}

			if status.NodeID != "node-123" {
				t.Errorf("node_id = %v, want node-123", status.NodeID)
			}
		})
	}
}

func TestServer_handleReady(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer(Config{
		Logger:      logger,
		ComponentID: "test-component",
	})

	tests := []struct {
		name       string
		ready      bool
		wantStatus int
		wantBody   string
	}{
		{
			name:       "ready",
			ready:      true,
			wantStatus: http.StatusOK,
			wantBody:   "ready",
		},
		{
			name:       "not ready",
			ready:      false,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "not ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.SetReady(tt.ready)

			req := httptest.NewRequest(http.MethodGet, "/ready", nil)
			w := httptest.NewRecorder()

			s.handleReady(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status code = %v, want %v", resp.StatusCode, tt.wantStatus)
			}

			var status Status
			if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if status.Status != tt.wantBody {
				t.Errorf("status = %v, want %v", status.Status, tt.wantBody)
			}
		})
	}
}

func TestServer_SettersAndGetters(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer(Config{Logger: logger})

	// Default state
	if !s.IsHealthy() {
		t.Error("expected IsHealthy() to be true by default")
	}
	if !s.IsReady() {
		t.Error("expected IsReady() to be true by default")
	}

	// Set unhealthy
	s.SetHealthy(false)
	if s.IsHealthy() {
		t.Error("expected IsHealthy() to be false after SetHealthy(false)")
	}

	// Set not ready
	s.SetReady(false)
	if s.IsReady() {
		t.Error("expected IsReady() to be false after SetReady(false)")
	}

	// Set healthy again
	s.SetHealthy(true)
	if !s.IsHealthy() {
		t.Error("expected IsHealthy() to be true after SetHealthy(true)")
	}

	// Set ready again
	s.SetReady(true)
	if !s.IsReady() {
		t.Error("expected IsReady() to be true after SetReady(true)")
	}
}

func TestServer_StartStop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer(Config{
		Port:   0, // Will use default 8080
		Logger: logger,
	})

	ctx := context.Background()

	// Start should not error
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	// Stop should not error
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestServer_NilSafety(t *testing.T) {
	var s *Server

	// These should not panic
	s.SetHealthy(true)
	s.SetReady(true)

	if s.IsHealthy() {
		t.Error("expected IsHealthy() to be false for nil server")
	}
	if s.IsReady() {
		t.Error("expected IsReady() to be false for nil server")
	}

	// Stop should handle nil gracefully
	if err := s.Stop(context.Background()); err != nil {
		t.Errorf("Stop() on nil server should return nil, got %v", err)
	}
}
