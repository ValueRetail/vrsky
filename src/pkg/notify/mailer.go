package notify

import "context"

// Mailer sends one plain-text mail with the platform's identity. Two
// implementations: SMTP (a relay, or mailpit in dev) and Graph (Microsoft
// 365 through Microsoft Graph, for tenants where security defaults block
// SMTP authentication). The password-reset mail and the alert e-mail target
// both go through whichever one the installation has.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// MailerFunc adapts a function to Mailer (tests capture mail with it).
type MailerFunc func(ctx context.Context, to, subject, body string) error

// Send calls f.
func (f MailerFunc) Send(ctx context.Context, to, subject, body string) error {
	return f(ctx, to, subject, body)
}

// SMTP is the Mailer over a relay.
type SMTP struct {
	Config SMTPConfig
	// sendMail is swapped in tests; defaults to smtp.SendMail.
	sendMail sendMailFunc
}

// Send sends through the relay.
func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	return sendText(ctx, s.Config, to, subject, body, s.sendMail)
}
