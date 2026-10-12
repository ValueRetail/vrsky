package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// SMTPConfig is the platform-wide mail identity (one set of SMTP credentials
// for the deployment, from SMTP_* env). Per-target state is just the recipient.
type SMTPConfig struct {
	Host     string // e.g. mailpit (dev) or smtp.sendgrid.net
	Port     string // e.g. 1025 / 587
	From     string // e.g. alerts@vrsky.example.com
	Username string // empty -> unauthenticated (dev SMTP)
	Password string
}

// sendMailFunc is smtp.SendMail's shape; tests swap it.
type sendMailFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// SendText sends one plain-text UTF-8 mail with the platform identity. It is
// what the alert e-mail target and the password-reset mail share.
func SendText(ctx context.Context, cfg SMTPConfig, to, subject, body string) error {
	return sendText(ctx, cfg, to, subject, body, smtp.SendMail)
}

func sendText(ctx context.Context, cfg SMTPConfig, to, subject, body string, send sendMailFunc) error {
	if cfg.Host == "" || cfg.From == "" {
		return fmt.Errorf("email: SMTP is not configured (set SMTP_HOST and SMTP_FROM)")
	}
	if to == "" {
		return fmt.Errorf("email: recipient is empty")
	}
	if strings.ContainsAny(to+subject, "\r\n") {
		return fmt.Errorf("email: recipient or subject contains a line break")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = newServerAuth(cfg.Username, cfg.Password, cfg.Host)
	}
	msg := strings.Join([]string{
		"From: " + cfg.From,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")
	if send == nil {
		send = smtp.SendMail
	}
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	if err := send(addr, auth, cfg.From, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("email: send via %s: %w", addr, err)
	}
	return nil
}

// Email sends an alert as a plain-text mail to one recipient.
type Email struct {
	SMTP SMTPConfig
	To   string
	// sendMail is swapped in tests; defaults to smtp.SendMail.
	sendMail sendMailFunc
}

func (e *Email) Send(ctx context.Context, alert *Alert) error {
	body := alert.Title() + "\r\n\r\n"
	if alert.Description != "" {
		body += alert.Description + "\r\n\r\n"
	}
	for k, v := range alert.Labels {
		body += k + " = " + v + "\r\n"
	}
	return sendText(ctx, e.SMTP, e.To, alert.Title(), body, e.sendMail)
}

// serverAuth authenticates with whichever mechanism the server offers:
// PLAIN where it is advertised, else LOGIN. Go's net/smtp ships PLAIN and
// CRAM-MD5 only, and Exchange Online (smtp.office365.com) offers LOGIN and
// XOAUTH2 — it answers PLAIN with "504 5.7.4 Unrecognized authentication
// type". Either way the password only ever crosses a TLS connection.
type serverAuth struct {
	username, password, host string
	plain                    smtp.Auth
	login                    bool
}

func newServerAuth(username, password, host string) *serverAuth {
	return &serverAuth{username: username, password: password, host: host, plain: smtp.PlainAuth("", username, password, host)}
}

func (a *serverAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && server.Name != "localhost" && server.Name != "127.0.0.1" && server.Name != "::1" {
		return "", nil, errors.New("email: refusing to send a password over an unencrypted connection")
	}
	offered := func(m string) bool {
		for _, o := range server.Auth {
			if strings.EqualFold(o, m) {
				return true
			}
		}
		return false
	}
	switch {
	case offered("PLAIN"):
		return a.plain.Start(server)
	case offered("LOGIN"):
		a.login = true
		return "LOGIN", nil, nil
	}
	return "", nil, fmt.Errorf("email: server offers no supported authentication mechanism (%s)", strings.Join(server.Auth, " "))
}

func (a *serverAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !a.login {
		return a.plain.Next(fromServer, more)
	}
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(fromServer)), ":"))) {
	case "username":
		return []byte(a.username), nil
	case "password":
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("email: unexpected LOGIN challenge %q", fromServer)
}
