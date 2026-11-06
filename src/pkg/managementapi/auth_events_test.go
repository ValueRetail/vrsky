package managementapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// auditFailRepo cannot write security-log rows.
type auditFailRepo struct{ *MockRepository }

func (auditFailRepo) CreateAuthAuditLog(context.Context, *AuthAuditLog) error {
	return errors.New("failed to create auth audit log: pq: connection refused")
}

// A security-log row that cannot be written is logged, without the row's
// contents, and the request still succeeds.
func TestLogAuthEvent_LostRowIsLogged(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := NewHandler(auditFailRepo{NewMockRepository()}, NewValidator())
	const token, ua = "verify-token-secret", "agent-that-must-not-be-logged/1"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/verify-email?token="+token, nil)
	req.Header.Set("User-Agent", ua)
	rec := httptest.NewRecorder()
	h.VerifyEmail(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("verify answered %d, want 302 despite the lost audit row", rec.Code)
	}
	out := logs.String()
	for _, want := range []string{"level=WARN", "auth audit row not written", "event_type=email_verified", "status=success", "connection refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{token, ua} {
		if strings.Contains(out, leak) {
			t.Errorf("log holds %q:\n%s", leak, out)
		}
	}
}

func TestAuditText(t *testing.T) {
	cases := []struct {
		name, in string
		max      int
		want     string
	}{
		{"short", "abc", 5, "abc"},
		{"exactly max", "abcde", 5, "abcde"},
		{"over max", "abcdef", 5, "abcde"},
		{"counts characters, not bytes", "øøøøøø", 5, "øøøøø"},
		{"no limit", strings.Repeat("x", 1000), 0, strings.Repeat("x", 1000)},
		{"NUL removed", "a\x00b", 5, "ab"},
		{"invalid UTF-8 replaced", "a\xffb", 5, "a\uFFFDb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := auditText(c.in, c.max); got != c.want {
				t.Errorf("auditText(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
			}
		})
	}
}
