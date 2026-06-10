package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// defaultPagerDutyURL is the PagerDuty Events API v2 enqueue endpoint.
const defaultPagerDutyURL = "https://events.pagerduty.com/v2/enqueue"

// PagerDuty triggers/resolves incidents via the Events API v2. RoutingKey is
// the integration key (a secret, resolved by the caller).
type PagerDuty struct {
	RoutingKey string
	Endpoint   string       // empty -> the real Events API; tests point at httptest
	Client     *http.Client // nil -> shared default
}

func pdSeverity(severity string) string {
	switch severity {
	case "critical", "warning", "info", "error":
		return severity
	default:
		return "warning"
	}
}

func (p *PagerDuty) Send(ctx context.Context, a *Alert) error {
	if p.RoutingKey == "" {
		return fmt.Errorf("pagerduty: routing key is empty")
	}
	action := "trigger"
	if a.Status == "resolved" {
		action = "resolve"
	}
	// dedup_key ties resolve events to the original trigger.
	dedup := a.Name
	if a.TenantID != "" {
		dedup += ":" + a.TenantID
	}
	payload := map[string]interface{}{
		"routing_key":  p.RoutingKey,
		"event_action": action,
		"dedup_key":    dedup,
		"payload": map[string]interface{}{
			"summary":        a.Title(),
			"severity":       pdSeverity(a.Severity),
			"source":         "vrsky",
			"custom_details": a.Labels,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pagerduty: marshal: %w", err)
	}
	url := p.Endpoint
	if url == "" {
		url = defaultPagerDutyURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("pagerduty: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := p.Client
	if client == nil {
		client = httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("pagerduty: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("pagerduty: events API returned %d: %s", resp.StatusCode, b)
	}
	return nil
}
