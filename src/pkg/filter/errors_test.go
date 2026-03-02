package filter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestErrorHandler_RecordError(t *testing.T) {
	handler := NewErrorHandler(nil)

	handler.RecordError("warn", "test warning", errors.New("test error"), map[string]interface{}{
		"field": "value",
	})

	errors := handler.GetErrors()
	if len(errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(errors))
		return
	}

	if errors[0].Level != "warn" {
		t.Errorf("Expected level 'warn', got %s", errors[0].Level)
	}
	if errors[0].Message != "test warning" {
		t.Errorf("Expected message 'test warning', got %s", errors[0].Message)
	}
	if errors[0].Error != "test error" {
		t.Errorf("Expected error 'test error', got %s", errors[0].Error)
	}
}

func TestErrorHandler_ClearErrors(t *testing.T) {
	handler := NewErrorHandler(nil)

	handler.RecordError("error", "test", errors.New("test"), nil)
	if len(handler.GetErrors()) == 0 {
		t.Errorf("Expected errors before clear")
	}

	handler.ClearErrors()
	if len(handler.GetErrors()) != 0 {
		t.Errorf("Expected 0 errors after clear")
	}
}

func TestErrorHandler_MaxErrors(t *testing.T) {
	handler := NewErrorHandler(nil)

	// Record more than 1000 errors
	for i := 0; i < 1100; i++ {
		handler.RecordError("info", "test", nil, nil)
	}

	errors := handler.GetErrors()
	if len(errors) != 1000 {
		t.Errorf("Expected max 1000 errors, got %d", len(errors))
	}
}

func TestGracefulShutdown_RegisterHandler(t *testing.T) {
	gs := NewGracefulShutdown(5*time.Second, nil)

	called := false
	gs.Register(func(ctx context.Context) error {
		called = true
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_ = gs.Shutdown(ctx)

	if !called {
		t.Errorf("Handler should have been called")
	}
}

func TestGracefulShutdown_MultipleHandlers(t *testing.T) {
	gs := NewGracefulShutdown(5*time.Second, nil)

	var mu sync.Mutex
	count := 0
	for i := 0; i < 3; i++ {
		gs.Register(func(ctx context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			count++
			return nil
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_ = gs.Shutdown(ctx)

	mu.Lock()
	defer mu.Unlock()
	if count != 3 {
		t.Errorf("Expected 3 handlers called, got %d", count)
	}
}

func TestGracefulShutdown_Timeout(t *testing.T) {
	gs := NewGracefulShutdown(100*time.Millisecond, nil)

	gs.Register(func(ctx context.Context) error {
		// Simulate long-running handler
		time.Sleep(200 * time.Millisecond)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Should complete but log timeout
	_ = gs.Shutdown(ctx)
}

func TestRetryableError_ShouldRetry(t *testing.T) {
	tests := []struct {
		name        string
		err         *RetryableError
		shouldRetry bool
	}{
		{
			"first attempt, retryable",
			NewRetryableError(errors.New("test"), true, 1, 3),
			true,
		},
		{
			"last attempt, retryable",
			NewRetryableError(errors.New("test"), true, 3, 3),
			false,
		},
		{
			"non-retryable",
			NewRetryableError(errors.New("test"), false, 1, 3),
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.ShouldRetry() != tt.shouldRetry {
				t.Errorf("ShouldRetry() = %v, want %v", tt.err.ShouldRetry(), tt.shouldRetry)
			}
		})
	}
}

func TestPanicRecovery_Recover(t *testing.T) {
	pr := NewPanicRecovery(nil)

	// Test that recover doesn't panic
	func() {
		defer pr.Recover(context.Background())
		// No panic here
	}()

	// Test with panic
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic was not recovered properly")
			}
		}()
		defer pr.Recover(context.Background())
		panic("test panic")
	}()
}
