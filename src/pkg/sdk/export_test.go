package sdk

import (
	"log/slog"
	"testing"
	"time"
)

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
