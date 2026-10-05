package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/notify"
)

const (
	modeObserve = "observe" // diagnose and report; the action tools do not exist
	modeAct     = "act"     // may also take the allowlisted actions
	runTimeout  = 3 * time.Minute
)

// ignoredAlerts never get a run: the responder's own report (a loop), the
// operator's heartbeat, and the end-to-end test alert.
var ignoredAlerts = map[string]bool{notify.ResponderReportName: true, "Watchdog": true, "TestAlert": true, "InfoInhibitor": true}

type responder struct {
	mode     string
	model    Model
	api      *apiClient
	guard    *guard
	reporter reporter
	maxCalls int
	logger   *slog.Logger
	sem      chan struct{} // one run at a time
}

// subscribe listens for every alert the management API hands on. A queue
// group keeps a second replica from running the same alert twice.
func (s *responder) subscribe(nc *nats.Conn) (*nats.Subscription, error) {
	return nc.QueueSubscribe("vrsky.alerts.>", "alert-responder", func(m *nats.Msg) {
		var a notify.Alert
		if err := json.Unmarshal(m.Data, &a); err != nil {
			s.logger.Warn("alert that does not parse", "error", err)
			return
		}
		go s.handle(context.Background(), &a)
	})
}

// handle decides whether an alert gets a run, runs it, and posts the report.
func (s *responder) handle(ctx context.Context, a *notify.Alert) {
	log := s.logger.With("alert", a.Name, "tenant_id", a.TenantID, "status", a.Status)
	if ignoredAlerts[a.Name] || a.Severity == "info" {
		return
	}
	fp := fingerprint(a)

	if a.Status == "resolved" {
		// Only worth a card when the responder did something about it.
		if s.guard.takeActed(fp) {
			if err := s.reporter.post(ctx, a, "resolved", a.Name+": resolved after the responder's action", "The alert has cleared."); err != nil {
				log.Error("post resolved report", "error", err)
			}
		}
		return
	}
	if a.TenantID != "" && !s.api.hasTenant(a.TenantID) {
		log.Info("no API key configured for this workspace; alert not handled")
		return
	}
	if ok, why := s.guard.admitRun(fp); !ok {
		log.Info("alert not handled", "reason", why)
		return
	}

	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	r := &run{tenantID: a.TenantID, api: s.api, guard: s.guard, maxCalls: s.maxCalls,
		logAction: func(action, connID, reason string) {
			log.Info("responder action", "action", action, "connection_id", connID, "reason", reason)
		}}
	var tools []Tool
	if a.TenantID != "" { // a platform alert has no workspace to look into
		tools = r.tools(s.mode == modeAct)
	}

	started := time.Now()
	res, err := s.model.Run(ctx, systemPrompt, s.userMessage(a), tools)
	if err != nil {
		// The plain alert card is already in the channel; say nothing more
		// than the log rather than post a report about the responder itself.
		log.Error("responder run failed", "error", err, "tool_calls", r.calls)
		runsTotal.WithLabelValues("error").Inc()
		return
	}
	if len(r.actions) > 0 {
		s.guard.markActed(fp)
	}
	summary, body := splitReport(a, res.Text)
	body += fmt.Sprintf("\n\n(%s mode · %d tool calls · ~$%.2f)", s.mode, r.calls, costUSD(res))
	if err := s.reporter.post(ctx, a, "firing", summary, body); err != nil {
		log.Error("post report", "error", err)
		runsTotal.WithLabelValues("report_failed").Inc()
		return
	}
	runsTotal.WithLabelValues("reported").Inc()
	log.Info("responder run", "mode", s.mode, "tool_calls", r.calls, "actions", strings.Join(r.actions, "; "),
		"input_tokens", res.InputTokens, "output_tokens", res.OutputTokens, "cost_usd", fmt.Sprintf("%.3f", costUSD(res)),
		"duration", time.Since(started).Round(time.Second).String())
}

// userMessage is the alert as the model sees it.
func (s *responder) userMessage(a *notify.Alert) string {
	labels, _ := json.Marshal(a.Labels)
	scope := "Workspace: " + a.TenantID
	if a.TenantID == "" {
		scope = "This is a platform alert: it has no workspace and you have no tools for it."
	}
	mode := "You are in observe mode: there are no action tools. Diagnose, and say what you would have done."
	if s.mode == modeAct && a.TenantID != "" {
		mode = "You are in act mode: the action tools are available, within their limits."
	}
	since := ""
	if !a.StartsAt.IsZero() {
		since = "\nFiring since: " + a.StartsAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("Alert: %s\nSeverity: %s\nSummary: %s\nDescription: %s\nLabels: %s%s\n%s\n\n%s",
		a.Name, a.Severity, a.Summary, a.Description, labels, since, scope, mode)
}
