package managementapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"time"

	"github.com/ValueRetail/vrsky/pkg/promquery"
)

// Public status page (Phase 4D / #95), auto-driven by Prometheus `up` probe
// data. Served by the management API at:
//
//	GET /status        human-readable HTML
//	GET /status.json   machine/AI-readable JSON
//
// Both are public (no auth — exempted from the tenant middleware). Because the
// page is served by the management API it can't report its own hard-down state;
// docs/SLA.md notes that a production status page should be mirrored externally.

// statusComponents maps user-facing platform components to the Prometheus job
// whose `up` series reflects their health. min() collapses multi-instance jobs
// (e.g. all workers) to a single worst-case value.
var statusComponents = []struct {
	Name string
	Desc string
	Job  string
}{
	{"Management API", "REST API and control plane", "management-api"},
	{"Data plane", "Connector workers (consumers, producers, filters)", "vrsky-workers"},
	{"Message broker", "NATS JetStream", "nats"},
	{"API gateway", "Traefik edge / rate limiting", "traefik"},
	{"Monitoring", "Prometheus metrics", "prometheus"},
}

// ComponentStatus is one row of the status page.
type ComponentStatus struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Status      string  `json:"status"` // "up" | "down" | "unknown"
	Uptime24h   float64 `json:"uptime_24h"`
	Uptime7d    float64 `json:"uptime_7d"`
}

// StatusResponse is the GET /status.json payload.
type StatusResponse struct {
	Status      string            `json:"status"` // operational | degraded | major_outage | unknown
	GeneratedAt string            `json:"generated_at"`
	Components  []ComponentStatus `json:"components"`
}

// SetPrometheus wires the Prometheus client used by the status page. nil (no
// PROMETHEUS_URL) makes every component report "unknown".
func (h *Handler) SetPrometheus(c *promquery.Client) { h.prom = c }

// buildStatus queries Prometheus for each component's current up/down and uptime
// over 24h / 7d, and rolls up an overall status.
func (h *Handler) buildStatus(ctx context.Context) StatusResponse {
	resp := StatusResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Components:  make([]ComponentStatus, 0, len(statusComponents)),
	}

	upCount, downCount := 0, 0
	for _, c := range statusComponents {
		cs := ComponentStatus{Name: c.Name, Description: c.Desc, Status: "unknown"}
		if h.prom != nil {
			if v, ok, err := h.prom.QueryScalar(ctx, fmt.Sprintf(`min(up{job=%q})`, c.Job)); err == nil && ok {
				if v >= 1 {
					cs.Status = "up"
					upCount++
				} else {
					cs.Status = "down"
					downCount++
				}
			}
			if v, ok, err := h.prom.QueryScalar(ctx, fmt.Sprintf(`min(avg_over_time(up{job=%q}[24h]))`, c.Job)); err == nil && ok {
				cs.Uptime24h = v
			}
			if v, ok, err := h.prom.QueryScalar(ctx, fmt.Sprintf(`min(avg_over_time(up{job=%q}[7d]))`, c.Job)); err == nil && ok {
				cs.Uptime7d = v
			}
		}
		resp.Components = append(resp.Components, cs)
	}

	switch {
	case h.prom == nil || (upCount == 0 && downCount == 0):
		resp.Status = "unknown"
	case downCount == 0:
		resp.Status = "operational"
	case upCount == 0:
		resp.Status = "major_outage"
	default:
		resp.Status = "degraded"
	}
	return resp
}

// ServeStatusJSON serves the machine-readable status.
func (h *Handler) ServeStatusJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(h.buildStatus(r.Context()))
}

// ServeStatusPage serves a self-contained HTML status page (no external assets).
func (h *Handler) ServeStatusPage(w http.ResponseWriter, r *http.Request) {
	s := h.buildStatus(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprint(w, renderStatusHTML(s))
}

func renderStatusHTML(s StatusResponse) string {
	banner := map[string]struct{ label, color string }{
		"operational":  {"All systems operational", "#059669"},
		"degraded":     {"Degraded performance", "#d97706"},
		"major_outage": {"Major outage", "#dc2626"},
		"unknown":      {"Status unavailable", "#6b7280"},
	}[s.Status]

	dot := func(status string) string {
		c := map[string]string{"up": "#059669", "down": "#dc2626", "unknown": "#9ca3af"}[status]
		return fmt.Sprintf(`<span style="display:inline-block;width:10px;height:10px;border-radius:50%%;background:%s"></span>`, c)
	}
	pct := func(f float64) string {
		if f <= 0 {
			return "—"
		}
		return fmt.Sprintf("%.2f%%", f*100)
	}

	var rows string
	for _, c := range s.Components {
		rows += fmt.Sprintf(`<tr style="border-top:1px solid #e5e7eb">
      <td style="padding:12px 8px">%s &nbsp;<strong>%s</strong><div style="color:#6b7280;font-size:12px">%s</div></td>
      <td style="padding:12px 8px;text-transform:capitalize">%s</td>
      <td style="padding:12px 8px;text-align:right">%s</td>
      <td style="padding:12px 8px;text-align:right">%s</td>
    </tr>`, dot(c.Status), html.EscapeString(c.Name), html.EscapeString(c.Description),
			html.EscapeString(c.Status), pct(c.Uptime24h), pct(c.Uptime7d))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>VRSky Status</title></head>
<body style="font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;max-width:760px;margin:40px auto;padding:0 16px;color:#111827">
  <h1 style="font-size:22px;margin-bottom:4px">VRSky platform status</h1>
  <div style="display:inline-block;padding:8px 14px;border-radius:8px;color:#fff;background:%s;font-weight:600;margin:10px 0 20px">%s</div>
  <table style="width:100%%;border-collapse:collapse;font-size:14px">
    <thead><tr style="text-align:left;color:#6b7280;font-size:12px">
      <th style="padding:6px 8px">Component</th><th style="padding:6px 8px">Status</th>
      <th style="padding:6px 8px;text-align:right">Uptime (24h)</th>
      <th style="padding:6px 8px;text-align:right">Uptime (7d)</th>
    </tr></thead>
    <tbody>%s</tbody>
  </table>
  <p style="color:#9ca3af;font-size:12px;margin-top:18px">Last updated %s · auto-generated from Prometheus probe data ·
  <a href="/status.json" style="color:#2563eb">JSON</a></p>
</body></html>`, banner.color, html.EscapeString(banner.label), rows, html.EscapeString(s.GeneratedAt))
}
