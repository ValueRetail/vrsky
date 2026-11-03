package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a log sink the poller goroutine and the test can share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A poll the cap stopped logs the Warn and no Error line (#699).
func TestRunPoller_PageCapLogsAWarnAndNoError(t *testing.T) {
	lowerPageCap(t, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"d":{"results":[{"SalesOrder":"x"}],"__next":"A_SalesOrder?$skiptoken=P"}}`)
	}))
	defer srv.Close()

	c, _ := collectingConsumer()
	logs := &lockedBuffer{}
	c.logger = slog.New(slog.NewTextHandler(logs, nil))
	cfg := &SAPConfig{APIBaseURL: srv.URL + svcPath, EntitySet: "A_SalesOrder", Username: "u", Password: "p", PollIntervalSeconds: 3600}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.runPoller(ctx, "conn-699", "tenant", cfg) }()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), "page_cap=2") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // an Error line would follow the Warn at once
	cancel()
	<-done

	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "connection_id=conn-699") || !strings.Contains(out, "page_cap=2") {
		t.Errorf("no Warn naming the connection and the cap: %q", out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("a capped poll logged an Error: %q", out)
	}
}
