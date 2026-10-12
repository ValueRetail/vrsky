package notify

import (
	"context"
	"net/smtp"
	"strings"
	"testing"
)

func TestSendText_FormatsAPlainMail(t *testing.T) {
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg string
	send := func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, string(msg)
		return nil
	}
	cfg := SMTPConfig{Host: "mail.example", Port: "587", From: "vrsky@example.com"}
	if err := sendText(context.Background(), cfg, "you@example.com", "Reset your password", "Hello æøå\r\nline two", send); err != nil {
		t.Fatal(err)
	}
	if gotAddr != "mail.example:587" || gotFrom != "vrsky@example.com" || len(gotTo) != 1 || gotTo[0] != "you@example.com" {
		t.Fatalf("sent via %s from %s to %v", gotAddr, gotFrom, gotTo)
	}
	for _, want := range []string{"From: vrsky@example.com\r\n", "To: you@example.com\r\n", "Subject: Reset your password\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n\r\nHello æøå\r\nline two"} {
		if !strings.Contains(gotMsg, want) {
			t.Errorf("mail lacks %q:\n%s", want, gotMsg)
		}
	}
}

func TestSendText_RefusesWithoutConfigOrWithHeaderInjection(t *testing.T) {
	calls := 0
	send := func(string, smtp.Auth, string, []string, []byte) error { calls++; return nil }
	ctx := context.Background()
	if err := sendText(ctx, SMTPConfig{Port: "25"}, "a@b", "s", "b", send); err == nil {
		t.Error("no host: want an error")
	}
	ok := SMTPConfig{Host: "h", Port: "25", From: "f@x"}
	if err := sendText(ctx, ok, "a@b\r\nBcc: c@d", "s", "b", send); err == nil {
		t.Error("line break in recipient: want an error")
	}
	if err := sendText(ctx, ok, "a@b", "s\nX: y", "b", send); err == nil {
		t.Error("line break in subject: want an error")
	}
	if calls != 0 {
		t.Fatalf("send was called %d times", calls)
	}
}
