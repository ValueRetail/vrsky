package managementapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// These run the proxy behind a real http.Server. httptest.NewRecorder has no
// write deadline and no idle proxy in front of it, which is how a stream that
// died after the server's WriteTimeout passed every recorder-based test.

const streamConnID = "33333333-3333-3333-3333-333333333333"

// serveProxy starts the proxy on a real server with the given WriteTimeout,
// with upstream as the data-filter worker, and returns the stream URL.
func serveProxy(t *testing.T, upstream http.HandlerFunc, writeTimeout time.Duration) string {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	t.Setenv(workerAddrTemplateEnv, up.URL+"/%s/%d")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mock.ExpectQuery("SELECT id::text FROM connections").
		WithArgs(streamConnID, "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(streamConnID))

	h := &Handler{db: db}
	front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", streamConnID)
		r.SetPathValue("worker", "data-filter")
		h.ProxyWorkerEvents(w, r.WithContext(ContextWithTenantID(r.Context(), "tenant-a")))
	}))
	front.Config.WriteTimeout = writeTimeout
	front.Start()
	t.Cleanup(front.Close)
	return front.URL + "/api/v1/connections/" + streamConnID + "/workers/data-filter/events"
}

// readUntil reads the stream until it has seen want, the stream ends, or the
// deadline passes, and returns everything read.
func readUntil(t *testing.T, url, want string, within time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	var got strings.Builder
	r := bufio.NewReader(resp.Body)
	for !strings.Contains(got.String(), want) {
		line, err := r.ReadString('\n')
		got.WriteString(line)
		if err != nil {
			break
		}
	}
	return got.String()
}

// An event arriving after the server's WriteTimeout must still reach the
// browser. Before the fix the write failed silently and the connection hung
// open, so the builder showed "connected" and nothing after.
func TestProxyWorkerEvents_OutlivesServerWriteTimeout(t *testing.T) {
	url := serveProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"connected\"}\n\n"))
		w.(http.Flusher).Flush()
		time.Sleep(600 * time.Millisecond) // well past the 150 ms WriteTimeout
		_, _ = w.Write([]byte("data: {\"type\":\"delivered\"}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, 150*time.Millisecond)

	got := readUntil(t, url, `"delivered"`, 5*time.Second)
	if !strings.Contains(got, `"delivered"`) {
		t.Fatalf("the event after the server's WriteTimeout never arrived; stream was:\n%s", got)
	}
}

// An idle stream carries a comment heartbeat, so proxies with a 60 s read
// timeout do not cut it — and a heartbeat never lands inside a frame the
// upstream sent in two pieces.
func TestProxyWorkerEvents_HeartbeatBetweenFramesOnly(t *testing.T) {
	orig := sseHeartbeat
	sseHeartbeat = 20 * time.Millisecond
	t.Cleanup(func() { sseHeartbeat = orig })

	url := serveProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Half a frame, a pause spanning several heartbeat ticks, the rest.
		_, _ = w.Write([]byte(`data: {"type":"deli`))
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("vered\"}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, 0)

	got := readUntil(t, url, ": ping\n\n", 5*time.Second)
	if !strings.Contains(got, `data: {"type":"delivered"}`+"\n\n") {
		t.Errorf("a heartbeat was written inside a split frame; stream was:\n%q", got)
	}
	if !strings.Contains(got, ": ping\n\n") {
		t.Errorf("no heartbeat on an idle stream; stream was:\n%q", got)
	}
}
