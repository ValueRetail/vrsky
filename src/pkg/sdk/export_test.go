package sdk

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

// PendingRetriesForTest is how many start retries g has pending. mu is the
// owner's lock.
func PendingRetriesForTest(g *StartGuard, mu sync.Locker) int {
	mu.Lock()
	defer mu.Unlock()
	return len(g.retries)
}

// WaitRetriesForTest waits up to d for every retry loop g started to end,
// and reports whether they did: a loop still running after its retry was
// ended is a leaked goroutine.
func WaitRetriesForTest(g *StartGuard, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		g.retryWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// SetDispatchBackoffForTest shortens the dispatch durable's redelivery
// schedule for one test, so a retried message reaches the DLQ in seconds.
func SetDispatchBackoffForTest(t *testing.T, b []time.Duration) {
	t.Helper()
	old := dispatchBackoff
	dispatchBackoff = b
	t.Cleanup(func() { dispatchBackoff = old })
}

// SetLoggerForTest makes the runner log to l for one test.
func SetLoggerForTest(t *testing.T, l *slog.Logger) {
	t.Helper()
	old := newLogger
	newLogger = func(string) *slog.Logger { return l }
	t.Cleanup(func() { newLogger = old })
}
