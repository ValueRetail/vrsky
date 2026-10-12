package notify

import (
	"context"
	"net/smtp"
	"strings"
	"testing"
)

// Exchange Online offers LOGIN and XOAUTH2, never PLAIN; a relay like
// mailpit or Postfix offers PLAIN. The chooser must follow the server.
func TestServerAuth_FollowsWhatTheServerOffers(t *testing.T) {
	a := newServerAuth("vrsky@example.com", "pw", "smtp.office365.com")
	mech, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.office365.com", TLS: true, Auth: []string{"LOGIN", "XOAUTH2"}})
	if err != nil || mech != "LOGIN" {
		t.Fatalf("Exchange-style server: mechanism %q, err %v; want LOGIN", mech, err)
	}
	if u, err := a.Next([]byte("Username:"), true); err != nil || string(u) != "vrsky@example.com" {
		t.Fatalf("username challenge: %q %v", u, err)
	}
	if p, err := a.Next([]byte("Password:"), true); err != nil || string(p) != "pw" {
		t.Fatalf("password challenge: %q %v", p, err)
	}
	if _, err := a.Next([]byte("Something else:"), true); err == nil {
		t.Fatal("an unknown challenge must not get the password")
	}

	b := newServerAuth("u", "pw", "mail.example")
	mech, payload, err := b.Start(&smtp.ServerInfo{Name: "mail.example", TLS: true, Auth: []string{"PLAIN", "LOGIN"}})
	if err != nil || mech != "PLAIN" || string(payload) != "\x00u\x00pw" {
		t.Fatalf("PLAIN-capable server: mechanism %q payload %q err %v", mech, payload, err)
	}

	c := newServerAuth("u", "pw", "mail.example")
	if _, _, err := c.Start(&smtp.ServerInfo{Name: "mail.example", TLS: false, Auth: []string{"LOGIN"}}); err == nil {
		t.Fatal("no TLS: the password must not be sent")
	}
	d := newServerAuth("u", "pw", "mail.example")
	if _, _, err := d.Start(&smtp.ServerInfo{Name: "mail.example", TLS: true, Auth: []string{"XOAUTH2"}}); err == nil {
		t.Fatal("no usable mechanism: want an error, not a guess")
	}
}

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
