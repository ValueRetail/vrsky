package natsdiscovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolve_ReturnsURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tenants/t-1/nats-instances" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"instances":[],"urls":["nats://a:4222","nats://b:4222"]}}`))
	}))
	defer srv.Close()

	r := New(srv.URL, "t-1", "")
	joined, err := r.ResolveJoined(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if joined != "nats://a:4222,nats://b:4222" {
		t.Fatalf("joined = %q, want the two servers comma-joined", joined)
	}
}

func TestResolve_DisabledWhenUnconfigured(t *testing.T) {
	if New("", "t-1", "").Enabled() {
		t.Error("expected disabled with empty base URL")
	}
	if New("http://x", "", "").Enabled() {
		t.Error("expected disabled with empty tenant")
	}
	urls, err := New("", "", "").Resolve(context.Background())
	if err != nil || urls != nil {
		t.Fatalf("disabled resolve should return (nil,nil), got (%v,%v)", urls, err)
	}
}

func TestResolve_ErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "t-1", "").Resolve(context.Background()); err == nil {
		t.Fatal("expected error on non-200 response")
	}
}
