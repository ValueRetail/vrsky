// Package notify delivers alert notifications to per-tenant targets (#84):
// Slack incoming webhooks, email (SMTP), PagerDuty Events API v2, and generic
// webhooks. The management-api's Alertmanager receiver normalizes incoming
// alerts into Alert values and fans them out to a Notifier per target.
package notify

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Alert is one normalized alert (firing or resolved) ready for delivery.
type Alert struct {
	Name        string            `json:"name"`     // alertname label
	Status      string            `json:"status"`   // "firing" | "resolved"
	Severity    string            `json:"severity"` // "critical" | "warning" | "info"
	Summary     string            `json:"summary"`
	Description string            `json:"description,omitempty"`
	TenantID    string            `json:"tenant_id,omitempty"` // empty for platform alerts
	Labels      map[string]string `json:"labels,omitempty"`
	StartsAt    time.Time         `json:"starts_at"`
}

// Title renders the conventional one-line headline, e.g.
// "[FIRING:critical] PipelineDown — no messages published for tenant X".
func (a *Alert) Title() string {
	status := a.Status
	if status == "" {
		status = "firing"
	}
	sev := a.Severity
	if sev == "" {
		sev = "warning"
	}
	t := fmt.Sprintf("[%s:%s] %s", upper(status), sev, a.Name)
	if a.Summary != "" {
		t += " — " + a.Summary
	}
	return t
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if 'a' <= b[i] && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

// Notifier delivers one alert to one destination. Implementations must be safe
// for concurrent use and respect ctx cancellation.
type Notifier interface {
	Send(ctx context.Context, a *Alert) error
}

// httpClient is the shared default for the HTTP-based senders; tests inject
// their own via each sender's Client field.
var httpClient = &http.Client{Timeout: 10 * time.Second}
