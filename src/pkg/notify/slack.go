package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Slack posts alerts to a Slack incoming webhook
// (https://api.slack.com/messaging/webhooks). The webhook URL is a secret —
// it is resolved from the secrets vault by the caller, never stored here.
type Slack struct {
	WebhookURL string
	Client     *http.Client // nil -> shared default
}

// slack message colors by severity (attachment bar).
func slackColor(severity, status string) string {
	if status == "resolved" {
		return "#2eb886" // green
	}
	switch severity {
	case "critical":
		return "#cc0000"
	case "warning":
		return "#daa038"
	default:
		return "#439fe0"
	}
}

func (s *Slack) Send(ctx context.Context, a *Alert) error {
	if s.WebhookURL == "" {
		return fmt.Errorf("slack: webhook URL is empty")
	}
	text := a.Title()
	if a.Description != "" {
		text += "\n" + a.Description
	}
	payload := map[string]interface{}{
		"attachments": []map[string]interface{}{{
			"color":    slackColor(a.Severity, a.Status),
			"fallback": a.Title(),
			"text":     text,
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := s.Client
	if client == nil {
		client = httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("slack: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack: webhook returned %d: %s", resp.StatusCode, b)
	}
	return nil
}
