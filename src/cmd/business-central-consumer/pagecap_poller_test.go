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

// A poll the cap stopped logs the Warn and no Error line (#699). The Warn
// names the page size and says what happens next.
func TestRunPoller_PageCapLogsAWarnAndNoError(t *testing.T) {
	lowerPageCap(t, 2)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		fmt.Fprint(w, `{"value":[{"number":"R"}],"@odata.nextLink":"?page=2"}`)
	}))
	defer api.Close()

	c, _, _ := newTestConsumer()
	logs := &lockedBuffer{}
	c.logger = slog.New(slog.NewTextHandler(logs, nil))
	cfg := &BCConfig{
		AADTenantID: "t1", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: api.URL + "/api/v2.0", TokenURL: api.URL + "/token",
		PollIntervalSeconds: 3600, PageSize: 50,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.runPoller(ctx, "conn-699", "tenant-1", cfg, nil) }()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), "page_cap=2") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // an Error line would follow the Warn at once
	cancel()
	<-done

	out := logs.String()
	for _, want := range []string{"level=WARN", "connection_id=conn-699", "page_cap=2", "page_size=50", "re-reads and re-sends", "never delivered"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %q", want, out)
		}
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("a capped poll logged an Error: %q", out)
	}
}
