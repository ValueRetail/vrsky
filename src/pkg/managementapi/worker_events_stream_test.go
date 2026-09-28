package managementapi

import (
	"bufio"
	"context"
	"net"
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

	// Frames are forwarded whole, so the heartbeat sent during the pause
	// comes first and the frame follows intact.
	got := readUntil(t, url, `"delivered"}`+"\n\n", 5*time.Second)
	if !strings.Contains(got, `data: {"type":"delivered"}`+"\n\n") {
		t.Errorf("the split frame did not arrive intact; stream was:\n%q", got)
	}
	if !strings.Contains(got, ": ping\n\n") {
		t.Errorf("no heartbeat on an idle stream; stream was:\n%q", got)
	}
}

// serveProxyReplicas is serveProxy with several upstreams standing in for the
// replicas of one worker, the way a headless Service resolves to every pod.
func serveProxyReplicas(t *testing.T, upstreams ...http.HandlerFunc) string {
	t.Helper()
	var urls []string
	for _, h := range upstreams {
		up := httptest.NewServer(h)
		t.Cleanup(up.Close)
		urls = append(urls, up.URL)
	}
	orig := workerUpstreams
	workerUpstreams = func(context.Context, workerEventSource) ([]string, error) { return urls, nil }
	t.Cleanup(func() { workerUpstreams = orig })
	return serveProxy(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }, 0)
}

func sseReplica(hello, frame string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(hello + frame))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
}

// The transforms run two replicas, each with its own event hub, and the
// pipeline's messages are split between them. The panel must hear both — one
// stream would show half the traffic and look like a pipeline dropping data.
func TestProxyWorkerEvents_FansInAcrossReplicas(t *testing.T) {
	hello := "data: {\"type\":\"connected\"}\n\n"
	url := serveProxyReplicas(t,
		sseReplica(hello, "data: {\"type\":\"converted\",\"from\":\"a\"}\n\n"),
		sseReplica(hello, "data: {\"type\":\"converted\",\"from\":\"b\"}\n\n"),
	)
	got := readUntil(t, url, "NEVER", 1500*time.Millisecond)
	for _, want := range []string{`"from":"a"`, `"from":"b"`} {
		if !strings.Contains(got, want) {
			t.Errorf("frames from one replica are missing (%s); stream was:\n%q", want, got)
		}
	}
	if n := strings.Count(got, `"type":"connected"`); n != 1 {
		t.Errorf("%d connected frames, want exactly 1 — each replica greets, the panel needs one", n)
	}
}

// One replica down must not hide the other: its frames still flow, and the
// stream says what is missing.
func TestProxyWorkerEvents_ReportsAnUnreachableReplica(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadURL := "http://" + dead.Addr().String()
	dead.Close() // nothing listens there any more

	live := httptest.NewServer(sseReplica("", "data: {\"type\":\"converted\",\"from\":\"live\"}\n\n"))
	t.Cleanup(live.Close)
	orig := workerUpstreams
	workerUpstreams = func(context.Context, workerEventSource) ([]string, error) { return []string{deadURL, live.URL}, nil }
	t.Cleanup(func() { workerUpstreams = orig })
	url := serveProxy(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }, 0)

	got := readUntil(t, url, `"from":"live"`, 5*time.Second)
	if !strings.Contains(got, `"from":"live"`) {
		t.Fatalf("the live replica's frame never arrived; stream was:\n%q", got)
	}
	if !strings.Contains(got, "cannot reach 1 of 2 data-filter instances") {
		t.Errorf("the missing replica was not reported; stream was:\n%q", got)
	}
}
