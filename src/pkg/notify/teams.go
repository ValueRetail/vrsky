package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Teams posts alerts to a Microsoft Teams incoming webhook created with
// Workflows ("Post to a channel when a webhook request is received"). Those
// webhooks render Adaptive Cards, not free-form JSON, which is why the generic
// Webhook notifier cannot be used for Teams. The URL is a secret — anyone
// holding it can post to the channel — and is resolved from the vault by the
// caller, never stored here.
type Teams struct {
	WebhookURL string
	Client     *http.Client // nil -> shared default
}

// teamsColor maps severity/status to an Adaptive Card TextBlock color name.
func teamsColor(severity, status string) string {
	if status == "resolved" {
		return "Good"
	}
	switch severity {
	case "critical":
		return "Attention"
	case "warning":
		return "Warning"
	default:
		return "Accent"
	}
}

// teamsCard is the Adaptive Card for one alert: a coloured headline, the
// description, and a fact set an operator can act on without opening VRSky.
func teamsCard(a *Alert) map[string]interface{} {
	facts := []map[string]string{
		{"title": "Status", "value": upper(nonEmpty(a.Status, "firing"))},
		{"title": "Severity", "value": nonEmpty(a.Severity, "warning")},
	}
	if a.TenantID != "" {
		facts = append(facts, map[string]string{"title": "Workspace", "value": a.TenantID})
	}
	if !a.StartsAt.IsZero() {
		facts = append(facts, map[string]string{"title": "Since", "value": a.StartsAt.UTC().Format("2006-01-02 15:04 UTC")})
	}
	body := []map[string]interface{}{{
		"type":   "TextBlock",
		"size":   "Medium",
		"weight": "Bolder",
		"color":  teamsColor(a.Severity, a.Status),
		"wrap":   true,
		"text":   a.Title(),
	}}
	if a.Description != "" {
		body = append(body, map[string]interface{}{"type": "TextBlock", "wrap": true, "text": a.Description})
	}
	body = append(body, map[string]interface{}{"type": "FactSet", "facts": facts})
	return map[string]interface{}{
		"type": "message",
		"attachments": []map[string]interface{}{{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content": map[string]interface{}{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard",
				"version": "1.4",
				"msteams": map[string]string{"width": "Full"},
				"body":    body,
			},
		}},
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func (t *Teams) Send(ctx context.Context, a *Alert) error {
	if t.WebhookURL == "" {
		return fmt.Errorf("teams: webhook URL is empty")
	}
	body, err := json.Marshal(teamsCard(a))
	if err != nil {
		return fmt.Errorf("teams: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("teams: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := t.Client
	if client == nil {
		client = httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("teams: post: %w", err)
	}
	defer resp.Body.Close()
	// Workflows answers 202 Accepted; anything from 300 up is a refusal.
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("teams: webhook returned %d: %s", resp.StatusCode, b)
	}
	return nil
}
