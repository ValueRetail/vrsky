package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func testAlert() *Alert {
	return &Alert{
		Name:        "PipelineDown",
		Status:      "firing",
		Severity:    "critical",
		Summary:     "no messages published for 10m",
		Description: "tenant t1 pipeline stopped producing",
		TenantID:    "t1",
		Labels:      map[string]string{"tenant_id": "t1"},
		StartsAt:    time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	}
}

func TestAlertTitle(t *testing.T) {
	a := testAlert()
	want := "[FIRING:critical] PipelineDown — no messages published for 10m"
	if got := a.Title(); got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
	a.Status = "resolved"
	if got := a.Title(); !strings.HasPrefix(got, "[RESOLVED:critical]") {
		t.Errorf("resolved Title() = %q, want [RESOLVED:critical] prefix", got)
	}
}

func TestSlack_Send(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Slack{WebhookURL: srv.URL, Client: srv.Client()}
	if err := s.Send(context.Background(), testAlert()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	atts, ok := got["attachments"].([]interface{})
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %v, want 1", got["attachments"])
	}
	att := atts[0].(map[string]interface{})
	if att["color"] != "#cc0000" {
		t.Errorf("color = %v, want #cc0000 (critical)", att["color"])
	}
	if !strings.Contains(att["text"].(string), "PipelineDown") {
		t.Errorf("text missing alert name: %v", att["text"])
	}
}

func TestSlack_SendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no_service", http.StatusNotFound)
	}))
	defer srv.Close()
	s := &Slack{WebhookURL: srv.URL, Client: srv.Client()}
	if err := s.Send(context.Background(), testAlert()); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

func TestEmail_Send(t *testing.T) {
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	e := &Email{
		SMTP: SMTPConfig{Host: "mail.local", Port: "1025", From: "alerts@vrsky.local"},
		To:   "ops@example.com",
		sendMail: func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
			gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, msg
			return nil
		},
	}
	if err := e.Send(context.Background(), testAlert()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAddr != "mail.local:1025" || gotFrom != "alerts@vrsky.local" {
		t.Errorf("addr/from = %q/%q", gotAddr, gotFrom)
	}
	if len(gotTo) != 1 || gotTo[0] != "ops@example.com" {
		t.Errorf("to = %v", gotTo)
	}
	if !strings.Contains(string(gotMsg), "Subject: [FIRING:critical] PipelineDown") {
		t.Errorf("message missing subject: %s", gotMsg)
	}
}

func TestEmail_RequiresConfig(t *testing.T) {
	e := &Email{To: "ops@example.com"}
	if err := e.Send(context.Background(), testAlert()); err == nil {
		t.Fatal("expected error without SMTP config")
	}
	e = &Email{SMTP: SMTPConfig{Host: "h", Port: "25", From: "f@x"}}
	if err := e.Send(context.Background(), testAlert()); err == nil {
		t.Fatal("expected error without recipient")
	}
}

func TestPagerDuty_Send(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	p := &PagerDuty{RoutingKey: "rk-123", Endpoint: srv.URL, Client: srv.Client()}
	if err := p.Send(context.Background(), testAlert()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got["routing_key"] != "rk-123" || got["event_action"] != "trigger" {
		t.Errorf("routing_key/event_action = %v/%v", got["routing_key"], got["event_action"])
	}
	if got["dedup_key"] != "PipelineDown:t1" {
		t.Errorf("dedup_key = %v", got["dedup_key"])
	}

	// Resolved alerts resolve the same dedup key.
	a := testAlert()
	a.Status = "resolved"
	if err := p.Send(context.Background(), a); err != nil {
		t.Fatalf("Send resolved: %v", err)
	}
	if got["event_action"] != "resolve" {
		t.Errorf("event_action = %v, want resolve", got["event_action"])
	}
}

func TestWebhook_SendWithHMAC(t *testing.T) {
	var gotSig string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-VRSky-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &Webhook{URL: srv.URL, Secret: "s3cret", Client: srv.Client()}
	if err := wh.Send(context.Background(), testAlert()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(gotBody)
	if want := hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Errorf("signature = %q, want %q", gotSig, want)
	}
	var a Alert
	if err := json.Unmarshal(gotBody, &a); err != nil || a.Name != "PipelineDown" {
		t.Errorf("body did not round-trip an Alert: %v %v", err, a.Name)
	}
}

func TestWebhook_NoSecretNoHeader(t *testing.T) {
	var hasSig bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasSig = r.Header["X-Vrsky-Signature"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	wh := &Webhook{URL: srv.URL, Client: srv.Client()}
	if err := wh.Send(context.Background(), testAlert()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if hasSig {
		t.Error("signature header set without a secret")
	}
}
