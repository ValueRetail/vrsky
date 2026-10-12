package managementapi

import (
	"testing"

	"github.com/ValueRetail/vrsky/pkg/notify"
)

// Graph wins when fully configured, SMTP next, nothing otherwise; a partial
// Graph configuration never half-works.
func TestMailerFromEnv_PicksGraphThenSMTPThenNothing(t *testing.T) {
	for _, k := range []string{"GRAPH_TENANT_ID", "GRAPH_CLIENT_ID", "GRAPH_CLIENT_SECRET", "MAIL_FROM", "SMTP_HOST", "SMTP_FROM", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD"} {
		t.Setenv(k, "")
	}
	if m, how := MailerFromEnv(); m != nil || how != "" {
		t.Fatalf("nothing set: %T %q", m, how)
	}
	t.Setenv("SMTP_HOST", "mailpit")
	t.Setenv("SMTP_FROM", "alerts@vrsky.local")
	m, how := MailerFromEnv()
	if _, ok := m.(*notify.SMTP); !ok || how != "mail: SMTP via mailpit as alerts@vrsky.local" {
		t.Fatalf("SMTP set: %T %q", m, how)
	}
	t.Setenv("GRAPH_TENANT_ID", "tenant")
	t.Setenv("GRAPH_CLIENT_ID", "client")
	t.Setenv("MAIL_FROM", "vrsky@example.com")
	if m, _ := MailerFromEnv(); m != nil {
		if _, ok := m.(*notify.SMTP); !ok {
			t.Fatalf("Graph without a secret must not be chosen: %T", m)
		}
	}
	t.Setenv("GRAPH_CLIENT_SECRET", "s")
	m, how = MailerFromEnv()
	g, ok := m.(*notify.Graph)
	if !ok || g.From != "vrsky@example.com" || g.TenantID != "tenant" || how != "mail: Microsoft Graph as vrsky@example.com" {
		t.Fatalf("Graph set: %T %q", m, how)
	}
}
