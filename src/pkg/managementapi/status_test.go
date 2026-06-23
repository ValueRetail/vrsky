package managementapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/promquery"
)

// fakeProm answers the status page's `up` queries: every component is up at
// 99% uptime, except NATS which is currently down.
func fakeStatusProm(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		val := "1"
		switch {
		case strings.Contains(q, "avg_over_time"):
			val = "0.99" // uptime fraction
		case strings.Contains(q, `job="nats"`):
			val = "0" // broker currently down
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1718000000,"` + val + `"]}]}}`))
	}))
}

func TestBuildStatus_Degraded(t *testing.T) {
	srv := fakeStatusProm(t)
	defer srv.Close()
	h := &Handler{}
	h.SetPrometheus(promquery.New(srv.URL, srv.Client()))

	s := h.buildStatus(context.Background())
	if s.Status != "degraded" {
		t.Errorf("overall = %q, want degraded (NATS down, others up)", s.Status)
	}
	byName := map[string]ComponentStatus{}
	for _, c := range s.Components {
		byName[c.Name] = c
	}
	if byName["Management API"].Status != "up" {
		t.Errorf("Management API = %q, want up", byName["Management API"].Status)
	}
	if byName["Message broker"].Status != "down" {
		t.Errorf("Message broker = %q, want down", byName["Message broker"].Status)
	}
	if got := byName["Management API"].Uptime24h; got < 0.98 || got > 1 {
		t.Errorf("uptime_24h = %v, want ~0.99", got)
	}
	if s.GeneratedAt == "" {
		t.Error("generated_at not set")
	}
}

func TestBuildStatus_UnknownWithoutProm(t *testing.T) {
	h := &Handler{} // no Prometheus client
	s := h.buildStatus(context.Background())
	if s.Status != "unknown" {
		t.Errorf("overall = %q, want unknown", s.Status)
	}
	for _, c := range s.Components {
		if c.Status != "unknown" {
			t.Errorf("%s = %q, want unknown", c.Name, c.Status)
		}
	}
}

func TestServeStatusJSON(t *testing.T) {
	srv := fakeStatusProm(t)
	defer srv.Close()
	h := &Handler{}
	h.SetPrometheus(promquery.New(srv.URL, srv.Client()))

	rec := httptest.NewRecorder()
	h.ServeStatusJSON(rec, httptest.NewRequest(http.MethodGet, "/status.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"status"`) {
		t.Errorf("body missing status field: %s", rec.Body.String())
	}
}
