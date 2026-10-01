package managementapi

import (
	"net/http"
	"time"
)

// beginSSE prepares w for a long-lived Server-Sent Events stream.
//
// The server's WriteTimeout (30 s) covers the whole response, and a live
// stream is meant to outlive it. Past the deadline every write fails while the
// connection stays open, so the browser sits on a dead stream: it shows the
// first frames and nothing after, and never reconnects. Clearing the deadline
// needs every ResponseWriter wrapper in the chain to Unwrap; if one does not,
// this fails and the stream is cut at the timeout as before.
func beginSSE(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// The stream passes through a proxy in every deployment; without this,
	// nginx buffers it and events arrive in batches or not at all.
	w.Header().Set("X-Accel-Buffering", "no")
}

// writeSSEData writes one data frame and flushes it. An error means the
// stream is broken and the handler should return, so the connection closes
// and the browser reconnects instead of waiting on a stream nothing reaches.
// The flush goes through the ResponseController because http.Flusher cannot
// report a failure, and a small frame only fails when it is flushed.
func writeSSEData(w http.ResponseWriter, data []byte) error {
	if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}
