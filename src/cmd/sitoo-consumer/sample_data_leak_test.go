package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

// A base_url whose name resolves to a private address is refused at dial. The
// caller of /sample-data gets a fixed message: which address the name resolved
// to is internal detail and stays in the server log.
func TestSampleData_BlockedDialDoesNotLeakTheAddress(t *testing.T) {
	port, accepted := tcpCounter(t)
	var logs strings.Builder
	c := &sitooConsumer{
		httpClient: safeurl.NewClient(5*time.Second, false), // what Configure builds
		logger:     slog.New(slog.NewTextHandler(&logs, nil)),
	}
	body := fmt.Sprintf(`{"api_id":"id","api_password":"pw","account_id":1,"site_id":2,"base_url":"https://localhost:%s/v2","resource":"transactions"}`, port)
	w := httptest.NewRecorder()
	c.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body)))

	out := w.Body.String()
	if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "target address not allowed") {
		t.Errorf("body = %s, want ok=false with the fixed message", out)
	}
	for _, leak := range []string{"127.0.0.1", "::1", "refused"} {
		if strings.Contains(out, leak) {
			t.Errorf("body %s leaks %q", out, leak)
		}
	}
	if !strings.Contains(logs.String(), "127.0.0.1") && !strings.Contains(logs.String(), "::1") {
		t.Errorf("the detail should be in the server log, got %q", logs.String())
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want 0", n)
	}
}
