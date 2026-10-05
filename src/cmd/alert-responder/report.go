package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/notify"
)

// reporter posts the responder's findings where operators already look: it
// sends a ResponderReport alert through the management API's Alertmanager
// webhook, which delivers it to the same notification targets (Teams, …) as
// the alert it answers. The webhook never hands a ResponderReport back.
type reporter interface {
	post(ctx context.Context, about *notify.Alert, status, summary, body string) error
}

type webhookReporter struct {
	url   string // the management API's /api/v1/alerts/webhook
	token string // ALERTS_WEBHOOK_TOKEN
	http  *http.Client
}

func (w *webhookReporter) post(ctx context.Context, about *notify.Alert, status, summary, body string) error {
	labels := map[string]string{
		"alertname": notify.ResponderReportName,
		"severity":  about.Severity,
		"about":     about.Name,
	}
	if about.TenantID != "" {
		labels["tenant_id"] = about.TenantID
	}
	payload, err := json.Marshal(map[string]any{
		"status": status,
		"alerts": []map[string]any{{
			"status":      status,
			"labels":      labels,
			"annotations": map[string]string{"summary": summary, "description": body},
			"startsAt":    time.Now().UTC(),
		}},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.token)
	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("post report: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("post report: HTTP %d: %s", resp.StatusCode, b)
	}
	return nil
}

// splitReport separates the model's "SUMMARY: …" first line from the body.
// A report without one still goes out, with a summary that says what it is
// about, rather than being dropped over a format slip.
func splitReport(about *notify.Alert, text string) (summary, body string) {
	text = strings.TrimSpace(text)
	first, rest, _ := strings.Cut(text, "\n")
	if s, ok := strings.CutPrefix(strings.TrimSpace(first), "SUMMARY:"); ok && strings.TrimSpace(s) != "" {
		return about.Name + ": " + strings.TrimSpace(s), strings.TrimSpace(rest)
	}
	return "diagnosis of " + about.Name, text
}
