package filter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Rate limit errors
var (
	ErrRateLimitExceeded      = errors.New("rate limit exceeded")
	ErrQueueFull              = errors.New("rate limit queue full")
	ErrInvalidRateLimitRule   = errors.New("invalid rate limit rule configuration")
	ErrConcurrentLimitExceeded = errors.New("concurrent limit exceeded")
)

// ErrorHandler provides structured error handling with context
type ErrorHandler struct {
	logger *slog.Logger
	mu     sync.RWMutex
	errors []ErrorRecord
}

// ErrorRecord represents a recorded error
type ErrorRecord struct {
	Timestamp time.Time
	Level     string
	Message   string
	Error     string
	Context   map[string]interface{}
}

// NewErrorHandler creates a new error handler
func NewErrorHandler(logger *slog.Logger) *ErrorHandler {
	if logger == nil {
		logger = slog.Default()
	}

	return &ErrorHandler{
		logger: logger,
		errors: make([]ErrorRecord, 0),
	}
}

// RecordError records an error for tracking and debugging
func (eh *ErrorHandler) RecordError(level string, message string, err error, context map[string]interface{}) {
	eh.mu.Lock()
	defer eh.mu.Unlock()

	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	record := ErrorRecord{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
		Error:     errStr,
		Context:   context,
	}

	eh.errors = append(eh.errors, record)

	// Keep only last 1000 errors
	if len(eh.errors) > 1000 {
		start := len(eh.errors) - 1000
		eh.errors = eh.errors[start:]
	}
}

// GetErrors returns all recorded errors
func (eh *ErrorHandler) GetErrors() []ErrorRecord {
	eh.mu.RLock()
	defer eh.mu.RUnlock()

	result := make([]ErrorRecord, len(eh.errors))
	copy(result, eh.errors)
	return result
}

// ClearErrors clears all recorded errors
func (eh *ErrorHandler) ClearErrors() {
	eh.mu.Lock()
	defer eh.mu.Unlock()

	eh.errors = make([]ErrorRecord, 0)
}

// GracefulShutdown handles graceful shutdown with signal handling
type GracefulShutdown struct {
	logger           *slog.Logger
	shutdownTimeout  time.Duration
	shutdownHandlers []ShutdownHandler
	mu               sync.Mutex
}

// ShutdownHandler is a function that handles shutdown
type ShutdownHandler func(ctx context.Context) error

// NewGracefulShutdown creates a new graceful shutdown handler
func NewGracefulShutdown(timeout time.Duration, logger *slog.Logger) *GracefulShutdown {
	if logger == nil {
		logger = slog.Default()
	}

	if timeout == 0 {
		timeout = 15 * time.Second
	}

	return &GracefulShutdown{
		logger:           logger,
		shutdownTimeout:  timeout,
		shutdownHandlers: make([]ShutdownHandler, 0),
	}
}

// Register registers a shutdown handler
func (gs *GracefulShutdown) Register(handler ShutdownHandler) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if handler != nil {
		gs.shutdownHandlers = append(gs.shutdownHandlers, handler)
	}
}

// Shutdown triggers graceful shutdown of all handlers
func (gs *GracefulShutdown) Shutdown(ctx context.Context) error {
	gs.mu.Lock()
	handlers := make([]ShutdownHandler, len(gs.shutdownHandlers))
	copy(handlers, gs.shutdownHandlers)
	gs.mu.Unlock()

	gs.logger.InfoContext(ctx, "Starting graceful shutdown", "handlers", len(handlers))

	// Create context with timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, gs.shutdownTimeout)
	defer cancel()

	// Run all handlers in parallel
	var wg sync.WaitGroup
	errs := make(chan error, len(handlers))

	for _, handler := range handlers {
		wg.Add(1)
		go func(h ShutdownHandler) {
			defer wg.Done()
			if err := h(shutdownCtx); err != nil {
				gs.logger.WarnContext(ctx, "Error during shutdown", "error", err)
				errs <- err
			}
		}(handler)
	}

	// Wait for all handlers to complete
	go func() {
		wg.Wait()
		close(errs)
	}()

	// Collect errors
	var shutdownErrs []error
	for err := range errs {
		shutdownErrs = append(shutdownErrs, err)
	}

	if len(shutdownErrs) > 0 {
		gs.logger.ErrorContext(ctx, "Errors during shutdown", "count", len(shutdownErrs))
		return fmt.Errorf("shutdown errors: %v", shutdownErrs)
	}

	gs.logger.InfoContext(ctx, "Graceful shutdown completed")
	return nil
}

// WaitForShutdownSignal waits for OS shutdown signals (SIGINT, SIGTERM)
func WaitForShutdownSignal(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		logger.InfoContext(ctx, "Received shutdown signal", "signal", sig.String())
	case <-ctx.Done():
		logger.InfoContext(ctx, "Context cancelled")
	}
}

// RetryableError represents an error that can be retried
type RetryableError struct {
	Err         error
	Retryable   bool
	RetryAfter  time.Duration
	Attempt     int
	MaxAttempts int
}

// Error returns the error message
func (re *RetryableError) Error() string {
	return fmt.Sprintf("attempt %d/%d: %v", re.Attempt, re.MaxAttempts, re.Err)
}

// ShouldRetry determines if the error should be retried
func (re *RetryableError) ShouldRetry() bool {
	return re.Retryable && re.Attempt < re.MaxAttempts
}

// NewRetryableError creates a new retryable error
func NewRetryableError(err error, retryable bool, attempt int, maxAttempts int) *RetryableError {
	return &RetryableError{
		Err:         err,
		Retryable:   retryable,
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
	}
}

// PanicRecovery handles panic recovery
type PanicRecovery struct {
	logger *slog.Logger
}

// NewPanicRecovery creates a new panic recovery handler
func NewPanicRecovery(logger *slog.Logger) *PanicRecovery {
	if logger == nil {
		logger = slog.Default()
	}

	return &PanicRecovery{
		logger: logger,
	}
}

// Recover recovers from a panic and logs it
func (pr *PanicRecovery) Recover(ctx context.Context) {
	if r := recover(); r != nil {
		pr.logger.ErrorContext(ctx, "Panic recovered",
			"panic", r,
		)
	}
}

// RecoverWithCallback recovers from a panic and calls a callback
func (pr *PanicRecovery) RecoverWithCallback(ctx context.Context, callback func(interface{})) {
	if r := recover(); r != nil {
		pr.logger.ErrorContext(ctx, "Panic recovered",
			"panic", r,
		)
		if callback != nil {
			callback(r)
		}
	}
}
