package managementapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/notify"
)

// Password-reset mail (plans/auth-leftovers.md). The platform sends it with
// the same SMTP identity the alert e-mail targets use; links point at
// PUBLIC_URL, never at whatever Host header the request carried.

const resetUnavailableMessage = "password reset is not available on this installation yet — ask your workspace owner to set your password"

// MailerFromEnv returns the platform mailer, or nil when SMTP_HOST or
// SMTP_FROM is unset.
func MailerFromEnv() func(ctx context.Context, to, subject, body string) error {
	cfg := smtpFromEnv()
	if cfg.Host == "" || cfg.From == "" {
		return nil
	}
	return func(ctx context.Context, to, subject, body string) error {
		return notify.SendText(ctx, cfg, to, subject, body)
	}
}

// SetMailer sets how the API sends mail; nil means it cannot.
func (h *Handler) SetMailer(m func(ctx context.Context, to, subject, body string) error) {
	h.mailer = m
}

// SetPublicURL sets the origin links in mail point at: scheme and host only,
// e.g. https://vrsky.valueretail.no. Empty clears it.
func (h *Handler) SetPublicURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		h.publicURL = ""
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("PUBLIC_URL %q: %w", raw, err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("PUBLIC_URL %q: want scheme and host only, like https://vrsky.example.com", raw)
	}
	h.publicURL = u.Scheme + "://" + u.Host
	return nil
}

// CanSendMail reports whether the handler can send a password-reset mail
// (main logs a warning at boot when it cannot).
func CanSendMail(h *Handler) bool { return h.canSendResetMail() }

// canSendResetMail is the one question ForgotPassword asks before it looks
// anything up, so the answer is the same for every address.
func (h *Handler) canSendResetMail() bool { return h.mailer != nil && h.publicURL != "" }

func (h *Handler) resetLink(rawToken string) string {
	return h.publicURL + "/reset-password?token=" + url.QueryEscape(rawToken)
}

func resetMailBody(name, link string, expires time.Time) string {
	if name == "" {
		name = "there"
	}
	return fmt.Sprintf("Hi %s,\r\n\r\n"+
		"Someone asked to reset the password for this VRSky account. If that was you, open this link and choose a new password:\r\n\r\n"+
		"%s\r\n\r\n"+
		"The link works once and expires at %s. If you did not ask for this, ignore this mail; your password stays as it is.\r\n",
		name, link, expires.UTC().Format("2006-01-02 15:04 UTC"))
}
