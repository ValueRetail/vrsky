package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A fake of the two Microsoft endpoints the Graph mailer talks to.
type graphFake struct {
	t          *testing.T
	tokenCalls int
	tokenForm  map[string]string
	sends      []graphSend
	sendStatus int
	sendBody   string
}

type graphSend struct {
	path, bearer string
	payload      map[string]any
}

func newGraphFake(t *testing.T) (*graphFake, *Graph) {
	f := &graphFake{t: t, sendStatus: http.StatusAccepted}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls++
		_ = r.ParseForm()
		f.tokenForm = map[string]string{}
		for k := range r.PostForm {
			f.tokenForm[k] = r.PostForm.Get(k)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + string(rune('0'+f.tokenCalls)), "expires_in": 3600})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		f.sends = append(f.sends, graphSend{path: r.URL.Path, bearer: r.Header.Get("Authorization"), payload: payload})
		w.WriteHeader(f.sendStatus)
		_, _ = w.Write([]byte(f.sendBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	g := &Graph{TenantID: "tenant-1", ClientID: "client-1", ClientSecret: "s3cret", From: "vrsky@example.com",
		TokenURL: srv.URL + "/token", GraphURL: srv.URL + "/v1.0"}
	return f, g
}

func TestGraph_SendsThroughGraph(t *testing.T) {
	f, g := newGraphFake(t)
	if err := g.Send(context.Background(), "you@example.com", "Reset your password", "Hi there\r\nline two"); err != nil {
		t.Fatal(err)
	}
	if f.tokenForm["grant_type"] != "client_credentials" || f.tokenForm["client_id"] != "client-1" ||
		f.tokenForm["client_secret"] != "s3cret" || f.tokenForm["scope"] != "https://graph.microsoft.com/.default" {
		t.Fatalf("token request form = %v", f.tokenForm)
	}
	if len(f.sends) != 1 {
		t.Fatalf("%d sends", len(f.sends))
	}
	s := f.sends[0]
	if s.path != "/v1.0/users/vrsky@example.com/sendMail" || s.bearer != "Bearer tok-1" {
		t.Fatalf("path %s bearer %q", s.path, s.bearer)
	}
	msg := s.payload["message"].(map[string]any)
	body := msg["body"].(map[string]any)
	rcpt := msg["toRecipients"].([]any)[0].(map[string]any)["emailAddress"].(map[string]any)
	if msg["subject"] != "Reset your password" || body["contentType"] != "Text" || body["content"] != "Hi there\r\nline two" ||
		rcpt["address"] != "you@example.com" || s.payload["saveToSentItems"] != false {
		t.Fatalf("payload = %v", s.payload)
	}
}

func TestGraph_CachesTheTokenUntilExpiry(t *testing.T) {
	f, g := newGraphFake(t)
	now := time.Unix(1_760_000_000, 0)
	g.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := g.Send(ctx, "you@example.com", "s", "b"); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokenCalls != 1 {
		t.Fatalf("%d token requests for 3 sends, want 1", f.tokenCalls)
	}
	now = now.Add(3600*time.Second - 30*time.Second) // inside the last minute: refresh
	if err := g.Send(ctx, "you@example.com", "s", "b"); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 2 || f.sends[3].bearer != "Bearer tok-2" {
		t.Fatalf("after expiry: %d token requests, last bearer %q", f.tokenCalls, f.sends[3].bearer)
	}
}

func TestGraph_ReportsGraphErrorsWithoutTheToken(t *testing.T) {
	f, g := newGraphFake(t)
	f.sendStatus, f.sendBody = http.StatusForbidden, `{"error":{"code":"ErrorAccessDenied","message":"Access to OData is disabled."}}`
	err := g.Send(context.Background(), "you@example.com", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "ErrorAccessDenied") ||
		!strings.Contains(err.Error(), "Access to OData is disabled") || !strings.Contains(err.Error(), "vrsky@example.com") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "tok-") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error leaks a credential: %v", err)
	}
}

func TestGraph_RefusesUnconfigured(t *testing.T) {
	g := &Graph{TenantID: "t", ClientID: "c", From: "f@x"} // no secret
	if err := g.Send(context.Background(), "you@example.com", "s", "b"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
}
