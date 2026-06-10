package notify

import (
	"context"
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

// Email sends an alert as a plain-text mail to one recipient.
type Email struct {
	SMTP SMTPConfig
	To   string

	// sendMail is swapped in tests; defaults to smtp.SendMail.
	sendMail func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

func (e *Email) Send(ctx context.Context, alert *Alert) error {
	if e.SMTP.Host == "" || e.SMTP.From == "" {
		return fmt.Errorf("email: SMTP is not configured (set SMTP_HOST and SMTP_FROM)")
	}
	if e.To == "" {
		return fmt.Errorf("email: recipient is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var auth smtp.Auth
	if e.SMTP.Username != "" {
		auth = smtp.PlainAuth("", e.SMTP.Username, e.SMTP.Password, e.SMTP.Host)
	}

	body := alert.Title() + "\r\n\r\n"
	if alert.Description != "" {
		body += alert.Description + "\r\n\r\n"
	}
	for k, v := range alert.Labels {
		body += k + " = " + v + "\r\n"
	}

	msg := strings.Join([]string{
		"From: " + e.SMTP.From,
		"To: " + e.To,
		"Subject: " + alert.Title(),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")

	send := e.sendMail
	if send == nil {
		send = smtp.SendMail
	}
	addr := net.JoinHostPort(e.SMTP.Host, e.SMTP.Port)
	if err := send(addr, auth, e.SMTP.From, []string{e.To}, []byte(msg)); err != nil {
		return fmt.Errorf("email: send via %s: %w", addr, err)
	}
	return nil
}
