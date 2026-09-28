package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The worker-events proxy lifts the server's WriteTimeout for its stream with
// http.ResponseController, which only reaches the connection if every
// ResponseWriter wrapper in the chain Unwraps. Logging and Metrics wrap every
// request (Audit skips GETs), so they are the ones that matter.
func TestMiddlewareChain_AllowsLiftingWriteDeadline(t *testing.T) {
	var deadlineErr error
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deadlineErr = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond) // past the 150 ms WriteTimeout
		_, _ = io.WriteString(w, "late")
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewUnstartedServer(LoggingMiddleware(logger)(MetricsMiddleware(inner)))
	srv.Config.WriteTimeout = 150 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/connections/x/workers/data-filter/events")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if deadlineErr != nil {
		t.Errorf("SetWriteDeadline through Logging+Metrics: %v", deadlineErr)
	}
	if string(body) != "late" {
		t.Errorf("body = %q, want the write made after the WriteTimeout", body)
	}
}
